package sessionrelay

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	_ "modernc.org/sqlite" // the "sqlite" driver the store opens (legacy-db test)

	"github.com/inhere/gofer/internal/jobstore"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s := NewService(st)
	s.SetPollInterval(10 * time.Millisecond)
	return s
}

// fakeNotifier records the outbound notifications the relay raises.
type fakeNotifier struct {
	waiting   []string
	attention []string
	handedOff []string
	released  []string
}

func (f *fakeNotifier) NotifySessionWaiting(sid, _, _, msg string, _ int64) {
	f.waiting = append(f.waiting, sid+":"+msg)
}
func (f *fakeNotifier) NotifySessionAttention(sid, _, _, detail string) {
	f.attention = append(f.attention, sid+":"+detail)
}
func (f *fakeNotifier) NotifySessionHandedOff(sid, _, _, jobID string) {
	f.handedOff = append(f.handedOff, sid+":"+jobID)
}
func (f *fakeNotifier) NotifySessionTakeoverReleased(sid, _, _, jobID, reason string) {
	f.released = append(f.released, sid+":"+jobID+":"+reason)
}

// TestNotifierHooks covers when the relay pushes to the outbound notifier:
// every opened turn, and only the TRANSITION into needs_attention while the
// session is relayed.
func TestNotifierHooks(t *testing.T) {
	s := newSvc(t)
	f := &fakeNotifier{}
	s.SetNotifier(f)
	_, err := s.Register(RegisterInput{SessionID: "sid-n", Agent: "claude"})
	assert.NoErr(t, err)

	// relay off: attention raises no notification (the human is at the keyboard)
	_, err = s.Heartbeat("sid-n", HeartbeatInput{Event: EventNotification, LastMessage: "允许运行 rm?"})
	assert.NoErr(t, err)
	assert.Len(t, f.attention, 0)

	_, _ = s.SetRelayMode("sid-n", jobstore.RelayModeOn)

	// same prompt re-raised while already needs_attention: suppressed
	_, err = s.Heartbeat("sid-n", HeartbeatInput{Event: EventNotification, LastMessage: "允许运行 rm?"})
	assert.NoErr(t, err)
	assert.Len(t, f.attention, 0)

	// a DIFFERENT prompt while still waiting is new information: notified
	_, err = s.Heartbeat("sid-n", HeartbeatInput{Event: EventNotification, LastMessage: "允许写入 /etc?"})
	assert.NoErr(t, err)
	assert.Len(t, f.attention, 1)
	assert.Eq(t, "sid-n:允许写入 /etc?", f.attention[0])

	// leaving and re-entering notifies again, even with no detail text
	_, err = s.Heartbeat("sid-n", HeartbeatInput{Event: EventUserPromptSubmit, Injected: true})
	assert.NoErr(t, err)
	_, err = s.Heartbeat("sid-n", HeartbeatInput{Event: EventNotification})
	assert.NoErr(t, err)
	assert.Len(t, f.attention, 2)

	// an opened turn always notifies
	_, err = s.OpenTurn("sid-n", "选 A 还是 B？", 60)
	assert.NoErr(t, err)
	assert.Len(t, f.waiting, 1)
	assert.Eq(t, "sid-n:选 A 还是 B？", f.waiting[0])
}

func TestRelayHappyPath(t *testing.T) {
	s := newSvc(t)
	a, err := s.Register(RegisterInput{SessionID: "sid-a", Agent: "Claude", Cwd: "/w/repo", Event: EventSessionStart})
	assert.NoErr(t, err)
	assert.Eq(t, "claude", a.Agent)
	assert.False(t, a.Relay)

	// Relay off: Stop heartbeat → idle, no turn possible.
	hb, err := s.Heartbeat("sid-a", HeartbeatInput{Event: EventStop, LastMessage: "first stop"})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.SessionIdle, hb.State)
	assert.False(t, hb.Relay)
	_, err = s.OpenTurn("sid-a", "x", 60)
	assert.True(t, errors.Is(err, ErrRelayOff))

	// Switch on, open a turn, wait → answered via Say.
	on, err := s.SetRelayMode("sid-a", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	assert.True(t, on.Relay)
	d, err := s.OpenTurn("sid-a", "need a decision", 60)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.DecisionKindRelay, d.Kind)
	assert.Eq(t, "sid-a", d.SessionID)
	assert.True(t, len(d.Title) > 0)
	got, _ := s.Get("sid-a", 10)
	assert.Eq(t, jobstore.SessionWaitingReply, got.Session.State)
	assert.Eq(t, int64(1), got.Session.TurnNo)
	assert.Len(t, got.Turns, 1)

	st, err := s.WaitTurn(context.Background(), "sid-a", d.ID, 0)
	assert.NoErr(t, err)
	assert.Eq(t, TurnOpen, st.Outcome)

	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = s.Say("sid-a", "go with plan B", "human")
	}()
	st, err = s.WaitTurn(context.Background(), "sid-a", d.ID, 2*time.Second)
	assert.NoErr(t, err)
	assert.Eq(t, TurnAnswered, st.Outcome)
	assert.Eq(t, "go with plan B", st.Decision.Answer)
	got, _ = s.Get("sid-a", 10)
	assert.Eq(t, jobstore.SessionRunning, got.Session.State)

	// Nothing open now.
	_, err = s.Say("sid-a", "again", "human")
	assert.True(t, errors.Is(err, ErrNoOpenTurn))
}

func TestRelayOffReleasesWaitAndAutoOff(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sid-b", Agent: "codex"})
	assert.NoErr(t, err)
	_, err = s.SetRelayMode("sid-b", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	d, err := s.OpenTurn("sid-b", "waiting", 60)
	assert.NoErr(t, err)

	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = s.SetRelayMode("sid-b", jobstore.RelayModeOff)
	}()
	st, err := s.WaitTurn(context.Background(), "sid-b", d.ID, 2*time.Second)
	assert.NoErr(t, err)
	assert.Eq(t, TurnRelayOff, st.Outcome)
	assert.False(t, st.Relay)
	got, _ := s.Get("sid-b", 5)
	assert.Eq(t, jobstore.SessionIdle, got.Session.State)
	// Once the mode is off the turn is expired.
	time.Sleep(50 * time.Millisecond)
	expired, _, _ := s.store.GetDecision(d.ID)
	assert.Eq(t, jobstore.DecisionExpired, expired.State)

	// A human prompt drops an explicit `on` back to `auto` (they are back at the
	// keyboard: the switch hands the decision back to the idle rules).
	_, err = s.SetRelayMode("sid-b", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	hb, err := s.Heartbeat("sid-b", HeartbeatInput{Event: EventUserPromptSubmit, Title: "repo: task"})
	assert.NoErr(t, err)
	assert.False(t, hb.Relay)
	assert.Eq(t, jobstore.RelayModeAuto, hb.RelayMode)
	assert.Eq(t, jobstore.SessionRunning, hb.State)
	assert.Eq(t, "repo: task", hb.Title)

	// An injected continuation (the relay's own reply) never auto-offs.
	_, _ = s.SetRelayMode("sid-b", jobstore.RelayModeOn)
	hb, _ = s.Heartbeat("sid-b", HeartbeatInput{Event: EventUserPromptSubmit, Injected: true})
	assert.True(t, hb.Relay)

	s.AutoOffOnPrompt = false
	hb, _ = s.Heartbeat("sid-b", HeartbeatInput{Event: EventUserPromptSubmit})
	assert.True(t, hb.Relay)
}

func TestRelayExpiryAndErrors(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sid-c", Agent: "claude"})
	assert.NoErr(t, err)
	_, _ = s.SetRelayMode("sid-c", jobstore.RelayModeOn)
	d, err := s.OpenTurn("sid-c", "short fuse", 2) // store min clamp = 2s
	assert.NoErr(t, err)
	assert.Eq(t, int64(2), d.TimeoutSec)

	st, err := s.WaitTurn(context.Background(), "sid-c", d.ID, 4*time.Second)
	assert.NoErr(t, err)
	assert.Eq(t, TurnExpired, st.Outcome)
	idle, _ := s.Get("sid-c", 1)
	assert.Eq(t, jobstore.SessionIdle, idle.Session.State) // expired turn → idle, relay stays on
	assert.True(t, idle.Session.Relay)

	// A new turn expires the stale one and is the only answerable turn.
	d2, err := s.OpenTurn("sid-c", "again", 60)
	assert.NoErr(t, err)
	open, _ := s.store.ListSessionDecisions("sid-c", jobstore.DecisionOpen, 10)
	assert.Len(t, open, 1)
	assert.Eq(t, d2.ID, open[0].ID)

	_, err = s.WaitTurn(context.Background(), "sid-c", "dec-nope", 0)
	assert.True(t, errors.Is(err, ErrUnknownTurn))
	_, err = s.WaitTurn(context.Background(), "sid-other", d2.ID, 0)
	assert.True(t, errors.Is(err, ErrUnknownTurn))
	_, err = s.Heartbeat("sid-nope", HeartbeatInput{Event: EventStop})
	assert.True(t, errors.Is(err, ErrUnknownSession))
	_, err = s.Register(RegisterInput{SessionID: "sid-new"})
	assert.True(t, errors.Is(err, ErrInvalidInput))
	_, err = s.Say("sid-c", "  ", "h")
	assert.True(t, errors.Is(err, ErrInvalidInput))
	assert.True(t, errors.Is(s.Delete("sid-nope"), ErrUnknownSession))

	// OnAnswered from the generic decision endpoint moves waiting → running.
	ok, _ := s.store.AnswerDecision(d2.ID, "via bell", "h")
	assert.True(t, ok)
	full, _, _ := s.store.GetDecision(d2.ID)
	s.OnAnswered(full)
	got, _ := s.Get("sid-c", 5)
	assert.Eq(t, jobstore.SessionRunning, got.Session.State)

	// Resolve by cwd.
	_, _ = s.Register(RegisterInput{SessionID: "sid-d", Agent: "claude", Cwd: "/w/repo"})
	found, err := s.Resolve("/w/repo/sub")
	assert.NoErr(t, err)
	assert.Len(t, found, 1)
	assert.Eq(t, "sid-d", found[0].SessionID)
	assert.NoErr(t, s.Delete("sid-d"))
}

// TestRelayDecisionTable is the R1/R2 rule table: for every relay mode and every
// combination of readings, which wait does a Stop get — and why. The two auto
// criteria interact in exactly one place: a WORKING keyboard probe (idle >= 0)
// is authoritative while its criterion is enabled, so a machine where the human
// is demonstrably present never waits on the input clock alone; a disabled
// criterion falls through to the other one.
func TestRelayDecisionTable(t *testing.T) {
	const now = int64(1_700_000_000)
	const unknown = int64(-1)
	const never = int64(0) // last_human_at 0 = no human ever seen
	cases := []struct {
		name     string
		mode     string
		idle     int64
		humanAgo int64 // seconds since the last human input; 0 = never
		idleSec  int
		turnSec  int
		want     string
	}{
		// An explicit switch is authoritative over every reading.
		{"on ignores an unknowable keyboard", jobstore.RelayModeOn, unknown, never, 300, 900, WaitModeOn},
		{"on waits even while the human is typing", jobstore.RelayModeOn, 3, 5, 300, 900, WaitModeOn},
		{"off never waits, however long the silence", jobstore.RelayModeOff, 9999, 9999, 300, 900, ""},
		{"off ignores a missing probe", jobstore.RelayModeOff, unknown, never, 300, 900, ""},
		// auto with a working probe: the keyboard reading decides.
		{"probe at the threshold waits", jobstore.RelayModeAuto, 300, 9000, 300, 900, WaitIdleProbe},
		{"probe above the threshold waits", jobstore.RelayModeAuto, 901, never, 300, 900, WaitIdleProbe},
		{"probe below the threshold does not wait", jobstore.RelayModeAuto, 299, 9000, 300, 900, ""},
		{"a zero reading is a reading, not unknown", jobstore.RelayModeAuto, 0, 9000, 300, 900, ""},
		// auto without a probe (containers): the human-input clock decides.
		{"no probe waits on turn age", jobstore.RelayModeAuto, unknown, 900, 300, 900, WaitTurnAge},
		{"no probe, below the turn age", jobstore.RelayModeAuto, unknown, 899, 300, 900, ""},
		{"no probe and no human ever seen is not evidence", jobstore.RelayModeAuto, unknown, never, 300, 900, ""},
		// 0 disables one criterion (falling through to the other).
		{"idle criterion off falls back to turn age", jobstore.RelayModeAuto, unknown, 900, 0, 900, WaitTurnAge},
		{"idle criterion off with a reading still uses turn age", jobstore.RelayModeAuto, 9999, 900, 0, 900, WaitTurnAge},
		{"turn criterion off blocks the fallback", jobstore.RelayModeAuto, unknown, 9999, 300, 0, ""},
		{"both criteria off never wait", jobstore.RelayModeAuto, unknown, 9999, 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSvc(t)
			s.AutoArmIdleSec, s.AutoArmTurnSec = tc.idleSec, tc.turnSec
			s.nowFn = func() time.Time { return time.Unix(now, 0) }
			a := jobstore.AgentSession{SessionID: "sid-t", RelayMode: tc.mode, IdleSec: tc.idle}
			if tc.humanAgo > 0 {
				a.LastHumanAt = now - tc.humanAgo
			}
			assert.Eq(t, tc.want, s.WaitReason(a))
			// The arming gate the hook's turn-open obeys follows the same rule.
			assert.Eq(t, tc.want != "", s.WaitReason(a) != "")
		})
	}
}

// TestRelayModeMigrationFromBool opens a database written by a pre-R1 binary and
// checks the migration's reading of the old boolean: relay=1 was an explicit
// switch (→ `on`), relay=0 was "nobody flipped it" (→ `auto`, so the idle rules
// may arm it without the human doing anything).
func TestRelayModeMigrationFromBool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	// The pre-R1 schema: a boolean relay column, no relay_mode / last_human_at.
	db, err := sql.Open("sqlite", path)
	assert.NoErr(t, err)
	_, err = db.Exec(`CREATE TABLE agent_sessions (
  session_id TEXT PRIMARY KEY, agent TEXT NOT NULL, project_key TEXT, runner TEXT, cwd TEXT,
  title TEXT, transcript TEXT, tmux_pane TEXT, state TEXT NOT NULL DEFAULT 'running',
  relay INTEGER NOT NULL DEFAULT 0, idle_sec INTEGER, turn_no INTEGER NOT NULL DEFAULT 0,
  last_message TEXT, last_event TEXT, last_seen_at INTEGER NOT NULL, started_at INTEGER NOT NULL,
  ended_at INTEGER)`)
	assert.NoErr(t, err)
	_, err = db.Exec(`INSERT INTO agent_sessions (session_id, agent, relay, last_seen_at, started_at)
  VALUES ('sid-switch','claude',1,1,1), ('sid-auto','codex',0,1,1)`)
	assert.NoErr(t, err)
	assert.NoErr(t, db.Close())

	st, err := jobstore.Open(path)
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	switched, ok, err := st.GetAgentSession("sid-switch")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.RelayModeOn, switched.RelayMode)
	assert.True(t, switched.Relay)
	assert.Eq(t, int64(0), switched.LastHumanAt)
	idle, ok, err := st.GetAgentSession("sid-auto")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.RelayModeAuto, idle.RelayMode)
	assert.False(t, idle.Relay)

	// Re-opening (the migration runs on every Open) changes nothing.
	st2, err := jobstore.Open(path)
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st2.Close() })
	again, _, err := st2.GetAgentSession("sid-switch")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.RelayModeOn, again.RelayMode)
}

// TestHumanPromptResetsOnToAuto pins the R1 demotion rule: a prompt a HUMAN
// typed hands an explicit switch back to `auto` (the idle rules take over), while
// the relay's own injected continuation leaves the switch alone — the human is on
// the web, not at the keyboard.
func TestHumanPromptResetsOnToAuto(t *testing.T) {
	s := newSvc(t)
	_, err := s.Register(RegisterInput{SessionID: "sid-r", Agent: "claude", Event: EventSessionStart})
	assert.NoErr(t, err)
	_, err = s.SetRelayMode("sid-r", jobstore.RelayModeOn)
	assert.NoErr(t, err)

	a, err := s.Heartbeat("sid-r", HeartbeatInput{Event: EventUserPromptSubmit, Injected: true})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.RelayModeOn, a.RelayMode, "an injected reply must not touch the switch")

	a, err = s.Heartbeat("sid-r", HeartbeatInput{Event: EventUserPromptSubmit, Title: "repo: task"})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.RelayModeAuto, a.RelayMode)

	// `off` is not resurrected by typing either: the human's explicit "never
	// wait" stays.
	_, err = s.SetRelayMode("sid-r", jobstore.RelayModeOff)
	assert.NoErr(t, err)
	a, err = s.Heartbeat("sid-r", HeartbeatInput{Event: EventUserPromptSubmit})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.RelayModeOff, a.RelayMode)

	// With the auto-demotion knob off an explicit switch survives a prompt too.
	_, err = s.SetRelayMode("sid-r", jobstore.RelayModeOn)
	assert.NoErr(t, err)
	s.AutoOffOnPrompt = false
	a, err = s.Heartbeat("sid-r", HeartbeatInput{Event: EventUserPromptSubmit})
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.RelayModeOn, a.RelayMode)
}

// TestLastHumanAtUpdatedOnlyByHumanPrompt pins the anchor of the turn-age
// fallback: it moves on a HUMAN prompt (and on registration) and on nothing else
// — a Stop heartbeat or an injected continuation must never fake a return.
func TestLastHumanAtUpdatedOnlyByHumanPrompt(t *testing.T) {
	s := newSvc(t)
	started, err := s.Register(RegisterInput{SessionID: "sid-h", Agent: "claude", Event: EventSessionStart})
	assert.NoErr(t, err)
	assert.True(t, started.LastHumanAt > 0, "SessionStart stamps last_human_at")

	// Wind the stamp back so "unchanged" is distinguishable from "stamped now".
	old := time.Now().Unix() - 7200
	_, err = s.store.UpsertAgentSession(jobstore.AgentSession{SessionID: "sid-h", LastHumanAt: old})
	assert.NoErr(t, err)

	for _, hb := range []HeartbeatInput{
		{Event: EventStop, LastMessage: "done"},
		{Event: EventNotification, LastMessage: "allow?"},
		{Event: EventUserPromptSubmit, Injected: true},
	} {
		got, err := s.Heartbeat("sid-h", hb)
		assert.NoErr(t, err)
		assert.Eq(t, old, got.LastHumanAt, hb.Event)
	}

	human, err := s.Heartbeat("sid-h", HeartbeatInput{Event: EventUserPromptSubmit, Title: "repo: task"})
	assert.NoErr(t, err)
	assert.True(t, human.LastHumanAt > old, "a human prompt stamps last_human_at")
	assert.True(t, human.LastHumanAt <= time.Now().Unix())

	// A registration that is NOT a SessionStart (a hook retrying mid-session)
	// keeps the stored evidence instead of clearing it.
	again, err := s.Register(RegisterInput{SessionID: "sid-h", Agent: "claude", Event: EventStop})
	assert.NoErr(t, err)
	assert.Eq(t, human.LastHumanAt, again.LastHumanAt)
}

// seedCallerJob inserts a job row submitted by caller, so the supervision window
// has something to count (the store is the relay's only view of jobs).
func seedCallerJob(t *testing.T, s *Service, id, caller, status string, startedAt int64) {
	t.Helper()
	assert.NoErr(t, s.store.UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "claude", Runner: "local", Status: status,
		ResultDir: "/tmp/" + id, StartedAt: startedAt, UpdatedAt: startedAt, CallerID: caller,
	}))
}

// finishJob moves a seeded job to a terminal state (the edge that must lift the
// supervision gate).
func finishJob(t *testing.T, s *Service, id, status string) {
	t.Helper()
	rec, ok, err := s.store.GetJob(id)
	if err != nil || !ok {
		t.Fatalf("seed job %s: ok=%v err=%v", id, ok, err)
	}
	rec.Status = status
	assert.NoErr(t, s.store.UpsertJob(rec))
}

// TestWaitReasonSkipsWhileSupervising pins D (bd h-aii-s2v4): a caller whose own
// jobs are still running IS supervising them, so their session must not auto-arm
// — otherwise the Stop hook blocks for hours and the job's completion notice never
// reaches them. The gate lifts by itself when the jobs end, and the reason is
// reported so the web can say WHY it is not arming.
func TestWaitReasonSkipsWhileSupervising(t *testing.T) {
	s := newSvc(t)
	s.AutoArmIdleSec, s.AutoArmTurnSec = 300, 900
	s.SkipWhenSupervising, s.SupervisingWindowSec = true, 7200
	now := time.Now()
	s.nowFn = func() time.Time { return now }
	a := jobstore.AgentSession{
		SessionID: "sid-sup", RelayMode: jobstore.RelayModeAuto, CallerID: "claude-sup", IdleSec: 600,
	}

	// Nothing of theirs is live: the ordinary idle rule arms the session.
	reason, detail := s.WaitDecision(a)
	assert.Eq(t, WaitIdleProbe, reason)
	assert.Eq(t, "", detail)

	seedCallerJob(t, s, "job-sup-1", "claude-sup", "running", now.Unix()-60)
	seedCallerJob(t, s, "job-sup-2", "claude-sup", "queued", now.Unix()-10)
	reason, detail = s.WaitDecision(a)
	assert.Eq(t, "", reason, "a supervising caller's Stop must not wait")
	assert.Eq(t, "supervising 2 jobs", detail)
	assert.False(t, s.AutoArmed(a))
	// WaitReason is the same verdict without the detail (the hook keys on it).
	assert.Eq(t, "", s.WaitReason(a))

	// Their jobs reached a terminal state: the idle rule is back, detail cleared.
	finishJob(t, s, "job-sup-1", "done")
	finishJob(t, s, "job-sup-2", "failed")
	reason, detail = s.WaitDecision(a)
	assert.Eq(t, WaitIdleProbe, reason)
	assert.Eq(t, "", detail)

	// A job that started before the window is over — the human is not supervising
	// it any more, so it must not keep the gate shut.
	seedCallerJob(t, s, "job-sup-old", "claude-sup", "running", now.Unix()-7201)
	reason, _ = s.WaitDecision(a)
	assert.Eq(t, WaitIdleProbe, reason)
}

// TestWaitReasonSupervisingIgnoresModeOn: the supervision gate only ever speaks
// for `auto`. An explicit `on` is the human saying "wait for me" — it wins.
func TestWaitReasonSupervisingIgnoresModeOn(t *testing.T) {
	s := newSvc(t)
	s.AutoArmIdleSec, s.SkipWhenSupervising, s.SupervisingWindowSec = 300, true, 7200
	now := time.Now()
	s.nowFn = func() time.Time { return now }
	seedCallerJob(t, s, "job-on", "claude-sup", "running", now.Unix()-5)

	a := jobstore.AgentSession{
		SessionID: "sid-on", RelayMode: jobstore.RelayModeOn, CallerID: "claude-sup", IdleSec: 600,
	}
	reason, detail := s.WaitDecision(a)
	assert.Eq(t, WaitModeOn, reason)
	assert.Eq(t, "", detail)
}

// TestWaitReasonSupervisingDisabledByConfig: the gate is opt-out —
// `session.auto_relay_skip_when_supervising: false` restores the old behaviour of
// arming whenever the keyboard is idle.
func TestWaitReasonSupervisingDisabledByConfig(t *testing.T) {
	s := newSvc(t)
	s.AutoArmIdleSec, s.SupervisingWindowSec = 300, 7200
	s.SkipWhenSupervising = false
	now := time.Now()
	s.nowFn = func() time.Time { return now }
	seedCallerJob(t, s, "job-cfg", "claude-sup", "running", now.Unix()-5)

	a := jobstore.AgentSession{
		SessionID: "sid-cfg", RelayMode: jobstore.RelayModeAuto, CallerID: "claude-sup", IdleSec: 600,
	}
	reason, detail := s.WaitDecision(a)
	assert.Eq(t, WaitIdleProbe, reason)
	assert.Eq(t, "", detail)
}

// TestWaitReasonSupervisingNeedsCallerID: without a caller the relay cannot know
// whose jobs those are, so the gate stays open — the pre-caller_id session (and
// the empty-token server) keeps its previous behaviour.
func TestWaitReasonSupervisingNeedsCallerID(t *testing.T) {
	s := newSvc(t)
	s.AutoArmIdleSec, s.AutoArmTurnSec = 300, 900
	s.SkipWhenSupervising, s.SupervisingWindowSec = true, 7200
	now := time.Now()
	s.nowFn = func() time.Time { return now }
	seedCallerJob(t, s, "job-anon", "claude-sup", "running", now.Unix()-5)

	a := jobstore.AgentSession{SessionID: "sid-anon", RelayMode: jobstore.RelayModeAuto, IdleSec: 600}
	reason, detail := s.WaitDecision(a)
	assert.Eq(t, WaitIdleProbe, reason)
	assert.Eq(t, "", detail)

	// And a job with no caller recorded counts for nobody.
	seedCallerJob(t, s, "job-nocall", "", "running", now.Unix()-5)
	a.CallerID = ""
	reason, _ = s.WaitDecision(a)
	assert.Eq(t, WaitIdleProbe, reason)
}
