package serve

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func offlineStore(t *testing.T) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func regSession(t *testing.T, st *jobstore.Store, sid string) {
	t.Helper()
	if _, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p"}); err != nil {
		t.Fatal(err)
	}
}

func stateOf(t *testing.T, st *jobstore.Store, sid string) string {
	t.Helper()
	a, ok, err := st.GetAgentSession(sid)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", sid, ok, err)
	}
	return a.State
}

func TestSessionOfflineSweep(t *testing.T) {
	st := offlineStore(t)
	regSession(t, st, "quiet")
	regSession(t, st, "fresh")
	regSession(t, st, "gone")
	regSession(t, st, "waiting")
	// Everything registered "now"; sweep as if 2h later, except fresh which beat later.
	later := time.Now().Add(2 * time.Hour)
	if _, _, err := st.TouchAgentSession("gone", jobstore.SessionHeartbeat{Event: "SessionEnd", State: jobstore.SessionEnded}); err != nil {
		t.Fatal(err)
	}
	// a relay turn still OPEN well beyond the sweep time keeps its session out
	if err := st.InsertDecision(&jobstore.PlanDecision{
		ID: "d1", Title: "t", Question: "q", SessionID: "waiting", Kind: "relay",
		TimeoutSec: 86400, AskedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	// threshold 0 = off
	if n, _ := sweepSessionsOffline(st, 0, later); n != 0 {
		t.Fatalf("disabled sweep changed %d rows", n)
	}
	// threshold not yet reached -> nothing
	if n, _ := sweepSessionsOffline(st, 3*3600, later); n != 0 {
		t.Fatalf("below threshold changed %d rows", n)
	}
	n, err := sweepSessionsOffline(st, 1800, later)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("offline count=%d, want quiet+fresh(2)", n)
	}
	if stateOf(t, st, "quiet") != jobstore.SessionOffline {
		t.Fatal("quiet should be offline")
	}
	if stateOf(t, st, "gone") != jobstore.SessionEnded {
		t.Fatal("ended must stay ended")
	}
	if stateOf(t, st, "waiting") == jobstore.SessionOffline {
		t.Fatal("a session parked on an OPEN relay turn must not be marked offline")
	}

	// A heartbeat revives; a heartbeat carrying a state refines it.
	if _, ok, err := st.TouchAgentSession("quiet", jobstore.SessionHeartbeat{Event: "PostToolUse"}); err != nil || !ok {
		t.Fatalf("touch: %v %v", ok, err)
	}
	if got := stateOf(t, st, "quiet"); got != jobstore.SessionRunning {
		t.Fatalf("revived state=%s", got)
	}
	if _, err := sweepSessionsOffline(st, 1800, later); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.TouchAgentSession("fresh", jobstore.SessionHeartbeat{Event: "Stop", State: jobstore.SessionIdle}); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, st, "fresh"); got != jobstore.SessionIdle {
		t.Fatalf("fresh state=%s, want idle", got)
	}
	// Re-register also revives.
	if _, err := sweepSessionsOffline(st, 1800, later.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	regSession(t, st, "fresh")
	if got := stateOf(t, st, "fresh"); got != jobstore.SessionRunning {
		t.Fatalf("re-register state=%s", got)
	}
	// Once the open turn's deadline is also far in the past the waiting session goes too.
	if n, _ := sweepSessionsOffline(st, 1800, time.Now().Add(72*time.Hour)); n == 0 {
		t.Fatal("waiting session should go offline once its turn deadline is long past")
	}
}

func TestSessionOfflineThresholdIsHotConfig(t *testing.T) {
	var c *config.Config
	if got := c.EffectiveSessionOfflineAfterSec(); got != config.DefaultSessionOfflineAfterSec {
		t.Fatalf("default=%d", got)
	}
	zero, big := 0, 600
	c = &config.Config{Session: config.SessionConfig{OfflineAfterSec: &zero}}
	if c.EffectiveSessionOfflineAfterSec() != 0 {
		t.Fatal("0 must disable")
	}
	c = &config.Config{Session: config.SessionConfig{OfflineAfterSec: &big}}
	if c.EffectiveSessionOfflineAfterSec() != 600 {
		t.Fatal("explicit value must win")
	}
	if cl := c.Clone(); cl.Session.OfflineAfterSec == c.Session.OfflineAfterSec || *cl.Session.OfflineAfterSec != 600 {
		t.Fatal("Clone must deep-copy offline_after_sec")
	}
}

// The loop re-reads the threshold every tick, so a config reload takes effect on the
// next sweep without restarting anything.
func TestSessionOfflineLoopPicksUpReloadedThreshold(t *testing.T) {
	st := offlineStore(t)
	regSession(t, st, "s1")
	time.Sleep(1100 * time.Millisecond) // last_seen_at has 1s resolution
	var after atomic.Int64
	after.Store(0) // sweep disabled
	stop := make(chan struct{})
	defer close(stop)
	startSessionOfflineLoop(st, func() int { return int(after.Load()) }, 20*time.Millisecond, stop)
	time.Sleep(100 * time.Millisecond)
	if stateOf(t, st, "s1") == jobstore.SessionOffline {
		t.Fatal("sweep ran while disabled")
	}
	after.Store(1) // "reload": 1s threshold, session already silent >1s
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stateOf(t, st, "s1") == jobstore.SessionOffline {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("reloaded threshold never applied")
}
