package presence

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/jobstore"
)

// testClock is a manually-advanced clock so tests pin TTL/expiry windows.
type testClock struct {
	mu  sync.Mutex
	sec int64
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Unix(c.sec, 0)
}

func (c *testClock) set(sec int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sec = sec
}

// newTestService builds a Service over a temp-dir jobstore with a manual clock and
// a deterministic id generator (id-1, id-2, …) so assertions can name ids.
func newTestService(t *testing.T) (*Service, *testClock) {
	t.Helper()
	store, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = store.Close() })

	clk := &testClock{sec: 1000}
	var n int
	svc := NewService(store)
	svc.nowFn = clk.now
	svc.newID = func() string { n++; return fmt.Sprintf("id-%d", n) }
	return svc, clk
}

func TestRegisterMintsIDAndToken(t *testing.T) {
	svc, _ := newTestService(t)

	res, err := svc.Register(RegisterInput{Name: "alice", Role: "reviewer", CallerID: "c1"})
	assert.NoErr(t, err)
	assert.NotEmpty(t, res.AgentID)
	assert.NotEmpty(t, res.AgentToken)
	assert.Neq(t, res.AgentID, res.AgentToken)
}

func TestRegisterRenewsSameNameCaller(t *testing.T) {
	svc, clk := newTestService(t)

	first, err := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})
	assert.NoErr(t, err)

	clk.set(1050)
	again, err := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})
	assert.NoErr(t, err)
	// 续约: same agent_id + token reused, last_seen refreshed.
	assert.Eq(t, first.AgentID, again.AgentID)
	assert.Eq(t, first.AgentToken, again.AgentToken)

	list, err := svc.List("", "")
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, int64(1050), list[0].LastSeenAt)
}

func TestRegisterDifferentCallerIsNewAgent(t *testing.T) {
	svc, _ := newTestService(t)
	a, err := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})
	assert.NoErr(t, err)
	b, err := svc.Register(RegisterInput{Name: "alice", CallerID: "c2"})
	assert.NoErr(t, err)
	assert.Neq(t, a.AgentID, b.AgentID)
}

func TestRegisterRejectsEmptyName(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Register(RegisterInput{Name: "  "})
	assert.Err(t, err)
}

func TestPostDirectAndPollConsumes(t *testing.T) {
	svc, _ := newTestService(t)
	a, _ := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})
	b, _ := svc.Register(RegisterInput{Name: "bob", CallerID: "c1"})

	n, err := svc.Post(a.AgentID, b.AgentID, KindTask, "review PR", "job:1")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)

	// First poll (ack=true) returns the message and consumes it.
	msgs, err := svc.Poll(b.AgentID, b.AgentToken, true)
	assert.NoErr(t, err)
	assert.Len(t, msgs, 1)
	assert.Eq(t, a.AgentID, msgs[0].FromAgent)
	assert.Eq(t, KindTask, msgs[0].Kind)
	assert.Eq(t, "review PR", msgs[0].Body)
	assert.Eq(t, "job:1", msgs[0].Ref)

	// Second poll is empty (already read).
	msgs2, err := svc.Poll(b.AgentID, b.AgentToken, true)
	assert.NoErr(t, err)
	assert.Len(t, msgs2, 0)
}

func TestPollPeekDoesNotConsume(t *testing.T) {
	svc, _ := newTestService(t)
	a, _ := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})
	b, _ := svc.Register(RegisterInput{Name: "bob", CallerID: "c1"})
	_, _ = svc.Post(a.AgentID, b.AgentID, KindNote, "hi", "")

	// ack=false: peek leaves it unread.
	peek, err := svc.Poll(b.AgentID, b.AgentToken, false)
	assert.NoErr(t, err)
	assert.Len(t, peek, 1)

	// Still there on the next peek.
	again, err := svc.Poll(b.AgentID, b.AgentToken, false)
	assert.NoErr(t, err)
	assert.Len(t, again, 1)
}

func TestPollRefreshesHeartbeat(t *testing.T) {
	svc, clk := newTestService(t)
	a, _ := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})

	clk.set(1080)
	_, err := svc.Poll(a.AgentID, a.AgentToken, true)
	assert.NoErr(t, err)

	list, err := svc.List("", "")
	assert.NoErr(t, err)
	assert.Eq(t, int64(1080), list[0].LastSeenAt)
}

func TestPollTokenMismatchUnauthorized(t *testing.T) {
	svc, _ := newTestService(t)
	a, _ := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})

	_, err := svc.Poll(a.AgentID, "wrong-token", true)
	assert.Err(t, err)
	assert.True(t, err == ErrUnauthorizedAgent)
}

func TestPollUnknownAgent(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Poll("ghost", "t", true)
	assert.True(t, err == ErrUnknownAgent)
}

func TestPostRoleFanOutToOnlineMatches(t *testing.T) {
	svc, _ := newTestService(t)
	sender, _ := svc.Register(RegisterInput{Name: "sender", CallerID: "c1"})
	r1, _ := svc.Register(RegisterInput{Name: "rev1", Role: "reviewer", CallerID: "c1"})
	r2, _ := svc.Register(RegisterInput{Name: "rev2", Role: "reviewer", CallerID: "c1"})
	_, _ = svc.Register(RegisterInput{Name: "writer", Role: "writer", CallerID: "c1"})

	n, err := svc.Post(sender.AgentID, "role:reviewer", KindTask, "审 PR", "")
	assert.NoErr(t, err)
	assert.Eq(t, 2, n) // both reviewers, not the writer

	m1, _ := svc.Poll(r1.AgentID, r1.AgentToken, true)
	assert.Len(t, m1, 1)
	assert.Eq(t, "role:reviewer", m1[0].ToSpec)
	m2, _ := svc.Poll(r2.AgentID, r2.AgentToken, true)
	assert.Len(t, m2, 1)
}

func TestPostRoleSkipsOfflineMatches(t *testing.T) {
	svc, clk := newTestService(t)
	sender, _ := svc.Register(RegisterInput{Name: "sender", CallerID: "c1"})
	online, _ := svc.Register(RegisterInput{Name: "rev1", Role: "reviewer", CallerID: "c1"})
	_, _ = svc.Register(RegisterInput{Name: "rev2", Role: "reviewer", CallerID: "c1"})

	// Advance past TTL, then refresh only `online` and the sender so rev2 is offline.
	clk.set(1000 + int64(DefaultTTL/time.Second) + 10)
	_, _ = svc.Poll(online.AgentID, online.AgentToken, true)
	_, _ = svc.Poll(sender.AgentID, sender.AgentToken, true)

	n, err := svc.Post(sender.AgentID, "role:reviewer", KindTask, "x", "")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n) // only the still-online reviewer
}

func TestPostNoRecipientReturnsZero(t *testing.T) {
	svc, _ := newTestService(t)
	sender, _ := svc.Register(RegisterInput{Name: "sender", CallerID: "c1"})

	// role with no members.
	n, err := svc.Post(sender.AgentID, "role:nobody", KindTask, "x", "")
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)

	// unknown direct agent.
	n2, err := svc.Post(sender.AgentID, "ghost", KindTask, "x", "")
	assert.NoErr(t, err)
	assert.Eq(t, 0, n2)
}

// TestPostDirectToOfflineStoresAndForwards: a direct send to a KNOWN-but-offline
// agent is stored (delivered=1) and waits in its inbox until it next polls —
// store-and-forward, the best-effort unreachable contract for direct addressing.
func TestPostDirectToOfflineStoresAndForwards(t *testing.T) {
	svc, clk := newTestService(t)
	sender, _ := svc.Register(RegisterInput{Name: "sender", CallerID: "c1"})
	target, _ := svc.Register(RegisterInput{Name: "target", CallerID: "c1"})

	// Push target past TTL (offline); keep only the sender fresh.
	clk.set(1000 + int64(DefaultTTL/time.Second) + 10)
	_, _ = svc.Poll(sender.AgentID, sender.AgentToken, true)

	n, err := svc.Post(sender.AgentID, target.AgentID, KindTask, "later", "")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n) // stored despite the recipient being offline

	// The target comes back and collects it.
	m, err := svc.Poll(target.AgentID, target.AgentToken, true)
	assert.NoErr(t, err)
	assert.Len(t, m, 1)
	assert.Eq(t, "later", m[0].Body)
}

// TestPostRoleOnePicksExactlyOneOnline: role-one:<name> delivers to exactly ONE of
// the online agents of that role (work-assignment), not a fan-out to all.
func TestPostRoleOnePicksExactlyOneOnline(t *testing.T) {
	svc, _ := newTestService(t)
	sender, _ := svc.Register(RegisterInput{Name: "sender", CallerID: "c1"})
	revs := make([]RegisterResult, 0, 3)
	for i := 0; i < 3; i++ {
		r, _ := svc.Register(RegisterInput{Name: fmt.Sprintf("rev%d", i), Role: "reviewer", CallerID: "c1"})
		revs = append(revs, r)
	}
	writer, _ := svc.Register(RegisterInput{Name: "writer", Role: "writer", CallerID: "c1"})

	n, err := svc.Post(sender.AgentID, "role-one:reviewer", KindTask, "认领", "")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n) // exactly one reviewer, not fan-out to all three

	got := 0
	for _, r := range revs {
		m, _ := svc.Poll(r.AgentID, r.AgentToken, true)
		got += len(m)
	}
	assert.Eq(t, 1, got) // the message landed in exactly one reviewer's inbox
	wm, _ := svc.Poll(writer.AgentID, writer.AgentToken, true)
	assert.Len(t, wm, 0) // never the writer (wrong role)
}

// TestPostRoleOnePicksOnlyOnline: role-one skips offline agents and returns 0 when
// none of the role is online (best-effort, like role:/broadcast).
func TestPostRoleOnePicksOnlyOnline(t *testing.T) {
	svc, clk := newTestService(t)
	sender, _ := svc.Register(RegisterInput{Name: "sender", CallerID: "c1"})
	online, _ := svc.Register(RegisterInput{Name: "rev-on", Role: "reviewer", CallerID: "c1"})
	_, _ = svc.Register(RegisterInput{Name: "rev-off", Role: "reviewer", CallerID: "c1"})

	// Past TTL, refresh only `online` + sender so rev-off is offline.
	clk.set(1000 + int64(DefaultTTL/time.Second) + 10)
	_, _ = svc.Poll(online.AgentID, online.AgentToken, true)
	_, _ = svc.Poll(sender.AgentID, sender.AgentToken, true)

	n, err := svc.Post(sender.AgentID, "role-one:reviewer", KindTask, "x", "")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n) // the only online reviewer
	m, _ := svc.Poll(online.AgentID, online.AgentToken, true)
	assert.Len(t, m, 1)

	// Now everyone offline → no online match → delivered 0 (best-effort, nothing stored).
	clk.set(1000 + 2*int64(DefaultTTL/time.Second) + 30)
	_, _ = svc.Poll(sender.AgentID, sender.AgentToken, true)
	n2, err := svc.Post(sender.AgentID, "role-one:reviewer", KindTask, "x", "")
	assert.NoErr(t, err)
	assert.Eq(t, 0, n2)
}

func TestPostBroadcast(t *testing.T) {
	svc, _ := newTestService(t)
	sender, _ := svc.Register(RegisterInput{Name: "sender", CallerID: "c1"})
	_, _ = svc.Register(RegisterInput{Name: "a", CallerID: "c1"})
	_, _ = svc.Register(RegisterInput{Name: "b", CallerID: "c1"})

	n, err := svc.Post(sender.AgentID, "broadcast", KindNote, "all hands", "")
	assert.NoErr(t, err)
	assert.Eq(t, 3, n) // sender + a + b all online
}

func TestListComputesOfflineLazily(t *testing.T) {
	svc, clk := newTestService(t)
	_, _ = svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})

	// Within TTL → online.
	list, err := svc.List("", "")
	assert.NoErr(t, err)
	assert.Len(t, list, 1)
	assert.Eq(t, StatusOnline, list[0].Status)

	// Past TTL → offline (computed, row not deleted).
	clk.set(1000 + int64(DefaultTTL/time.Second) + 1)
	list2, err := svc.List("", "")
	assert.NoErr(t, err)
	assert.Len(t, list2, 1)
	assert.Eq(t, StatusOffline, list2[0].Status)
}

func TestListFiltersRoleAndProject(t *testing.T) {
	svc, _ := newTestService(t)
	_, _ = svc.Register(RegisterInput{Name: "a", Role: "reviewer", ProjectKey: "p1", CallerID: "c1"})
	_, _ = svc.Register(RegisterInput{Name: "b", Role: "writer", ProjectKey: "p1", CallerID: "c1"})
	_, _ = svc.Register(RegisterInput{Name: "c", Role: "reviewer", ProjectKey: "p2", CallerID: "c1"})

	byRole, err := svc.List("reviewer", "")
	assert.NoErr(t, err)
	assert.Len(t, byRole, 2)

	byProj, err := svc.List("", "p1")
	assert.NoErr(t, err)
	assert.Len(t, byProj, 2)

	both, err := svc.List("reviewer", "p1")
	assert.NoErr(t, err)
	assert.Len(t, both, 1)
}

func TestDeregister(t *testing.T) {
	svc, _ := newTestService(t)
	a, _ := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})

	// Wrong token rejected.
	assert.True(t, svc.Deregister(a.AgentID, "wrong") == ErrUnauthorizedAgent)

	// Correct token removes it; idempotent thereafter.
	assert.NoErr(t, svc.Deregister(a.AgentID, a.AgentToken))
	assert.NoErr(t, svc.Deregister(a.AgentID, a.AgentToken))

	list, err := svc.List("", "")
	assert.NoErr(t, err)
	assert.Len(t, list, 0)
}

func TestPrune(t *testing.T) {
	svc, clk := newTestService(t)
	a, _ := svc.Register(RegisterInput{Name: "alice", CallerID: "c1"})
	b, _ := svc.Register(RegisterInput{Name: "bob", CallerID: "c1"})
	_, _ = svc.Post(a.AgentID, b.AgentID, KindTask, "x", "")
	// Consume so the message is read (prune-eligible).
	_, _ = svc.Poll(b.AgentID, b.AgentToken, true)

	// Advance well past TTL so both presence rows are stale.
	clk.set(1000 + int64(DefaultTTL/time.Second) + 100)
	pN, mN, err := svc.Prune()
	assert.NoErr(t, err)
	assert.Eq(t, 2, pN) // both agents pruned (offline past TTL)
	assert.Eq(t, 1, mN) // the read message pruned

	list, err := svc.List("", "")
	assert.NoErr(t, err)
	assert.Len(t, list, 0)
}
