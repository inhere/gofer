package inbox

import (
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
	"github.com/inhere/gofer/internal/waitgate"
	"github.com/inhere/gofer/internal/work"
	"github.com/inhere/gofer/internal/workbench"
)

// seedSurfaces stores, in one database, the waiting and not-yet-waiting variants of both
// session kinds: a terminal session turn and a managed ACP session turn, each marked and
// past the grace period, marked and inside it, and (ACP) unmarked.
func seedSurfaces(t *testing.T, st *jobstore.Store, now int64) {
	t.Helper()
	for _, c := range []struct {
		sid    string
		ago    int64
		marked bool
	}{
		{"t-old", waitgate.GraceSec + 60, true},
		{"t-young", 30, true},
		{"t-plain", waitgate.GraceSec + 60, false},
	} {
		if _, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: c.sid, Agent: "claude", ProjectKey: "p1", Title: c.sid}); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertDecision(&jobstore.PlanDecision{ID: "d-" + c.sid, Title: "turn", Question: "q", Kind: jobstore.DecisionKindRelay,
			SessionID: c.sid, AskedAt: now - c.ago, TimeoutSec: 86400, NeedsReply: c.marked}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		id     string
		ago    int64
		marked bool
	}{
		{"a-old", waitgate.GraceSec + 60, true},
		{"a-young", 30, true},
		{"a-plain", waitgate.GraceSec + 60, false},
	} {
		if err := st.UpsertJob(jobstore.JobRecord{ID: c.id, ProjectKey: "p1", Agent: "claude-acp", SessionID: "sid-" + c.id, Status: "awaiting_input",
			SessionStateJSON: `{"session":true,"turn_no":1}`, RequestJSON: `{"title":"` + c.id + `"}`, StartedAt: now - 3600, UpdatedAt: now - c.ago}); err != nil {
			t.Fatal(err)
		}
		detail := `{"turn_no":1}`
		if c.marked {
			detail = `{"turn_no":1,"needs_reply":true}`
		}
		if _, err := st.InsertJobEvent(jobstore.JobEvent{JobID: c.id, Type: "job.awaiting_input", Detail: detail, At: now - c.ago}); err != nil {
			t.Fatal(err)
		}
	}
}

// waitingRefs is the set of session / job refs on the 「等我」 list.
func waitingRefs(t *testing.T, st *jobstore.Store) []string {
	t.Helper()
	ws := work.New(st)
	t.Cleanup(func() { _ = ws.Close() })
	cards, err := today.New(today.Deps{Store: st, Work: ws}).Decisions(false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range SubjectsOf("en", cards) {
		if s.Category != CatAnswer {
			t.Errorf("subject %s files under %s, want answer", s.Key, s.Category)
		}
		out = append(out, s.Key)
	}
	sort.Strings(out)
	return out
}

// attentionRefs is the same set as the workbench's attention items name it.
func attentionRefs(t *testing.T, st *jobstore.Store) []string {
	t.Helper()
	resp, err := workbench.NewService(st, nil, nil).List("c1", workbench.Query{Since: 1})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range resp.Attention {
		switch {
		case a.SessionID != "":
			out = append(out, SubjectSession+":"+a.SessionID)
		case a.JobID != "":
			out = append(out, SubjectJob+":"+a.JobID)
		}
	}
	sort.Strings(out)
	return out
}

// The inbox / today judgement and the workbench attention agree on the same rows, for a
// terminal turn and for a managed ACP turn alike (including the grace period), and the
// agreement holds for a fresh process over the same database.
func TestWorkbenchAttentionMatchesTheWaitingList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	st, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	seedSurfaces(t, st, now)
	want := []string{"job:a-old", "session:t-old"}
	check := func(st *jobstore.Store, when string) {
		t.Helper()
		if got := waitingRefs(t, st); !equal(got, want) {
			t.Errorf("%s: waiting list = %v, want %v", when, got, want)
		}
		if got := attentionRefs(t, st); !equal(got, want) {
			t.Errorf("%s: workbench attention = %v, want %v", when, got, want)
		}
	}
	check(st, "first open")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	check(st2, "after reopen")
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
