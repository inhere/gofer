package today

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/waitgate"
	"github.com/inhere/gofer/internal/work"
)

// openACPStore opens (or reopens) the store at path with the test clock.
func openACPStore(t *testing.T, path string) (*Service, *jobstore.Store) {
	t.Helper()
	st, err := jobstore.Open(path)
	assert.NoErr(t, err)
	st.SetClock(func() time.Time { return testNow })
	ws := work.New(st)
	ws.SetNow(func() time.Time { return testNow })
	svc := New(Deps{Store: st, Work: ws, Now: func() time.Time { return testNow }})
	// Registered after TempDir, so it runs before the directory goes (gofer-r7am).
	t.Cleanup(func() { _ = ws.Close(); _ = st.Close() })
	return svc, st
}

// putACPTurn stores a resident session job in the given status and the awaiting_input
// event of its turn, endedAgo seconds ago, with or without the needs-reply marker.
func putACPTurn(t *testing.T, st *jobstore.Store, status string, marker bool, endedAgo int64) {
	t.Helper()
	putJob(t, st, jobstore.JobRecord{ID: "acp1", ProjectKey: "p1", Agent: "claude-acp", Status: status,
		SessionStateJSON: `{"turn_no":1}`, UpdatedAt: testNow.Unix() - endedAgo})
	detail := `{"turn_no":1}`
	if marker {
		detail = `{"turn_no":1,"needs_reply":true}`
	}
	_, err := st.InsertJobEvent(jobstore.JobEvent{JobID: "acp1", Type: "job.awaiting_input", Detail: detail, At: testNow.Unix() - endedAgo})
	assert.NoErr(t, err)
}

func TestACPReplyCardWhenMarkedAndPastGrace(t *testing.T) {
	svc, st := openACPStore(t, filepath.Join(t.TempDir(), "a.db"))
	putACPTurn(t, st, "awaiting_input", true, waitgate.GraceSec+1)
	cards, recheck, err := svc.decisionsIn("", false)
	assert.NoErr(t, err)
	assert.Eq(t, []string{KindACPReply + ":acp1"}, keys(cards))
	assert.Eq(t, int64(0), recheck)
	assert.Eq(t, "acp1", cards[0].Refs.JobID)
}

func TestACPReplyNotMarkedIsNoCard(t *testing.T) {
	svc, st := openACPStore(t, filepath.Join(t.TempDir(), "a.db"))
	putACPTurn(t, st, "awaiting_input", false, 3600)
	cards, recheck, err := svc.decisionsIn("", false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(cards))
	assert.Eq(t, int64(0), recheck, "an unmarked turn is a report, nothing to recheck")
}

func TestACPReplyHeldByGraceSetsRecheck(t *testing.T) {
	svc, st := openACPStore(t, filepath.Join(t.TempDir(), "a.db"))
	putACPTurn(t, st, "awaiting_input", true, 100)
	cards, recheck, err := svc.decisionsIn("", false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(cards))
	assert.Eq(t, testNow.Unix()-100+waitgate.GraceSec, recheck)
	resp, err := svc.Today(Query{})
	assert.NoErr(t, err)
	assert.Eq(t, recheck, resp.RecheckAt)
}

func TestACPReplyAnsweredWithinGraceIsNoCard(t *testing.T) {
	svc, st := openACPStore(t, filepath.Join(t.TempDir(), "a.db"))
	putACPTurn(t, st, "awaiting_input", true, 100)
	// the person answers at the keyboard: the next turn starts, the job runs again
	putJob(t, st, jobstore.JobRecord{ID: "acp1", ProjectKey: "p1", Agent: "claude-acp", Status: "running", SessionStateJSON: `{"turn_no":2}`})
	cards, recheck, err := svc.decisionsIn("", false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(cards))
	assert.Eq(t, int64(0), recheck, "an answered turn has nothing left to wait for")
}

func TestACPReplyGoneWhenTheSessionEnds(t *testing.T) {
	svc, st := openACPStore(t, filepath.Join(t.TempDir(), "a.db"))
	putACPTurn(t, st, "done", true, 3600)
	cards, _, err := svc.decisionsIn("", false)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(cards))
}

// The verdict is read from rows alone: a fresh process over the same database (a server
// restart) reaches the same answer.
func TestACPReplyCardSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	svc, st := openACPStore(t, path)
	putACPTurn(t, st, "awaiting_input", true, waitgate.GraceSec+60)
	cards, _, err := svc.decisionsIn("", false)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(cards))
	assert.NoErr(t, st.Close())

	svc2, _ := openACPStore(t, path)
	cards, _, err = svc2.decisionsIn("", false)
	assert.NoErr(t, err)
	assert.Eq(t, []string{KindACPReply + ":acp1"}, keys(cards))
}
