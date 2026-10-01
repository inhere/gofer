package job

import (
	"testing"
)

// remoteSessionSender is a worker selector that also carries remote session
// commands, recording what was sent.
type remoteSessionSender struct{ sent []string }

func (r *remoteSessionSender) Candidates() []WorkerCandidate { return nil }
func (r *remoteSessionSender) Candidate(string) (WorkerCandidate, bool) {
	return WorkerCandidate{}, false
}
func (r *remoteSessionSender) SendSessionCommand(_, _, _, action, prompt string) error {
	r.sent = append(r.sent, action+":"+prompt)
	return nil
}
func (r *remoteSessionSender) IsWorkerOnline(string) bool { return true }

// TestRemoteSessionAcceptsSayEveryTurn: the host marked a remote say as pending
// and never cleared it, so a remote session took exactly one `say` and every later
// one failed with "session input already queued" (found by the v0.87 real-process
// smoke: turn 3 never ran). A worker report of a later turn consumes the pending
// command — whether the turn_started frame arrived or only the awaiting_input one.
func TestRemoteSessionAcceptsSayEveryTurn(t *testing.T) {
	root := t.TempDir()
	sender := &remoteSessionSender{}
	s := newWorkerTestServiceSel(t, root, &stubWorkerRunner{}, nil, sender)
	result := JobResult{
		ID: "remote-session-say", ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1", Session: true, Status: StatusAwaitingInput, TurnNo: 1,
		Cwd: ".", ResultDir: t.TempDir(),
	}
	if err := s.persist(result); err != nil {
		t.Fatal(err)
	}
	entry := &jobEntry{result: result, done: make(chan struct{})}
	s.mu.Lock()
	s.jobs[result.ID] = entry
	s.mu.Unlock()

	if err := s.SaySession(result.ID, "second"); err != nil {
		t.Fatalf("say turn 2: %v", err)
	}
	s.applyRemoteSessionState(entry, WorkerInflightJob{JobID: result.ID, SessionStatus: "turn_started", TurnNo: 2}, false)
	s.applyRemoteSessionState(entry, WorkerInflightJob{JobID: result.ID, SessionStatus: StatusAwaitingInput, TurnNo: 2}, false)
	if err := s.SaySession(result.ID, "third"); err != nil {
		t.Fatalf("say turn 3: %v", err)
	}
	// turn_started for turn 3 lost on the wire: the awaiting_input report alone
	// still proves the command was consumed.
	s.applyRemoteSessionState(entry, WorkerInflightJob{JobID: result.ID, SessionStatus: StatusAwaitingInput, TurnNo: 3}, false)
	if err := s.SaySession(result.ID, "fourth"); err != nil {
		t.Fatalf("say turn 4: %v", err)
	}
	// Until the worker reports a later turn, a second say is still refused.
	if err := s.SaySession(result.ID, "too-early"); err == nil {
		t.Fatal("second say before the worker consumed the first was accepted")
	}
	if len(sender.sent) != 3 {
		t.Fatalf("sent = %v, want three says", sender.sent)
	}
}
