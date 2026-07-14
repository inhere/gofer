package wshub

import (
	"testing"

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
