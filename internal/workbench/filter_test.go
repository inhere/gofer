package workbench

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// seedKindThread stores one finished one-row thread: interactive = PTY, otherwise a resident ACP session.
func seedKindThread(t *testing.T, store *jobstore.Store, sid string, interactive bool, updated int64, status string) {
	t.Helper()
	rec := jobstore.JobRecord{
		ID: "job-" + sid, ProjectKey: "alpha", Agent: "cli", Runner: "local", Cwd: ".",
		SessionID: sid, Interactive: interactive, Status: status, StartedAt: updated - 5, UpdatedAt: updated,
		CallerID: "alice",
	}
	if !interactive {
		rec.SessionStateJSON = `{"session":true}`
	}
	putWorkbenchJob(t, store, rec, "title "+sid, "prompt "+sid)
}

func seedRelaySession(t *testing.T, store *jobstore.Store, sid, state string, seen int64) {
	t.Helper()
	_, err := store.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "alpha", State: state, StartedAt: seen - 5, LastSeenAt: seen})
	assert.NoErr(t, err)
}

func sortedIDs(resp Response) []string {
	ids := threadIDs(resp)
	sort.Strings(ids)
	return ids
}

func TestListPageSourceFilter(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	old := now - 40*86400
	seedKindThread(t, store, "acp-new", false, now-10, job.StatusDone)
	seedKindThread(t, store, "pty-new", true, now-20, job.StatusDone)
	seedKindThread(t, store, "acp-pinned", false, old, job.StatusDone)
	seedKindThread(t, store, "pty-pinned", true, old, job.StatusDone)
	seedKindThread(t, store, "acp-live", false, old-1, job.StatusRunning)
	seedKindThread(t, store, "pty-live", true, old-1, job.StatusRunning)
	seedRelaySession(t, store, "rel-live", jobstore.SessionRunning, now-30)
	for _, id := range []string{"s:acp-pinned", "s:pty-pinned"} {
		assert.NoErr(t, store.UpsertWorkbenchThreadPref(jobstore.WorkbenchThreadPref{CallerID: "alice", ThreadID: id, Pinned: true}))
	}
	svc := NewService(store, nil, nil)

	for _, paged := range []bool{true, false} {
		q := func(src string) Query {
			if paged {
				return Query{Limit: 20, Source: src}
			}
			return Query{Source: src, Since: old - 100}
		}
		acp, err := svc.List("alice", q(SourceACP))
		assert.NoErr(t, err)
		pty, err := svc.List("alice", q(SourcePTY))
		assert.NoErr(t, err)
		relay, err := svc.List("alice", q(SourceRelay))
		assert.NoErr(t, err)
		assert.Eq(t, []string{"s:acp-live", "s:acp-new", "s:acp-pinned"}, sortedIDs(acp))
		assert.Eq(t, []string{"s:pty-live", "s:pty-new", "s:pty-pinned"}, sortedIDs(pty))
		assert.Eq(t, []string{"r:rel-live"}, sortedIDs(relay))
		all, err := svc.List("alice", q(""))
		assert.NoErr(t, err)
		assert.Gt(t, len(threadIDs(all)), len(threadIDs(acp))+len(threadIDs(pty)))
	}
}

func TestThreadLive(t *testing.T) {
	assert.True(t, threadLive(Thread{Kind: KindRelay, RawStatus: jobstore.SessionRunning}))
	assert.True(t, threadLive(Thread{Kind: KindRelay, RawStatus: jobstore.SessionIdle}))
	assert.False(t, threadLive(Thread{Kind: KindRelay, RawStatus: jobstore.SessionEnded}))
	assert.False(t, threadLive(Thread{Kind: KindRelay, RawStatus: jobstore.SessionOffline}))
	assert.False(t, threadLive(Thread{Kind: KindAgent, RawStatus: job.StatusDone}))
	assert.True(t, threadLive(Thread{Kind: KindAgent, RawStatus: job.StatusRunning}))
	// An older row still running while the latest row is finished: the thread is live.
	assert.True(t, threadLive(Thread{Kind: KindAgent, RawStatus: job.StatusDone,
		Jobs: []JobTurn{{Status: job.StatusRunning}, {Status: job.StatusDone}}}))
	assert.False(t, threadLive(Thread{Kind: KindAgent, RawStatus: job.StatusRunning,
		Jobs: []JobTurn{{Status: job.StatusDone}, {Status: job.StatusDone}}}))
}

func TestValidSource(t *testing.T) {
	for _, v := range []string{"", "acp", "pty", "relay"} {
		assert.True(t, ValidSource(v), v)
	}
	assert.False(t, ValidSource("bad"))
	assert.False(t, ValidSource("ACP"))
}

// pageCountingStore counts LoadWorkbenchPage calls over a real store.
type pageCountingStore struct {
	*jobstore.Store
	pages int
}

func (s *pageCountingStore) LoadWorkbenchPage(callerID string, q jobstore.WorkbenchPageQuery) (jobstore.WorkbenchPage, error) {
	s.pages++
	return s.Store.LoadWorkbenchPage(callerID, q)
}

func TestListPageLiveShortCircuit(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	for i := 0; i < 130; i++ { // finished ACP threads that pass the source filter, inside the window
		seedKindThread(t, store, fmt.Sprintf("done-%03d", i), false, now-int64(100+i), job.StatusDone)
	}
	seedKindThread(t, store, "run", false, now-5000, job.StatusRunning)
	// Older row still running, latest row finished: still live.
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "multi-1", ProjectKey: "alpha", Agent: "cli", Runner: "local", Cwd: ".", SessionID: "multi",
		SessionStateJSON: `{"session":true}`, Status: job.StatusRunning, StartedAt: now - 3000, UpdatedAt: now - 3000, CallerID: "alice",
	}, "multi", "p1")
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "multi-2", ProjectKey: "alpha", Agent: "cli", Runner: "local", Cwd: ".", SessionID: "multi",
		SessionStateJSON: `{"session":true}`, Status: job.StatusDone, StartedAt: now - 2000, EndedAt: now - 1990, UpdatedAt: now - 1990, CallerID: "alice",
	}, "multi", "p2")
	seedRelaySession(t, store, "rel-run", jobstore.SessionRunning, now-60)
	seedRelaySession(t, store, "rel-end", jobstore.SessionEnded, now-60)
	counting := &pageCountingStore{Store: store}
	svc := NewService(counting, nil, nil)

	resp, err := svc.List("alice", Query{Limit: 5, Live: true})
	assert.NoErr(t, err)
	assert.Eq(t, []string{"r:rel-run", "s:multi", "s:run"}, sortedIDs(resp))
	assert.False(t, resp.HasMore)
	assert.Eq(t, "", resp.NextCursor)
	assert.Eq(t, 1, counting.pages)

	// The same data without live pages through many store rounds (the counting store sees them).
	counting.pages = 0
	_, err = svc.List("alice", Query{Limit: 5, Source: SourceACP, Status: StatusWorking})
	assert.NoErr(t, err)
	assert.Gt(t, counting.pages, 1)

	// A live request with a cursor is an empty page and never reads the store.
	counting.pages = 0
	empty, err := svc.List("alice", Query{Limit: 5, Live: true, Cursor: encodeCursor(now, "s:x")})
	assert.NoErr(t, err)
	assert.Len(t, threadIDs(empty), 0)
	assert.False(t, empty.HasMore)
	assert.Eq(t, 0, counting.pages)
}

func TestListPageInclude(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	old := now - 30*86400
	for i := 0; i < 4; i++ {
		seedKindThread(t, store, fmt.Sprintf("new-%d", i), false, now-int64(10*(i+1)), job.StatusDone)
	}
	seedKindThread(t, store, "old-plain", false, old, job.StatusDone)
	seedKindThread(t, store, "old-other", false, old-1, job.StatusDone)
	seedKindThread(t, store, "old-pin", false, old-2, job.StatusDone)
	assert.NoErr(t, store.UpsertWorkbenchThreadPref(jobstore.WorkbenchThreadPref{CallerID: "alice", ThreadID: "s:old-pin", Pinned: true}))
	svc := NewService(store, nil, nil)
	prefsBefore, err := store.ListWorkbenchThreadPrefs("alice")
	assert.NoErr(t, err)

	base, err := svc.List("alice", Query{Limit: 3})
	assert.NoErr(t, err)
	assert.NotContains(t, threadIDs(base), "s:old-plain")

	resp, err := svc.List("alice", Query{Limit: 3, Include: []string{"s:old-plain", "s:old-pin", "s:old-plain"}})
	assert.NoErr(t, err)
	ids := threadIDs(resp)
	assert.Contains(t, ids, "s:old-plain")
	assert.NotContains(t, ids, "s:old-other")
	count := map[string]int{}
	for _, id := range ids {
		count[id]++
	}
	assert.Eq(t, 1, count["s:old-plain"])
	assert.Eq(t, 1, count["s:old-pin"])
	for _, th := range resp.Projects[0].Threads {
		if th.ID == "s:old-plain" {
			assert.False(t, th.Pinned)
		}
	}
	prefsAfter, err := store.ListWorkbenchThreadPrefs("alice")
	assert.NoErr(t, err)
	assert.Eq(t, prefsBefore, prefsAfter) // include never writes a preference

	// With a cursor include does not apply (the helper leaves pinned alone).
	assert.Eq(t, []string{"p"}, withIncluded([]string{"p"}, Query{Cursor: "x", Include: []string{"s:a"}}))
	assert.Eq(t, []string{"p", "s:a"}, withIncluded([]string{"p"}, Query{Include: []string{"s:a", "p", "s:a"}}))
	cur, err := svc.List("alice", Query{Limit: 3, Include: []string{"s:old-plain"}, Cursor: encodeCursor(old, "")})
	assert.NoErr(t, err)
	assert.NotContains(t, threadIDs(cur), "s:old-plain")

	// An included thread still has to pass Keep.
	kept, err := svc.List("alice", Query{Limit: 3, Include: []string{"s:old-plain"},
		Keep: func(th Thread) bool { return th.ID != "s:old-plain" }})
	assert.NoErr(t, err)
	assert.NotContains(t, threadIDs(kept), "s:old-plain")

	// ...and the other filters (source).
	pty, err := svc.List("alice", Query{Limit: 3, Include: []string{"s:old-plain"}, Source: SourcePTY})
	assert.NoErr(t, err)
	assert.NotContains(t, threadIDs(pty), "s:old-plain")

	// Unpaged listing ignores include.
	legacy, err := svc.List("alice", Query{Include: []string{"s:old-plain"}})
	assert.NoErr(t, err)
	assert.NotContains(t, threadIDs(legacy), "s:old-plain")
}
