package jobstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestBackgroundAgentsUpgradeFromPreviousRelease opens a database built from the last
// released schema (which has the background tables WITHOUT the runner's columns and a
// summarizer_sessions book) and requires the additive migration to add the columns, import
// the summarizer's book once, and keep the older background rows readable.
func TestBackgroundAgentsUpgradeFromPreviousRelease(t *testing.T) {
	ddl, err := os.ReadFile(filepath.Join("testdata", "schema-v0.205.1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gofer.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO summarizer_sessions (project_key, session_id, last_job_id, runs, created_at, updated_at) VALUES ('p1','s-old','job-old',7,100,200)`,
		`INSERT INTO summarizer_sessions (project_key, session_id, last_job_id, runs, created_at, updated_at) VALUES ('p2','','job-p2',2,150,250)`,
		`INSERT INTO background_agent_sessions (kind, session_id, agent, project, work_dir, model, created_at, last_used_at, runs, state, manual_reset)
		   VALUES ('delivery_check','chk-1','claude','p1','/w','',10,20,3,'active',0)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open the previous release's database: %v", err)
	}
	for table, cols := range map[string][]string{
		"background_agent_sessions": {"last_job_id", "rotation_reason"},
		"background_agent_runs":     {"project", "mode", "rotation_reason"},
	} {
		for _, c := range cols {
			if !tableHasColumn(t, s, table, c) {
				t.Fatalf("%s lacks the migrated column %s", table, c)
			}
		}
	}
	// the older rows read back with the new columns defaulted
	b, ok, err := s.ActiveBackgroundSession("delivery_check", "p1", "/w", "claude", "")
	if err != nil || !ok || b.SessionID != "chk-1" || b.LastJobID != "" || b.Runs != 3 {
		t.Fatalf("older background row = %+v ok=%v err=%v", b, ok, err)
	}
	// the summarizer's book was imported: bound to no agent yet, so the runner can adopt it
	u, ok, err := s.ActiveUnboundBackgroundSession(BackgroundKindSummarizer, "p1", "")
	if err != nil || !ok || u.SessionID != "s-old" || u.LastJobID != "job-old" || u.Runs != 7 || u.CreatedAt != 100 {
		t.Fatalf("imported p1 = %+v ok=%v err=%v", u, ok, err)
	}
	got, ok, err := s.GetSummarizerSession("p2")
	if err != nil || !ok || got.SessionID != "" || got.LastJobID != "job-p2" || got.Runs != 2 {
		t.Fatalf("a book row without a session id keeps working: %+v ok=%v err=%v", got, ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// the import is one-time: a second open does not duplicate or resurrect rows
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if err := s.DeleteSummarizerSession("p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok, _ := s.GetSummarizerSession("p1"); ok {
		t.Fatal("the book was imported again on a later open")
	}
}

func TestReplaceBackgroundSessionRetiresTheScope(t *testing.T) {
	s := openTest(t)
	a := BackgroundSession{Kind: "steward", SessionID: "a", Agent: "x", Project: "p", CreatedAt: 1, LastUsedAt: 1, Runs: 1, LastJobID: "j1"}
	if err := s.ReplaceBackgroundSession(a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.SessionID, b.Agent, b.LastJobID, b.LastUsedAt, b.RotationReason = "b", "y", "j2", 2, "max_runs"
	if err := s.ReplaceBackgroundSession(b); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListBackgroundSessions("steward")
	if err != nil || len(list) != 1 || list[0].SessionID != "b" || list[0].RotationReason != "max_runs" || list[0].LastJobID != "j2" {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if _, ok, _ := s.ActiveBackgroundSession("steward", "p", "", "x", ""); ok {
		t.Fatal("the replaced row is still active")
	}
	// ends of the scope do not leak into another kind or project
	other := BackgroundSession{Kind: "job_check", SessionID: "c", Project: "p", CreatedAt: 1, LastUsedAt: 1}
	if err := s.ReplaceBackgroundSession(other); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListBackgroundSessions("steward"); len(list) != 1 {
		t.Fatalf("another kind retired a steward session: %+v", list)
	}
	// unknown context stays NULL -> 0 with an unknown limit, a known one round-trips
	b.SessionID, b.ContextUsed, b.ContextLimit = "d", 150, 200
	if err := s.ReplaceBackgroundSession(b); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.ActiveBackgroundSession("steward", "p", "", "y", "")
	if got.ContextRatio() != 0.75 {
		t.Fatalf("ratio = %v", got.ContextRatio())
	}
	if err := s.RetireBackgroundSession("steward", "d", "dropped"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ActiveBackgroundSession("steward", "p", "", "y", ""); ok {
		t.Fatal("a dropped session is still active")
	}
	if last, ok, _ := s.LatestBackgroundSession("steward", "p", "", "y", ""); !ok || last.State != "dropped" {
		t.Fatalf("latest = %+v ok=%v", last, ok)
	}
}

func TestBackgroundRunLogAndStats(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertBackgroundRun(BackgroundRun{}); err == nil {
		t.Fatal("a run without a kind and status was accepted")
	}
	mustRun := func(r BackgroundRun) {
		t.Helper()
		if _, err := s.InsertBackgroundRun(r); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(BackgroundRun{Kind: "summarizer", JobID: "j1", StartedAt: 100, Status: BackgroundRunOK, UsageJSON: `{"input_tokens":10,"output_tokens":5,"total_tokens":15,"cost_usd":0.5}`})
	mustRun(BackgroundRun{Kind: "summarizer", JobID: "j2", StartedAt: 200, Status: BackgroundRunOK}) // no usage reported
	mustRun(BackgroundRun{Kind: "summarizer", StartedAt: 300, Status: BackgroundRunBlocked, Error: "daily_limit"})
	mustRun(BackgroundRun{Kind: "summarizer", StartedAt: 50, Status: BackgroundRunOK, UsageJSON: `{"total_tokens":999}`}) // yesterday
	mustRun(BackgroundRun{Kind: "steward", StartedAt: 100, Status: BackgroundRunOK, UsageJSON: `{"total_tokens":1}`})
	st, err := s.BackgroundStatsSince("summarizer", 100)
	if err != nil {
		t.Fatal(err)
	}
	if st.Runs != 2 || st.Blocked != 1 || st.UsageRuns != 1 || st.TotalTokens != 15 || st.InputTokens != 10 || st.CostUSD != 0.5 ||
		st.LastStatus != BackgroundRunBlocked || st.LastError != "daily_limit" {
		t.Fatalf("stats = %+v", st)
	}
	// a late usage only fills a run that has none
	if ok, _ := s.SetBackgroundRunUsage("summarizer", "j1", `{"total_tokens":1}`); ok {
		t.Fatal("overwrote a recorded usage")
	}
	if ok, err := s.SetBackgroundRunUsage("summarizer", "j2", `{"total_tokens":40,"cost_usd":0.25}`); err != nil || !ok {
		t.Fatalf("late usage = %v %v", ok, err)
	}
	st, _ = s.BackgroundStatsSince("summarizer", 100)
	if st.UsageRuns != 2 || st.TotalTokens != 55 || st.CostUSD != 0.75 {
		t.Fatalf("stats after late usage = %+v", st)
	}
}

func TestSummarizerSessionViewKeepsTheBoundAgent(t *testing.T) {
	s := openTest(t)
	if err := s.ReplaceBackgroundSession(BackgroundSession{Kind: BackgroundKindSummarizer, SessionID: "s1", Agent: "claude",
		Project: "p1", Model: "haiku", CreatedAt: 1, LastUsedAt: 2, Runs: 1, LastJobID: "j1"}); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.GetSummarizerSession("p1")
	if !ok || got.SessionID != "s1" || got.LastJobID != "j1" || got.Runs != 1 {
		t.Fatalf("view = %+v", got)
	}
	got.Runs = 5
	if err := s.SaveSummarizerSession(got); err != nil {
		t.Fatal(err)
	}
	b, ok, _ := s.ActiveBackgroundSession(BackgroundKindSummarizer, "p1", "", "claude", "haiku")
	if !ok || b.Runs != 5 {
		t.Fatalf("a legacy save must update the bound row, not unbind it: %+v ok=%v", b, ok)
	}
}
