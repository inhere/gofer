package job

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingSessionSender is a worker selector carrying remote session commands: it
// records every command it was asked to send (worker id, cmd id, action, prompt) and
// can be made slow or failing on demand.
type recordingSessionSender struct {
	mu      sync.Mutex
	sent    []sessionCommand
	online  bool
	sendDur time.Duration
	err     error
	// onSend runs inside a send, before it returns: it stands in for a worker that
	// answers the frame by itself (e.g. by ending the session).
	onSend func()
}

type sessionCommand struct{ jobID, cmdID, action, prompt string }

func (r *recordingSessionSender) Candidates() []WorkerCandidate { return nil }
func (r *recordingSessionSender) Candidate(string) (WorkerCandidate, bool) {
	return WorkerCandidate{}, false
}

func (r *recordingSessionSender) SendSessionCommand(_, jobID, cmdID, action, prompt string) error {
	if d := r.sendDur; d > 0 {
		time.Sleep(d)
	}
	if hook := r.onSend; hook != nil {
		hook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, sessionCommand{jobID: jobID, cmdID: cmdID, action: action, prompt: prompt})
	return nil
}

func (r *recordingSessionSender) IsWorkerOnline(string) bool { return r.online }

func (r *recordingSessionSender) sentCommands() []sessionCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessionCommand(nil), r.sent...)
}

// remoteSayService builds a Service whose worker sessions can be said to, plus the
// recorder behind it.
func remoteSayService(t *testing.T, sendDur time.Duration) (*Service, *recordingSessionSender) {
	t.Helper()
	sender := &recordingSessionSender{online: true, sendDur: sendDur}
	s := newWorkerTestServiceSel(t, t.TempDir(), &stubWorkerRunner{}, nil, sender)
	return s, sender
}

// addRemoteSaySession stores and registers a live remote session entry for the test.
func addRemoteSaySession(t *testing.T, s *Service, id, status string, turn int) *jobEntry {
	t.Helper()
	result := JobResult{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1", Session: true, Status: status, TurnNo: turn,
		Cwd: ".", ResultDir: t.TempDir(),
	}
	if err := s.persist(result); err != nil {
		t.Fatal(err)
	}
	entry := &jobEntry{result: result, done: make(chan struct{})}
	s.mu.Lock()
	s.jobs[id] = entry
	s.mu.Unlock()
	return entry
}

// TestSayRefusalReasonsAreDistinguishable: every say refusal keeps the error and the
// text the HTTP/CLI surfaces already reported (ErrJobNotRunning / ErrInvalidRequest
// plus the exact message) and adds a reason the receipt path can branch on — the
// "retry later" three from the "the session is gone" two (design 2026-11-03 T2).
func TestSayRefusalReasonsAreDistinguishable(t *testing.T) {
	s, sender := remoteSayService(t, 0)

	cases := []struct {
		name   string
		setup  func() string
		reason error
		base   error
		want   string
	}{
		{
			name: "turn in progress",
			setup: func() string {
				id := "say-busy"
				addRemoteSaySession(t, s, id, StatusRunning, 2)
				return id
			},
			reason: ErrSessionTurnBusy, base: ErrJobNotRunning,
			want: ErrJobNotRunning.Error() + ": job say-busy is not awaiting ACP session input",
		},
		{
			name: "input already queued",
			setup: func() string {
				id := "say-queued"
				entry := addRemoteSaySession(t, s, id, StatusAwaitingInput, 1)
				entry.sessionCommandPending = true
				return id
			},
			reason: ErrSessionInputQueued, base: ErrInvalidRequest,
			want: ErrInvalidRequest.Error() + ": session input already queued",
		},
		{
			name: "worker offline",
			setup: func() string {
				sender.online = false
				id := "say-offline"
				addRemoteSaySession(t, s, id, StatusAwaitingInput, 1)
				return id
			},
			reason: ErrWorkerOffline, base: ErrJobNotRunning,
			want: ErrJobNotRunning.Error() + ": worker offline, retry later",
		},
		{
			name: "session ending",
			setup: func() string {
				sender.online = true
				id := "say-ending"
				entry := addRemoteSaySession(t, s, id, StatusAwaitingInput, 1)
				entry.sessionEnding = true
				return id
			},
			reason: ErrSessionEnding, base: ErrJobNotRunning,
			want: ErrJobNotRunning.Error() + ": session is ending",
		},
		{
			name: "no live session",
			setup: func() string {
				id := "say-local-terminal"
				if err := s.persist(JobResult{ID: id, ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
					WorkerID: "w1", Cwd: ".", ResultDir: t.TempDir()}); err != nil {
					t.Fatal(err)
				}
				return id
			},
			reason: ErrNoLiveSession, base: ErrJobNotRunning,
			want: ErrJobNotRunning.Error() + ": job say-local-terminal has no live ACP session",
		},
	}
	for _, tc := range cases {
		id := tc.setup()
		err := s.SaySession(id, "hello")
		if err == nil {
			t.Fatalf("%s: say was accepted", tc.name)
		}
		if !errors.Is(err, tc.reason) {
			t.Fatalf("%s: errors.Is(%v) = false, want the %v reason", tc.name, err, tc.reason)
		}
		if !errors.Is(err, tc.base) {
			t.Fatalf("%s: err = %v, want it to still be %v", tc.name, err, tc.base)
		}
		if err.Error() != tc.want {
			t.Fatalf("%s: message = %q, want the unchanged %q", tc.name, err.Error(), tc.want)
		}
		// The reasons are distinct: a receipt path must not confuse "retry later" with
		// "gone".
		for _, other := range []error{ErrSessionTurnBusy, ErrSessionInputQueued, ErrWorkerOffline, ErrSessionEnding, ErrNoLiveSession} {
			if other != tc.reason && errors.Is(err, other) {
				t.Fatalf("%s: err = %v also matches %v", tc.name, err, other)
			}
		}
	}
}

// TestRemoteSayReservesBeforeSending: two says racing for one remote session must
// produce ONE frame. The reservation is taken under entry.mu BEFORE the send, so the
// loser is refused instead of having its frame dropped by the worker with nobody
// noticing (the send is deliberately slow here to keep the window wide open).
func TestRemoteSayReservesBeforeSending(t *testing.T) {
	s, sender := remoteSayService(t, 50*time.Millisecond)
	addRemoteSaySession(t, s, "say-race", StatusAwaitingInput, 1)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = s.SaySession("say-race", "hello")
		}(i)
	}
	close(start)
	wg.Wait()

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		if !errors.Is(err, ErrSessionInputQueued) {
			t.Fatalf("loser err = %v, want ErrSessionInputQueued", err)
		}
	}
	if ok != 1 {
		t.Fatalf("accepted says = %d (%v), want exactly one", ok, errs)
	}
	if got := sender.sentCommands(); len(got) != 1 {
		t.Fatalf("frames sent = %d, want exactly one", len(got))
	}
}

// TestRemoteSayRollsBackOnSendFailure: a send that fails frees the reservation, so
// the caller can retry immediately — but a failure the worker answered by ENDING the
// session keeps it (the session is gone; a retry must not look merely queued).
func TestRemoteSayRollsBackOnSendFailure(t *testing.T) {
	s, sender := remoteSayService(t, 0)
	entry := addRemoteSaySession(t, s, "say-fail", StatusAwaitingInput, 1)
	sender.err = errors.New("worker frame lost")

	if err := s.SaySession("say-fail", "hello"); err == nil {
		t.Fatal("say reported success although the frame failed")
	}
	entry.mu.Lock()
	pending, cmdID := entry.sessionCommandPending, entry.sessionCommandID
	entry.mu.Unlock()
	if pending || cmdID != "" {
		t.Fatalf("pending = %v cmdID = %q, want the reservation rolled back", pending, cmdID)
	}

	// The worker answering by ENDING the session keeps the reservation: the session is
	// gone, so a retry must not look merely queued.
	sender.err = errors.New("session ended")
	sender.onSend = func() {
		entry.mu.Lock()
		entry.sessionEnding = true
		entry.mu.Unlock()
	}
	if err := s.SaySession("say-fail", "hello"); err == nil {
		t.Fatal("say reported success although the frame failed")
	}
	entry.mu.Lock()
	pending = entry.sessionCommandPending
	entry.mu.Unlock()
	if !pending {
		t.Fatal("a send failure on an ending session rolled the reservation back")
	}
}

// TestResendSessionSayReusesBatchCommandID: a re-send goes out with the SAME command
// id as its batch (the worker drops ids it has seen), and only while that batch still
// owns the slot and no turn has started since — a re-send after the worker consumed
// the batch, or after a new batch took the slot, sends nothing.
func TestResendSessionSayReusesBatchCommandID(t *testing.T) {
	s, sender := remoteSayService(t, 0)
	addRemoteSaySession(t, s, "say-resend", StatusAwaitingInput, 1)

	if err := s.SaySession("say-resend", "first"); err != nil {
		t.Fatalf("SaySession: %v", err)
	}
	sent := sender.sentCommands()
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one frame", sent)
	}
	batch := sent[0].cmdID
	if batch == "" {
		t.Fatal("the batch carried no command id")
	}

	// The tick re-sends the unconfirmed batch: same id, second frame.
	if err := s.resendSessionSay("say-resend", batch, "first"); err != nil {
		t.Fatalf("resend: %v", err)
	}
	sent = sender.sentCommands()
	if len(sent) != 2 || sent[1].cmdID != batch || sent[1].prompt != "first" {
		t.Fatalf("sent = %+v, want the same command id re-sent", sent)
	}

	// Once the worker reports the NEXT turn, the batch was consumed: nothing to re-send.
	s.applyRemoteSessionState(s.entryForTest(t, "say-resend"), WorkerInflightJob{
		JobID: "say-resend", SessionStatus: StatusAwaitingInput, TurnNo: 2,
	}, false)
	if err := s.resendSessionSay("say-resend", batch, "first"); err == nil {
		t.Fatal("a consumed batch was re-sent")
	}
	if got := len(sender.sentCommands()); got != 2 {
		t.Fatalf("frames sent = %d, want no third frame", got)
	}

	// A NEW batch gets a NEW id; the old batch's id must not re-send over it.
	if err := s.SaySession("say-resend", "second"); err != nil {
		t.Fatalf("SaySession(second): %v", err)
	}
	sent = sender.sentCommands()
	if len(sent) != 3 || sent[2].cmdID == batch {
		t.Fatalf("sent = %+v, want a new batch with a fresh command id", sent)
	}
	if err := s.resendSessionSay("say-resend", batch, "first"); err == nil {
		t.Fatal("a stale batch id re-sent over the current batch")
	}
	if got := len(sender.sentCommands()); got != 3 {
		t.Fatalf("frames sent = %d, want no fourth frame", got)
	}
}

// TestResendSessionSayNeedsUnchangedTurn: a batch reserved on turn 1 is NOT re-sent
// after a turn started without the pending flag being cleared (the worker may have
// consumed it) — the guard is the turn number, not only the pending flag.
func TestResendSessionSayNeedsUnchangedTurn(t *testing.T) {
	s, sender := remoteSayService(t, 0)
	entry := addRemoteSaySession(t, s, "say-turn", StatusAwaitingInput, 1)
	if err := s.SaySession("say-turn", "hello"); err != nil {
		t.Fatalf("SaySession: %v", err)
	}
	cmdID := sender.sentCommands()[0].cmdID

	entry.mu.Lock()
	entry.result.TurnNo = 2
	entry.mu.Unlock()
	err := s.resendSessionSay("say-turn", cmdID, "hello")
	if !errors.Is(err, ErrSessionTurnBusy) {
		t.Fatalf("resend err = %v, want ErrSessionTurnBusy", err)
	}
	if got := len(sender.sentCommands()); got != 1 {
		t.Fatalf("frames sent = %d, want no re-send after a turn started", got)
	}
}

// entryForTest returns the live entry of a job (the remote-say tests own it).
func (s *Service) entryForTest(t *testing.T, id string) *jobEntry {
	t.Helper()
	entry := s.entry(id)
	if entry == nil {
		t.Fatalf("job %s has no live entry", id)
	}
	return entry
}
