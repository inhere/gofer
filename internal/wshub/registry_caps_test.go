package wshub

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

// TestWorkerSnapshotCarriesCapsAndNodeInfo (federation P2 / T2.1): the register
// frame's capability report (AgentCaps + the bare Agents key list) and node info
// (OS/Arch/GoferVersion/StartedAt) must round-trip through WorkerSnapshot — it is
// the ONLY window the host (job validation P3, runners panel P4) has onto what a
// worker can actually run.
func TestWorkerSnapshotCarriesCapsAndNodeInfo(t *testing.T) {
	meta := wsproto.Register{
		WorkerID:        "w1",
		InstanceID:      "inst-1",
		ProtocolVersion: wsproto.CurrentProtocolVersion,
		PtyCapable:      true,
		OS:              "linux",
		Arch:            "arm64",
		GoferVersion:    "v1.2.3",
		StartedAt:       1700000000,
		Labels:          []string{"gpu"},
		Projects:        []string{"alpha", "beta"},
		Agents:          []string{"claude", "exec"},
		AgentCaps: []wsproto.AgentBrief{
			{Key: "claude", Type: "cli-agent", Interactive: true},
			{Key: "exec", Type: "exec"},
		},
	}
	r := newRegistry()
	wc := newWorkerConn("w1", "w1", nil, meta)
	wc.lastHeartbeat.Store(1700000001)
	r.Put(wc)

	snap, ok := r.WorkerSnapshot("w1")
	if !ok {
		t.Fatal("WorkerSnapshot(w1) not found")
	}
	if snap.OS != "linux" || snap.Arch != "arm64" || snap.GoferVersion != "v1.2.3" || snap.StartedAt != 1700000000 {
		t.Fatalf("node info = %+v, want os=linux arch=arm64 ver=v1.2.3 started=1700000000", snap)
	}
	if snap.InstanceID != "inst-1" || !snap.PtyCapable || snap.LastHeartbeat != 1700000001 {
		t.Fatalf("base fields not carried: %+v", snap)
	}
	if len(snap.Projects) != 2 || snap.Projects[0] != "alpha" || snap.Agents[1] != "exec" || snap.Labels[0] != "gpu" {
		t.Fatalf("labels/projects/agents = %v / %v / %v", snap.Labels, snap.Projects, snap.Agents)
	}
	if len(snap.AgentCaps) != 2 {
		t.Fatalf("AgentCaps = %+v, want 2 entries", snap.AgentCaps)
	}
	if snap.AgentCaps[0] != (wsproto.AgentBrief{Key: "claude", Type: "cli-agent", Interactive: true}) {
		t.Fatalf("AgentCaps[0] = %+v", snap.AgentCaps[0])
	}
	if snap.AgentCaps[1] != (wsproto.AgentBrief{Key: "exec", Type: "exec"}) {
		t.Fatalf("AgentCaps[1] = %+v", snap.AgentCaps[1])
	}

	// Every slice must be a defensive COPY: a consumer mutating its snapshot (or
	// appending to it) must not corrupt the live registry's register metadata.
	snap.Labels[0] = "mutated"
	snap.Projects[0] = "mutated"
	snap.Agents[0] = "mutated"
	snap.AgentCaps[0].Key = "mutated"

	again, ok := r.WorkerSnapshot("w1")
	if !ok {
		t.Fatal("WorkerSnapshot(w1) not found on re-read")
	}
	if again.Labels[0] != "gpu" || again.Projects[0] != "alpha" || again.Agents[0] != "claude" || again.AgentCaps[0].Key != "claude" {
		t.Fatalf("snapshot slices alias the registry meta: %v / %v / %v / %+v",
			again.Labels, again.Projects, again.Agents, again.AgentCaps)
	}
}

// TestWorkerSnapshotOfflineWorker: an unregistered worker has no capability view
// (ok=false) — P3 treats that as "offline", it must never fall back to an empty
// but ok=true snapshot.
func TestWorkerSnapshotOfflineWorker(t *testing.T) {
	r := newRegistry()
	if _, ok := r.WorkerSnapshot("nope"); ok {
		t.Fatal("WorkerSnapshot of an unregistered worker must return ok=false")
	}
}

// capsFor builds a Caps snapshot with a distinguishable tag, so a test can tell WHICH
// update landed (the point of most assertions below is which of two writers won).
func capsFor(tag string, maxConc int) wsproto.Caps {
	return wsproto.Caps{
		Labels:    []string{"label-" + tag},
		Projects:  []string{"proj-" + tag},
		Agents:    []string{"agent-" + tag},
		AgentCaps: []wsproto.AgentBrief{{Key: "agent-" + tag, Type: "exec"}},
		MaxConc:   maxConc,
	}
}

// TestUpdateCapsIsFullReplacement: Caps is a SNAPSHOT, not a patch. A reload that
// removes every project must leave the worker with no project — if an empty list were
// read as "unchanged", a worker that was just de-scoped would keep receiving jobs for
// projects it no longer serves.
func TestUpdateCapsIsFullReplacement(t *testing.T) {
	r := newRegistry()
	wc := newWorkerConn("w1", "w1", nil, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion,
		Labels: []string{"gpu"}, Projects: []string{"alpha", "beta"},
		Agents: []string{"claude"}, AgentCaps: []wsproto.AgentBrief{{Key: "claude", Type: "cli-agent"}},
	})
	r.Put(wc)

	r.UpdateCaps(wc, wsproto.Caps{
		Labels: []string{}, Projects: []string{}, Agents: []string{}, AgentCaps: []wsproto.AgentBrief{},
	})
	snap, ok := r.WorkerSnapshot("w1")
	if !ok {
		t.Fatal("worker vanished from the registry")
	}
	if len(snap.Projects) != 0 || len(snap.Labels) != 0 || len(snap.Agents) != 0 || len(snap.AgentCaps) != 0 {
		t.Fatalf("an empty caps snapshot must clear the capabilities, got %+v", snap)
	}
}

// TestUpdateCapsFromSupersededConnIgnored is the "old process poisons the new one"
// regression. A worker restarts: the registry now points at conn B, but conn A's read
// loop is still draining and delivers a caps frame from the DEAD process. Keying the
// update by worker_id would let A's capabilities overwrite B's — the hub would then
// route jobs at the live worker based on what the dead one could do.
func TestUpdateCapsFromSupersededConnIgnored(t *testing.T) {
	r := newRegistry()
	base := wsproto.Register{WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion}

	connA := newWorkerConn("w1", "w1", nil, base)
	r.Put(connA)
	r.UpdateCaps(connA, capsFor("old", 2)) // A is still current here: this one lands

	connB := newWorkerConn("w1", "w1", nil, base) // restart: B replaces A
	r.Put(connB)
	r.UpdateCaps(connB, capsFor("new", 5))

	// The late frame from the superseded conn A.
	r.UpdateCaps(connA, capsFor("stale", 99))

	snap, ok := r.WorkerSnapshot("w1")
	if !ok {
		t.Fatal("worker vanished from the registry")
	}
	if snap.Projects[0] != "proj-new" || snap.Agents[0] != "agent-new" || snap.Labels[0] != "label-new" {
		t.Fatalf("a superseded connection's late caps overwrote the live one: %+v", snap)
	}
	// The admission limit of the LIVE conn must be untouched by the stale update too:
	// 5 slots (from B's caps), not 99 (the stale frame).
	for i := 0; i < 5; i++ {
		if !connB.tryReserve(fmt.Sprintf("j%d", i)) {
			t.Fatalf("live conn rejected job %d, but its caps allow 5 concurrent", i)
		}
	}
	if connB.tryReserve("j5") {
		t.Fatal("live conn admitted a 6th job: the stale conn's max_concurrent=99 leaked into it")
	}
}

// TestUpdateCapsMovesTheRealAdmissionLimit is the "fake max_concurrent" regression.
// wc.meta.MaxConcurrent is the value we DISPLAY; wc.maxConcurrent is the value
// tryReserve ADMITS against. A reload that writes only the first would show the new
// limit in the UI while the hub kept enforcing the old one — so this test asserts the
// limit through tryReserve, never through the snapshot.
func TestUpdateCapsMovesTheRealAdmissionLimit(t *testing.T) {
	r := newRegistry()
	wc := newWorkerConn("w1", "w1", nil, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion, MaxConcurrent: 1,
	})
	r.Put(wc)

	// Register-time limit of 1: the second job is refused.
	if !wc.tryReserve("j1") {
		t.Fatal("first job refused at max_concurrent=1")
	}
	if wc.tryReserve("j2") {
		t.Fatal("second job admitted at max_concurrent=1: the register-time limit is not enforced")
	}

	// Scale UP via reload → the admission limit must follow immediately.
	r.UpdateCaps(wc, capsFor("up", 3))
	if !wc.tryReserve("j2") || !wc.tryReserve("j3") {
		t.Fatal("reload raised max_concurrent to 3 but tryReserve still enforces the old limit")
	}
	if wc.tryReserve("j4") {
		t.Fatal("a 4th job was admitted at max_concurrent=3")
	}

	// Scale DOWN below the current in-flight count (3 running, new cap 1). Documented
	// behaviour: already-reserved slots are NOT reclaimed — those jobs are running on
	// the worker and killing them would be worse than briefly exceeding the cap. The
	// new limit binds ADMISSION only: nothing more is admitted until the in-flight
	// count falls back under it. It must not panic, and the counter must not drift.
	r.UpdateCaps(wc, capsFor("down", 1))
	if wc.tryReserve("j5") {
		t.Fatal("a new job was admitted while 3 are in flight and the cap is now 1")
	}
	if got := wc.snapshot().InFlight; got != 3 {
		t.Fatalf("in-flight = %d after a shrink, want the 3 running jobs kept (no forced eviction)", got)
	}
	wc.release("j1")
	wc.release("j2")
	if wc.tryReserve("j5") { // still 1 in flight, cap 1 → full
		t.Fatal("admitted a job while in-flight(1) >= cap(1)")
	}
	wc.release("j3")
	if !wc.tryReserve("j5") { // 0 in flight, cap 1 → room again
		t.Fatal("after the in-flight count drained, the new cap must admit again")
	}
}

// TestUpdateCapsZeroMaxConcKeepsTheLimit: MaxConc=0 means "not reported", and must NOT
// be applied — 0 is also the encoding for "no hub-side cap", so treating an absent
// value as a real one would silently UNCAP the worker on every reload that omits it.
func TestUpdateCapsZeroMaxConcKeepsTheLimit(t *testing.T) {
	r := newRegistry()
	wc := newWorkerConn("w1", "w1", nil, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion, MaxConcurrent: 1,
	})
	r.Put(wc)

	r.UpdateCaps(wc, capsFor("nomax", 0))
	if !wc.tryReserve("j1") {
		t.Fatal("first job refused")
	}
	if wc.tryReserve("j2") {
		t.Fatal("max_concurrent=0 in a caps frame uncapped the worker: it must mean 'unchanged'")
	}
	// …while the rest of the snapshot still applied.
	if snap, _ := r.WorkerSnapshot("w1"); snap.Projects[0] != "proj-nomax" {
		t.Fatalf("the non-concurrency fields must still apply, got %+v", snap)
	}
}

// TestUpdateCapsConcurrentWithSnapshot is the -race proof (acceptance 9). Before this
// task, WorkerSnapshot read wc.meta AFTER releasing the registry lock and under no
// other lock — safe only while meta was immutable-after-register. The moment a reload
// can rewrite meta on a live connection, that read is a data race, and `-race` says so.
// Readers must therefore share the writer's lock (wc.mu), which is what this exercises:
// caps updates, snapshot reads and admission checks all hammering one conn at once.
func TestUpdateCapsConcurrentWithSnapshot(t *testing.T) {
	r := newRegistry()
	wc := newWorkerConn("w1", "w1", nil, wsproto.Register{
		WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion, MaxConcurrent: 2,
	})
	r.Put(wc)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ { // writers: caps re-reports (reload receipts / SIGHUP broadcasts)
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for round := 0; ; round++ {
				select {
				case <-stop:
					return
				default:
				}
				r.UpdateCaps(wc, capsFor(fmt.Sprintf("%d-%d", n, round), 1+round%4))
			}
		}(i)
	}
	for i := 0; i < 4; i++ { // readers: the /v1/runners + validation surface
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap, ok := r.WorkerSnapshot("w1")
				if !ok {
					continue
				}
				// Touch the slice CONTENTS, not just the headers: the read must be a real
				// read of the memory the writer is rewriting.
				for _, p := range snap.Projects {
					_ = len(p)
				}
				for _, a := range snap.AgentCaps {
					_ = a.Key
				}
				_ = snap.InFlight
			}
		}()
	}
	wg.Add(1)
	go func() { // the admission path reads the field UpdateCaps writes
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			id := fmt.Sprintf("j%d", i)
			wc.tryReserve(id)
			wc.release(id)
		}
	}()

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}
