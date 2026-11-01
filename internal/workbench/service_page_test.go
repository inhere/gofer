package workbench

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// seedPageThread stores one finished one-turn session thread "s:<sid>" updated at the given time.
func seedPageThread(t *testing.T, store *jobstore.Store, sid, project string, updated int64, status string) {
	t.Helper()
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "job-" + sid, ProjectKey: project, Agent: "cli", Runner: "local", Cwd: ".",
		SessionID: sid, Interactive: true, Status: status, StartedAt: updated - 5, UpdatedAt: updated,
		CallerID: "alice",
	}, "title "+sid, "prompt "+sid)
}

func threadIDs(resp Response) []string {
	var ids []string
	for _, g := range resp.Projects {
		for _, th := range g.Threads {
			ids = append(ids, th.ID)
		}
	}
	return ids
}

// walkPages follows next_cursor until HasMore is false and returns every page's thread ids.
func walkPages(t *testing.T, svc *Service, first Query) [][]string {
	t.Helper()
	var pages [][]string
	query := first
	for i := 0; i < 100; i++ {
		resp, err := svc.List("alice", query)
		assert.NoErr(t, err)
		pages = append(pages, threadIDs(resp))
		if !resp.HasMore {
			assert.Eq(t, "", resp.NextCursor)
			return pages
		}
		assert.NotEq(t, "", resp.NextCursor)
		query = Query{Limit: first.Limit, Cursor: resp.NextCursor, Project: first.Project, Status: first.Status, Keep: first.Keep}
	}
	t.Fatal("cursor never ended")
	return nil
}

func TestListPageWindowCursorNoDuplicatesNoGaps(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	for i := 0; i < 30; i++ { // inside the 7 day window
		seedPageThread(t, store, fmt.Sprintf("new-%02d", i), "alpha", now-int64(100*(i+1)), job.StatusDone)
	}
	for i := 0; i < 20; i++ { // far outside it
		seedPageThread(t, store, fmt.Sprintf("old-%02d", i), "alpha", now-30*86400-int64(i), job.StatusDone)
	}
	svc := NewService(store, nil, nil)

	// Default (no new parameters): the 7 day window, no paging fields.
	legacy, err := svc.List("alice", Query{})
	assert.NoErr(t, err)
	assert.Len(t, threadIDs(legacy), 30)
	assert.False(t, legacy.HasMore)
	assert.Eq(t, "", legacy.NextCursor)

	// One page big enough for the window: the window ends, older threads remain.
	big, err := svc.List("alice", Query{Limit: 50})
	assert.NoErr(t, err)
	assert.Len(t, threadIDs(big), 30)
	assert.True(t, big.HasMore)
	rest := walkPages(t, svc, Query{Limit: 50, Cursor: big.NextCursor})
	assert.Len(t, rest, 1)
	assert.Len(t, rest[0], 20)

	// Small pages cover everything exactly once, newest first.
	pages := walkPages(t, svc, Query{Limit: 7})
	seen := map[string]bool{}
	var order []string
	for _, p := range pages {
		for _, id := range p {
			assert.False(t, seen[id])
			seen[id] = true
			order = append(order, id)
		}
	}
	assert.Len(t, order, 50)
	assert.Eq(t, "s:new-00", order[0])
	assert.Eq(t, "s:old-00", order[30])
	assert.Eq(t, "s:old-19", order[49])
}

func TestListPageExceptionsPinnedAndLiveIgnoreWindowOnce(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	for i := 0; i < 6; i++ {
		seedPageThread(t, store, fmt.Sprintf("new-%d", i), "alpha", now-int64(10*(i+1)), job.StatusDone)
	}
	old := now - 40*86400
	seedPageThread(t, store, "old-pinned", "alpha", old, job.StatusDone)
	seedPageThread(t, store, "old-running", "alpha", old-1, job.StatusRunning)
	seedPageThread(t, store, "old-awaiting", "alpha", old-2, job.StatusAwaitingInput)
	seedPageThread(t, store, "old-plain", "alpha", old-3, job.StatusDone)
	assert.NoErr(t, store.UpsertWorkbenchThreadPref(jobstore.WorkbenchThreadPref{CallerID: "alice", ThreadID: "s:old-pinned", Pinned: true}))
	svc := NewService(store, nil, nil)

	// Legacy listing keeps the same exceptions (non-terminal) but not the pinned old thread.
	legacy, err := svc.List("alice", Query{})
	assert.NoErr(t, err)
	ids := threadIDs(legacy)
	assert.Len(t, ids, 8)

	first, err := svc.List("alice", Query{Limit: 3})
	assert.NoErr(t, err)
	got := map[string]bool{}
	for _, id := range threadIDs(first) {
		got[id] = true
	}
	// 3 ordinary + 3 exceptions (pinned, running, awaiting-input), exceptions do not count to the limit.
	assert.Len(t, got, 6)
	assert.True(t, got["s:old-pinned"])
	assert.True(t, got["s:old-running"])
	assert.True(t, got["s:old-awaiting"])
	assert.False(t, got["s:old-plain"])
	assert.True(t, first.HasMore)

	// Later pages never repeat an exception.
	pages := walkPages(t, svc, Query{Limit: 3})
	count := map[string]int{}
	for _, p := range pages {
		for _, id := range p {
			count[id]++
		}
	}
	assert.Len(t, count, 10)
	for id, n := range count {
		assert.Eq(t, 1, n, id)
	}
}

func TestListPageKeepFillsPageWithVisibleThreadsOnly(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	for i := 0; i < 40; i++ {
		sid := fmt.Sprintf("t-%02d", i)
		seedPageThread(t, store, sid, "alpha", now-int64(10*(i+1)), job.StatusDone)
	}
	// Every thread whose index is not a multiple of 4 belongs to somebody else.
	keep := func(th Thread) bool {
		var n int
		_, _ = fmt.Sscanf(th.ID, "s:t-%d", &n)
		return n%4 == 0
	}
	svc := NewService(store, nil, nil)
	first, err := svc.List("alice", Query{Limit: 4, Keep: keep})
	assert.NoErr(t, err)
	assert.Eq(t, []string{"s:t-00", "s:t-04", "s:t-08", "s:t-12"}, threadIDs(first))
	assert.True(t, first.HasMore)
	// The cursor points at the last visible thread, never at one the caller may not see.
	at, id, err := decodeCursor(first.NextCursor)
	assert.NoErr(t, err)
	assert.Eq(t, "s:t-12", id)
	assert.Eq(t, now-130, at)

	pages := walkPages(t, svc, Query{Limit: 4, Keep: keep})
	var all []string
	for _, p := range pages {
		all = append(all, p...)
	}
	assert.Len(t, all, 10)
	for _, id := range all {
		assert.True(t, keep(Thread{ID: id}))
	}
}

func TestListPageProjectAndStatusFiltersAndTotal(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	for i := 0; i < 10; i++ {
		seedPageThread(t, store, fmt.Sprintf("a-%d", i), "alpha", now-int64(10*(i+1)), job.StatusDone)
		seedPageThread(t, store, fmt.Sprintf("b-%d", i), "beta", now-int64(10*(i+1))-5, job.StatusDone)
	}
	seedPageThread(t, store, "b-run", "beta", now-1000, job.StatusRunning)
	svc := NewService(store, nil, nil)

	resp, err := svc.List("alice", Query{Limit: 4, Project: "beta"})
	assert.NoErr(t, err)
	assert.Len(t, resp.Projects, 1)
	assert.Eq(t, "beta", resp.Projects[0].ProjectKey)
	// 4 ordinary + the running exception; Total is what this response carries.
	assert.Eq(t, 5, resp.Total)
	assert.True(t, resp.HasMore)

	working, err := svc.List("alice", Query{Limit: 4, Status: StatusWorking})
	assert.NoErr(t, err)
	assert.Eq(t, []string{"s:b-run"}, threadIDs(working))
	assert.False(t, working.HasMore)
}

func TestListPageRejectsInvalidCursor(t *testing.T) {
	svc := NewService(openWorkbenchStore(t), nil, nil)
	for _, c := range []string{"!!!", "bm90LWEtY3Vyc29y"} {
		_, err := svc.List("alice", Query{Cursor: c})
		assert.ErrIs(t, err, ErrInvalidCursor)
	}
}

func TestListActiveWithinIsARelativeSince(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	seedPageThread(t, store, "recent", "alpha", now-60, job.StatusDone)
	seedPageThread(t, store, "hour", "alpha", now-3600, job.StatusDone)
	svc := NewService(store, nil, nil)
	resp, err := svc.List("alice", Query{ActiveWithin: 600})
	assert.NoErr(t, err)
	assert.Eq(t, []string{"s:recent"}, threadIDs(resp))
	assert.Gt(t, resp.Since, int64(0))
}

// Quantifies the window and the page on a large history: the numbers go to the test log
// (go test -v -run TestListPageQuantify); only response sizes are asserted, never timings.
func TestListPageQuantifyHistory(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	const recent, history = 120, 1500
	for i := 0; i < recent; i++ {
		seedPageThread(t, store, fmt.Sprintf("r-%04d", i), "alpha", now-int64(60*(i+1)), job.StatusDone)
	}
	for i := 0; i < history; i++ {
		seedPageThread(t, store, fmt.Sprintf("h-%04d", i), "alpha", now-30*86400-int64(i), job.StatusDone)
	}
	svc := NewService(store, nil, nil)
	measure := func(name string, q Query) int {
		start := time.Now()
		resp, err := svc.List("alice", q)
		assert.NoErr(t, err)
		elapsed := time.Since(start)
		raw, err := json.Marshal(resp)
		assert.NoErr(t, err)
		t.Logf("%-28s threads=%4d bytes=%7d elapsed=%v", name, len(threadIDs(resp)), len(raw), elapsed)
		return len(raw)
	}
	all := measure("all history (since=1)", Query{Since: 1})
	window := measure("7 day window (default)", Query{})
	page := measure("window + limit=30", Query{Limit: 30})
	assert.Lt(t, window, all)
	assert.Lt(t, page, window)
}
