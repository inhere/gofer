package httpapi

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/waitnotice"
)

// recPush is a WebPushService that records the 「等我」 notices pushed to it.
type recPush struct {
	WebPushService
	mu      sync.Mutex
	notices []waitnotice.Notice
}

func (r *recPush) NotifyWaiting(n waitnotice.Notice) {
	r.mu.Lock()
	r.notices = append(r.notices, n)
	r.mu.Unlock()
}

func (r *recPush) got() []waitnotice.Notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]waitnotice.Notice(nil), r.notices...)
}

// turnText stands for the text of a turn: it must reach no channel.
const turnText = "TURN-TEXT-MUST-NOT-LEAVE-12345"

func newWaitNoticeServer(t *testing.T) (*Server, *recPush) {
	t.Helper()
	s := recordingServerWith(t, config.ServerConfig{
		WebBaseURL: "https://gofer.example.com",
		Notification: &config.NotificationConfig{
			Webhooks:   []config.WebhookConfig{{URL: "https://hooks.example.com/w", Events: []string{"session.awaiting_reply"}}},
			AllowHosts: []string{"hooks.example.com"},
		},
	}, func(cfg *config.Config) {
		cfg.Projects["p1"] = config.ProjectConfig{HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true}
	})
	// the first scan on a database is silent; these tests are about the scans after it
	if err := s.jobs.Meta().MarkWaitingNoticesInitialized(time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	push := &recPush{}
	s.SetWebPush(push)
	return s, push
}

// seedWaitingTurns seeds a terminal session whose turn asks for a reply and a managed ACP
// session job that does, both past the grace period, plus a terminal turn still inside it
// and an unmarked ACP turn that must not be told about.
func seedWaitingTurns(t *testing.T, s *Server) {
	t.Helper()
	st := s.jobs.Meta()
	now := time.Now().Unix()
	for _, sid := range []string{"sess-p", "sess-fresh"} {
		if _, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p1", Runner: "local",
			Cwd: s.projects.Config().Storage.Root, Title: "周报会话 " + sid, CallerID: "plain", State: jobstore.SessionRunning, StartedAt: now, LastSeenAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for sid, ago := range map[string]int64{"sess-p": 3600, "sess-fresh": 10} {
		if err := st.InsertDecision(&jobstore.PlanDecision{ID: "dec-" + sid, Title: "turn", Question: turnText, Kind: jobstore.DecisionKindRelay,
			SessionID: sid, AskedAt: now - ago, TimeoutSec: 86400, NeedsReply: true}); err != nil {
			t.Fatal(err)
		}
	}
	for id, marked := range map[string]bool{"acp-marked": true, "acp-plain": false} {
		if err := st.UpsertJob(jobstore.JobRecord{ID: id, ProjectKey: "p1", Agent: "claude-acp", Runner: "local", Status: "awaiting_input", Cwd: ".",
			RequestJSON: `{"title":"ACP 助手"}`, SessionStateJSON: `{"turn_no":1}`, CallerID: "acp-owner", StartedAt: now - 7200, UpdatedAt: now - 3600}); err != nil {
			t.Fatal(err)
		}
		detail := `{"turn_no":1}`
		if marked {
			detail = `{"turn_no":1,"needs_reply":true}`
		}
		if _, err := st.InsertJobEvent(jobstore.JobEvent{JobID: id, Type: "job.awaiting_input", Detail: detail, At: now - 3600}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWaitingNoticeScanTellsEachSubjectOnce(t *testing.T) {
	s, push := newWaitNoticeServer(t)
	seedWaitingTurns(t, s)
	for i := 0; i < 3; i++ {
		s.sweepWaitingNotices()
	}
	got := push.got()
	keys := map[string]waitnotice.Notice{}
	for _, n := range got {
		keys[n.Key] = n
	}
	if len(got) != 2 || keys["session:sess-p"].Key == "" || keys["job:acp-marked"].Key == "" {
		t.Fatalf("want one notice for the late terminal turn and one for the marked ACP job, got %+v", got)
	}
	if n := keys["session:sess-p"]; n.Title != "周报会话 sess-p" || n.ThreadID != "r:sess-p" || n.CallerID != "plain" || n.Category != "answer" || n.Reminder {
		t.Fatalf("terminal notice = %+v", n)
	}
	if n := keys["job:acp-marked"]; n.Title != "ACP 助手" || n.ThreadID == "" || n.CallerID != "acp-owner" {
		t.Fatalf("ACP notice = %+v", n)
	}
}

// A seat's name is what the person knows the session by.
func TestWaitingNoticeNamesTheSeat(t *testing.T) {
	s, push := newWaitNoticeServer(t)
	seedWaitingTurns(t, s)
	x, err := s.jobs.Meta().CreateSeat(jobstore.Seat{CallerID: "plain", ProjectKey: "p1", Name: "评审席", Kind: jobstore.SeatKindTerminal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.jobs.Meta().BindSeat(x.ID, "sess-p", false, "user"); err != nil {
		t.Fatal(err)
	}
	s.sweepWaitingNotices()
	for _, n := range push.got() {
		if n.Key == "session:sess-p" && n.Title != "评审席" {
			t.Fatalf("title = %q, want the seat name", n.Title)
		}
	}
}

// The notice names who waits for what; the text of the turn reaches no channel: not the
// webhook delivery, not the push notice.
func TestWaitingNoticeCarriesNoTurnText(t *testing.T) {
	s, push := newWaitNoticeServer(t)
	seedWaitingTurns(t, s)
	s.sweepWaitingNotices()
	if len(push.got()) == 0 {
		t.Fatal("nothing was pushed")
	}
	due, err := s.jobs.Meta().ClaimDueDeliveries(time.Now().Unix()+60, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("webhook deliveries = %d, want 2", len(due))
	}
	for _, d := range due {
		if strings.Contains(d.Body, turnText) || !strings.Contains(d.Body, "在等你回答") {
			t.Fatalf("delivery body = %s", d.Body)
		}
	}
	for _, n := range push.got() {
		if strings.Contains(waitnotice.Title("zh-CN", n), turnText) {
			t.Fatalf("title carries the turn text: %+v", n)
		}
	}
}

// Answering ends the stay: nothing is told again, and a later turn is a new entry.
func TestWaitingNoticeLeaveAndReenter(t *testing.T) {
	s, push := newWaitNoticeServer(t)
	seedWaitingTurns(t, s)
	s.sweepWaitingNotices()
	if _, err := s.jobs.Meta().ExpireSessionDecisions("sess-p"); err != nil {
		t.Fatal(err)
	}
	s.sweepWaitingNotices()
	if open, _ := s.jobs.Meta().ListOpenWaitingNotices(); len(open) != 1 || open[0].SubjectKey != "job:acp-marked" {
		t.Fatalf("open rows = %+v, want only the ACP job", open)
	}
	if err := s.jobs.Meta().InsertDecision(&jobstore.PlanDecision{ID: "dec-again", Title: "turn", Question: turnText, Kind: jobstore.DecisionKindRelay,
		SessionID: "sess-p", AskedAt: time.Now().Unix() - 3600, TimeoutSec: 86400, NeedsReply: true}); err != nil {
		t.Fatal(err)
	}
	s.sweepWaitingNotices()
	n := 0
	for _, g := range push.got() {
		if g.Key == "session:sess-p" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("the session was told %d times, want 2 (entry, and again after it left)", n)
	}
}

// A turn is not 「等我」 while its owner has jobs running (they supervise the session): the
// notice is sent when the supervision ends and the turn is still unanswered.
func TestWaitingNoticeSupervisionEndsThenNotifies(t *testing.T) {
	s, push := newWaitNoticeServer(t)
	seedWaitingTurns(t, s)
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "watched", ProjectKey: "p1", Agent: "exec", Runner: "local", Status: "running", Cwd: ".",
		CallerID: "plain", SourceSessionID: "sess-p", StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	s.sweepWaitingNotices()
	for _, n := range push.got() {
		if n.Key == "session:sess-p" {
			t.Fatalf("a supervised session was told about: %+v", n)
		}
	}
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "watched", ProjectKey: "p1", Agent: "exec", Runner: "local", Status: "done", Cwd: ".",
		CallerID: "plain", SourceSessionID: "sess-p", StartedAt: now, UpdatedAt: now, EndedAt: now}); err != nil {
		t.Fatal(err)
	}
	s.sweepWaitingNotices()
	s.sweepWaitingNotices()
	n := 0
	for _, g := range push.got() {
		if g.Key == "session:sess-p" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("told %d times after the supervision ended, want exactly 1", n)
	}
}
