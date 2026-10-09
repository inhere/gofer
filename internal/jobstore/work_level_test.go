package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	_ "modernc.org/sqlite"
)

func journalLevels(t *testing.T, s *Store, id string) map[string]string {
	t.Helper()
	j, err := s.ListWorkJournal(id, 0, 0)
	assert.NoErr(t, err)
	out := map[string]string{}
	for _, e := range j {
		out[e.Text] = e.Level
	}
	return out
}

// TestWorkJournalLevelMigration builds a pre-WORK-06 work_journal (no level column),
// re-opens it and checks the one-shot backfill: reports, non-system status lines and
// human notes become milestones, the rest stays detail.
func TestWorkJournalLevelMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE work_journal (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  work_item_id TEXT NOT NULL,
  kind         TEXT NOT NULL,
  text         TEXT NOT NULL DEFAULT '',
  by           TEXT NOT NULL DEFAULT '',
  at           INTEGER NOT NULL,
  origin_item  TEXT NOT NULL DEFAULT ''
)`)
	assert.NoErr(t, err)
	rows := [][3]string{
		{"report", "session:s1(claude)", "r1"},
		{"status", "human:alice", "s-human"},
		{"status", "system", "s-auto"},
		{"note", "human", "n-human"},
		{"note", "system", "n-system"},
		{"steward", "steward(claude)", "st"},
		{"link", "human:alice", "l"},
	}
	for _, r := range rows {
		_, err = raw.Exec(`INSERT INTO work_journal(work_item_id, kind, by, text, at) VALUES ('w-1', ?, ?, ?, 1)`, r[0], r[1], r[2])
		assert.NoErr(t, err)
	}
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.True(t, tableHasColumn(t, s, "work_journal", "level"))
	assert.True(t, indexExists(t, s, "idx_work_journal_level"))
	got := journalLevels(t, s, "w-1")
	want := map[string]string{
		"r1": WorkLevelMilestone, "s-human": WorkLevelMilestone, "n-human": WorkLevelMilestone,
		"s-auto": WorkLevelDetail, "n-system": WorkLevelDetail, "st": WorkLevelDetail, "l": WorkLevelDetail,
	}
	assert.Eq(t, want, got)

	// Idempotent: a second open keeps the levels (no re-backfill, no error).
	assert.NoErr(t, s.migrateWorkJournalLevel())
	assert.Eq(t, want, journalLevels(t, s, "w-1"))
}

func TestDefaultWorkJournalLevel(t *testing.T) {
	cases := []struct{ kind, by, want string }{
		{WorkJournalReport, "session:x", WorkLevelMilestone},
		{WorkJournalStatus, "human:a", WorkLevelMilestone},
		{WorkJournalStatus, "steward(claude)", WorkLevelMilestone},
		{WorkJournalStatus, "system", WorkLevelDetail},
		{WorkJournalStatus, "", WorkLevelDetail},
		{WorkJournalNote, "human", WorkLevelMilestone},
		{WorkJournalNote, "human:bob", WorkLevelMilestone},
		{WorkJournalNote, "steward(claude)", WorkLevelDetail},
		{WorkJournalNote, "system", WorkLevelDetail},
		{WorkJournalSteward, "steward(claude)", WorkLevelDetail},
		{WorkJournalLink, "human:a", WorkLevelDetail},
	}
	for _, c := range cases {
		assert.Eq(t, c.want, DefaultWorkJournalLevel(c.kind, c.by), c.kind+"/"+c.by)
	}
}

// TestWorkJournalLevelOnWrite covers the write-side classification of the store's own
// lines: status change by a person = milestone, field-only edit = detail, auto status
// = detail, link = detail; and the explicit level / ?level filter.
func TestWorkJournalLevelOnWrite(t *testing.T) {
	s := openTest(t)
	w, err := s.CreateWorkItem(WorkItemInput{Title: "t", By: "human:a", Note: "created"})
	assert.NoErr(t, err)
	title := "t2"
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Title: &title}, 0, "human:a")
	assert.NoErr(t, err)
	st := WorkNeedsMe
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &st}, 0, "human:a")
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(w.ID, WorkLinkJob, "job-1", "human:a")
	assert.NoErr(t, err)
	_, err = s.AppendWorkJournalLevel(w.ID, WorkJournalSteward, "worth noting", "steward(claude)", WorkLevelMilestone)
	assert.NoErr(t, err)
	_, err = s.AppendWorkJournalLevel(w.ID, WorkJournalSteward, "bad", "steward(claude)", "loud")
	assert.Err(t, err)

	j, err := s.ListWorkJournal(w.ID, 0, 0)
	assert.NoErr(t, err)
	levels := map[string]string{}
	for _, e := range j {
		levels[e.Text] = e.Level
	}
	assert.Eq(t, WorkLevelMilestone, levels["created"])
	assert.Eq(t, WorkLevelDetail, levels["标题：t → t2"])
	assert.Eq(t, WorkLevelMilestone, levels["状态：active → needs_me"])
	assert.Eq(t, WorkLevelDetail, levels["关联 job job-1"])
	assert.Eq(t, WorkLevelMilestone, levels["worth noting"])

	ms, err := s.ListWorkJournalLevel(w.ID, 0, 0, WorkLevelMilestone)
	assert.NoErr(t, err)
	assert.Len(t, ms, 3)
	_, err = s.ListWorkJournalLevel(w.ID, 0, 0, "nope")
	assert.Err(t, err)

	// Auto-derived status is a detail.
	src := WorkSourceAuto
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{StatusSource: &src}, 0, "human:a")
	assert.NoErr(t, err)
	changed, err := s.SetWorkItemAutoStatus(w.ID, WorkActive, "会话运行中")
	assert.NoErr(t, err)
	assert.True(t, changed)
	last, err := s.ListWorkJournal(w.ID, 1, 0)
	assert.NoErr(t, err)
	assert.Eq(t, WorkLevelDetail, last[0].Level)
}

// TestWorkAnsweredMilestones: an answered plan decision / job interaction lands as a
// milestone on the open work items its plan / job belongs to; a relay turn does not.
func TestWorkAnsweredMilestones(t *testing.T) {
	s := openTest(t)
	w, err := s.CreateWorkItem(WorkItemInput{Title: "t", By: "human:a"})
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(w.ID, WorkLinkPlan, "plan-abc12345", "human:a")
	assert.NoErr(t, err)

	d := &PlanDecision{PlanID: "plan-abc12345", Title: "pick", Question: "A or B?"}
	assert.NoErr(t, s.InsertDecision(d))
	ok, err := s.AnswerDecision(d.ID, "A", "alice")
	assert.NoErr(t, err)
	assert.True(t, ok)

	relay := &PlanDecision{PlanID: "plan-abc12345", Title: "turn", Question: "q", Kind: DecisionKindRelay}
	assert.NoErr(t, s.InsertDecision(relay))
	_, err = s.AnswerDecision(relay.ID, "hi", "alice")
	assert.NoErr(t, err)

	job := sampleJob("job-i1", "p", 1)
	job.PlanID = "plan-abc12345"
	assert.NoErr(t, s.UpsertJob(job))
	rec := InteractionRecord{ID: "it-1", JobID: "job-i1", Type: "permission", Prompt: "run tests?", Status: "pending", CreatedAt: 1}
	assert.NoErr(t, s.UpsertInteraction(rec))
	rec.Status, rec.Answer, rec.AnsweredBy, rec.AnsweredAt = "answered", "allow", "human", 2
	assert.NoErr(t, s.UpsertInteraction(rec))
	// Re-writing the answered snapshot does not journal twice.
	assert.NoErr(t, s.UpsertInteraction(rec))

	ms, err := s.ListWorkJournalLevel(w.ID, 0, 0, WorkLevelMilestone)
	assert.NoErr(t, err)
	var texts []string
	for _, e := range ms {
		texts = append(texts, e.Text)
	}
	assert.Contains(t, texts, "已回答 decision「pick」：A")
	assert.Contains(t, texts, "已应答 job job-i1 的交互「run tests?」：allow")
	for _, e := range ms {
		assert.NotContains(t, e.Text, "turn")
		if e.Text == "已回答 decision「pick」：A" {
			assert.Eq(t, "human:alice", e.By)
		}
	}
	assert.Len(t, ms, 3) // creation + decision + interaction
}

func TestOpenWorkItemsFor(t *testing.T) {
	s := openTest(t)
	a, err := s.CreateWorkItem(WorkItemInput{Title: "a", By: "human", SessionIDs: []string{"sess-1"}})
	assert.NoErr(t, err)
	b, err := s.CreateWorkItem(WorkItemInput{Title: "b", By: "human"})
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(b.ID, WorkLinkJob, "job-x", "human")
	assert.NoErr(t, err)
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "sess-1", Agent: "claude"})
	assert.NoErr(t, err)
	_, err = s.AddSessionJobWatch("sess-1", "job-x")
	assert.NoErr(t, err)
	ids, err := s.OpenWorkItemsFor(WorkRefs{JobID: "job-x"})
	assert.NoErr(t, err)
	assert.Len(t, ids, 2)
	ids, err = s.OpenWorkItemsFor(WorkRefs{SessionID: "sess-1"})
	assert.NoErr(t, err)
	assert.Eq(t, []string{a.ID}, ids)
	done := WorkDone
	_, _, err = s.UpdateWorkItem(b.ID, WorkItemPatch{Status: &done}, 0, "human")
	assert.NoErr(t, err)
	ids, err = s.OpenWorkItemsFor(WorkRefs{JobID: "job-x"})
	assert.NoErr(t, err)
	assert.Eq(t, []string{a.ID}, ids)
	ids, err = s.OpenWorkItemsFor(WorkRefs{})
	assert.NoErr(t, err)
	assert.Len(t, ids, 0)
}
