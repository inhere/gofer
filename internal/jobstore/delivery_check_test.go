package jobstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestDeliveryCheckSchemaUpgrade opens a database from a release that predates the
// delivery-check columns and tables and requires the additive migration to add them.
func TestDeliveryCheckSchemaUpgrade(t *testing.T) {
	ddl, err := os.ReadFile(filepath.Join("testdata", "schema-v0.203.0.sql"))
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
	_ = raw.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open old schema: %v", err)
	}
	defer s.Close()
	jobCols, err := s.tableColumns("jobs")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"check_json", "check_resume_count", "check_error"} {
		if _, ok := jobCols[c]; !ok {
			t.Errorf("jobs.%s missing after upgrade", c)
		}
	}
	todoCols, err := s.tableColumns("plan_todos")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"check_prompt", "check_prompt_source"} {
		if _, ok := todoCols[c]; !ok {
			t.Errorf("plan_todos.%s missing after upgrade", c)
		}
	}
	for _, tbl := range []string{"background_agent_sessions", "background_agent_runs"} {
		cols, err := s.tableColumns(tbl)
		if err != nil || len(cols) == 0 {
			t.Errorf("table %s missing after upgrade: %v", tbl, err)
		}
	}
}

func TestJobCheckRoundTrip(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertJob(sampleJob("job-chk", "proj", 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetJobCheck("job-chk", `{"verdict":"fail"}`, 2, "boom"); err != nil {
		t.Fatal(err)
	}
	js, n, e, err := s.JobCheck("job-chk")
	if err != nil || js != `{"verdict":"fail"}` || n != 2 || e != "boom" {
		t.Fatalf("JobCheck = %q %d %q %v", js, n, e, err)
	}
	if err := s.SetJobCheck("absent", "", 0, ""); err == nil {
		t.Fatal("SetJobCheck accepted an unknown job")
	}
}

func TestBackgroundSessionKeyIsolationAndReset(t *testing.T) {
	s := openTest(t)
	base := BackgroundSession{Kind: "delivery_check", SessionID: "s1", Agent: "claude", Project: "proj-a", WorkDir: "/w/a",
		CreatedAt: 10, LastUsedAt: 10, Runs: 1, ContextUsed: 350, ContextLimit: 1000}
	if err := s.UpsertBackgroundSession(base); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.ActiveBackgroundSession("delivery_check", "proj-a", "/w/a", "claude", "")
	if err != nil || !ok || got.SessionID != "s1" || got.ContextRatio() != 0.35 || got.State != "active" {
		t.Fatalf("active = %+v ok=%v err=%v", got, ok, err)
	}
	for _, miss := range [][3]string{{"steward", "proj-a", "/w/a"}, {"delivery_check", "proj-b", "/w/a"}, {"delivery_check", "proj-a", "/w/b"}} {
		if _, ok, _ := s.ActiveBackgroundSession(miss[0], miss[1], miss[2], "claude", ""); ok {
			t.Fatalf("key %v leaked another session", miss)
		}
	}
	// an update keeps created_at and replaces runs
	upd := base
	upd.CreatedAt, upd.Runs, upd.LastUsedAt = 99, 2, 20
	if err := s.UpsertBackgroundSession(upd); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.ActiveBackgroundSession("delivery_check", "proj-a", "/w/a", "claude", "")
	if got.Runs != 2 || got.CreatedAt != 10 {
		t.Fatalf("after update = %+v", got)
	}
	n, err := s.ResetBackgroundSessions("delivery_check", "proj-a", "/w/a")
	if err != nil || n != 1 {
		t.Fatalf("reset = %d %v", n, err)
	}
	if _, ok, _ := s.ActiveBackgroundSession("delivery_check", "proj-a", "/w/a", "claude", ""); ok {
		t.Fatal("reset session still active")
	}
	if err := s.UpsertBackgroundSession(BackgroundSession{}); err == nil {
		t.Fatal("accepted a session without a key")
	}
}

func TestTodoCheckPromptSourceRoundTrip(t *testing.T) {
	s := openTest(t)
	if err := s.InsertPlan(Plan{PlanID: "plan-chk", Title: "p", Status: PlanOpen}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertTodo(PlanTodo{TodoID: "todo-chk", PlanID: "plan-chk", Title: "t", CheckPrompt: "req", CheckPromptSource: "prompt_block"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetTodo("todo-chk")
	if err != nil || !ok || got.CheckPrompt != "req" || got.CheckPromptSource != "prompt_block" {
		t.Fatalf("todo = %+v ok=%v err=%v", got, ok, err)
	}
	src := "explicit"
	if _, err := s.UpdateTodoPatch("todo-chk", TodoPatch{CheckPromptSource: &src}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetTodo("todo-chk")
	if got.CheckPromptSource != "explicit" || got.CheckPrompt != "req" {
		t.Fatalf("patched todo = %+v", got)
	}
}
