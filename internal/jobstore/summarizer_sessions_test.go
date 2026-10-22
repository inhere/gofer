package jobstore

import (
	"path/filepath"
	"testing"
	"time"
)

// The work summarizer's sessions are internal (gofer-ayu0): a session list hides them
// unless it asks for internal rows, and that must also cover the sessions the older,
// unpinned runs left behind (registered from inside the job under an id gofer never saw).
func TestListAgentSessionsHidesSummarizerSessions(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	// The store stamps started_at itself: fix it inside the summarizer job's window so the
	// legacy (unpinned) session is recognisable.
	st.SetClock(func() time.Time { return time.Unix(2000, 0) })

	// A pinned run: the summarizer job carries the session id.
	if _, err := st.UpsertAgentSession(AgentSession{SessionID: "sess-pinned", Agent: "claude", ProjectKey: "p1",
		Cwd: "/ws", State: SessionEnded, StartedAt: 1000}); err != nil {
		t.Fatalf("upsert pinned: %v", err)
	}
	// A legacy run: the agent registered its own (empty) session while the job ran.
	if _, err := st.UpsertAgentSession(AgentSession{SessionID: "sess-legacy", Agent: "suga", ProjectKey: "p1",
		Cwd: "/ws", State: SessionEnded, StartedAt: 2000}); err != nil {
		t.Fatalf("upsert legacy: %v", err)
	}
	// A person's session in the same project, also without a last message: it has a title,
	// so it stays visible.
	if _, err := st.UpsertAgentSession(AgentSession{SessionID: "sess-human", Agent: "suga", ProjectKey: "p1",
		Cwd: "/ws", State: SessionIdle, Title: "suga: 了解项目", StartedAt: 9000}); err != nil {
		t.Fatalf("upsert human: %v", err)
	}
	for _, rec := range []JobRecord{
		{ID: "job-sum-pinned", ProjectKey: "p1", Agent: "claude", Runner: "local", Status: "done",
			SessionID: "sess-pinned", TagsJSON: `["work-summarizer"]`, StartedAt: 990, EndedAt: 1010},
		{ID: "job-sum-legacy", ProjectKey: "p1", Agent: "suga", Runner: "local", Status: "done",
			TagsJSON: `["work-summarizer"]`, StartedAt: 1995, EndedAt: 2030},
		// An ordinary job next to the person's session: nothing to hide there.
		{ID: "job-user", ProjectKey: "p1", Agent: "suga", Runner: "local", Status: "done",
			TagsJSON: `["feature"]`, StartedAt: 8000, EndedAt: 9500},
	} {
		if err := st.UpsertJob(rec); err != nil {
			t.Fatalf("upsert job %s: %v", rec.ID, err)
		}
	}

	list, err := st.ListAgentSessions(ListSessionsOpts{IncludeEnded: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].SessionID != "sess-human" {
		t.Fatalf("default list = %+v, want only sess-human", list)
	}
	all, err := st.ListAgentSessions(ListSessionsOpts{IncludeEnded: true, IncludeInternal: true})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("IncludeInternal list = %d rows, want 3", len(all))
	}
}

func TestSummarizerSessionRecordRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if _, ok, err := st.GetSummarizerSession("p1"); err != nil || ok {
		t.Fatalf("empty record = %v/%v, want missing", ok, err)
	}
	rec := SummarizerSession{ProjectKey: "p1", SessionID: "sess-1", LastJobID: "job-1", Runs: 3,
		CreatedAt: 100, UpdatedAt: 200}
	if err := st.SaveSummarizerSession(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	rec.Runs = 4
	if err := st.SaveSummarizerSession(rec); err != nil {
		t.Fatalf("save again: %v", err)
	}
	got, ok, err := st.GetSummarizerSession("p1")
	if err != nil || !ok {
		t.Fatalf("get = %v/%v", ok, err)
	}
	got.UpdatedAt = 0
	if got != (SummarizerSession{ProjectKey: "p1", SessionID: "sess-1", LastJobID: "job-1", Runs: 4, CreatedAt: 100}) {
		t.Fatalf("record = %+v", got)
	}
	ids, err := st.ListSummarizerSessionIDs()
	if err != nil || len(ids) != 1 || ids[0] != "sess-1" {
		t.Fatalf("ids = %v/%v", ids, err)
	}
	if err := st.DeleteSummarizerSession("p1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := st.GetSummarizerSession("p1"); ok {
		t.Fatal("record survived the delete")
	}
}
