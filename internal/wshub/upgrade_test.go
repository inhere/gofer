package wshub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

type upgradeObs struct {
	mu         sync.Mutex
	registered []string
	reported   []wsproto.UpgradeResult
}

func (o *upgradeObs) UpgradeRegistered(workerID, upgradeID, version string) {
	o.mu.Lock()
	o.registered = append(o.registered, workerID+"/"+upgradeID+"/"+version)
	o.mu.Unlock()
}

func (o *upgradeObs) UpgradeReported(_ string, r wsproto.UpgradeResult) {
	o.mu.Lock()
	o.reported = append(o.reported, r)
	o.mu.Unlock()
}

func (o *upgradeObs) snapshot() ([]string, []wsproto.UpgradeResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.registered...), append([]wsproto.UpgradeResult(nil), o.reported...)
}

func readUpgradeFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) wsproto.Upgrade {
	t.Helper()
	for {
		env, err := readEnvelope(ctx, conn)
		if err != nil {
			t.Fatalf("worker never received the upgrade frame: %v", err)
		}
		if env.Type != wsproto.TypeUpgrade {
			continue
		}
		up, err := wsproto.As[wsproto.Upgrade](env)
		if err != nil {
			t.Fatal(err)
		}
		return up
	}
}

func sendUpgradeResult(t *testing.T, ctx context.Context, conn *websocket.Conn, r wsproto.UpgradeResult) {
	t.Helper()
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeUpgradeResult, Payload: mustRaw(r)}); err != nil {
		t.Fatalf("write upgrade result: %v", err)
	}
}

func TestWorkerUpgradeRejectsOldProtocol(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, reg := dialAndRegisterProto(t, ctx, wsURL, "w1", wsproto.UpgradeMinProtocolVersion-1)
	if !reg.Accepted {
		t.Fatalf("a v%d worker must still register: %+v", wsproto.UpgradeMinProtocolVersion-1, reg)
	}
	waitWorkerOnline(t, hub, "w1")
	_, err := hub.UpgradeWorker(ctx, "w1", wsproto.Upgrade{SHA256: "abc", Size: 1, URLPath: "/x"})
	if !errors.Is(err, ErrUpgradeUnsupported) {
		t.Fatalf("err = %v, want ErrUpgradeUnsupported", err)
	}
	snap, _ := hub.WorkerSnapshot("w1")
	if snap.Draining {
		t.Fatal("a refused upgrade must not leave the worker draining")
	}
}

func TestWorkerUpgradeHandoverKeepsWorkerOnline(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	obs := &upgradeObs{}
	hub.SetUpgradeObserver(obs)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	oldConn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "old", ProtocolVersion: wsproto.CurrentProtocolVersion, GoferVersion: "v1",
	})
	waitWorkerOnline(t, hub, "w1")

	type reply struct {
		version string
		err     error
	}
	done := make(chan reply, 1)
	go func() {
		v, err := hub.UpgradeWorker(ctx, "w1", wsproto.Upgrade{RequestID: "up1", SHA256: "abc", Size: 3, URLPath: "/v1/workers/w1/upgrade/file"})
		done <- reply{v, err}
	}()
	up := readUpgradeFrame(t, ctx, oldConn)
	if up.RequestID != "up1" {
		t.Fatalf("upgrade frame = %+v", up)
	}
	sendUpgradeResult(t, ctx, oldConn, wsproto.UpgradeResult{RequestID: "up1", OK: true, Phase: wsproto.UpgradePhaseAccepted, Version: "v2"})
	if r := <-done; r.err != nil || r.version != "v2" {
		t.Fatalf("UpgradeWorker = %+v", r)
	}

	// While the old process drains the hub takes no new jobs for it.
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j1"}); !errors.Is(err, ErrWorkerDraining) {
		t.Fatalf("dispatch to a draining worker = %v, want ErrWorkerDraining", err)
	}
	if snap, _ := hub.WorkerSnapshot("w1"); !snap.Draining {
		t.Fatal("snapshot must report draining")
	}

	// The replacement process (new instance id, upgrade id) registers while the old
	// connection is still open: it takes over and the worker never goes offline.
	stop := make(chan struct{})
	offline := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				if !hub.IsOnline("w1") {
					select {
					case offline <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	newConn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{
		WorkerID: "w1", InstanceID: "new", ProtocolVersion: wsproto.CurrentProtocolVersion, GoferVersion: "v2", UpgradeID: "up1",
	})
	_ = newConn
	// The old connection is closed by the hub ("replaced").
	if _, _, err := oldConn.Read(ctx); err == nil {
		t.Fatal("old connection should have been closed by the takeover")
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	select {
	case <-offline:
		t.Fatal("worker went offline during the handover")
	default:
	}

	reg, _ := obs.snapshot()
	if len(reg) != 1 || reg[0] != "w1/up1/v2" {
		t.Fatalf("registered events = %v", reg)
	}
	if snap, ok := hub.WorkerSnapshot("w1"); !ok || snap.Draining || snap.GoferVersion != "v2" {
		t.Fatalf("snapshot after handover = %+v ok=%v", snap, ok)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j2"}); err != nil {
		t.Fatalf("new process must take jobs: %v", err)
	}
}

func TestWorkerUpgradeLateFailureLiftsDrain(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	obs := &upgradeObs{}
	hub.SetUpgradeObserver(obs)
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{WorkerID: "w1", InstanceID: "a", ProtocolVersion: wsproto.CurrentProtocolVersion})
	waitWorkerOnline(t, hub, "w1")

	done := make(chan error, 1)
	go func() {
		_, err := hub.UpgradeWorker(ctx, "w1", wsproto.Upgrade{RequestID: "up2", SHA256: "abc", Size: 3, URLPath: "/x"})
		done <- err
	}()
	readUpgradeFrame(t, ctx, conn)
	sendUpgradeResult(t, ctx, conn, wsproto.UpgradeResult{RequestID: "up2", OK: true, Phase: wsproto.UpgradePhaseAccepted})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sendUpgradeResult(t, ctx, conn, wsproto.UpgradeResult{RequestID: "up2", Phase: wsproto.UpgradePhaseRolledBack, Error: "new process never registered"})

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, rep := obs.snapshot()
		snap, _ := hub.WorkerSnapshot("w1")
		if len(rep) == 1 && !snap.Draining {
			if rep[0].Phase != wsproto.UpgradePhaseRolledBack {
				t.Fatalf("report = %+v", rep[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rollback report not observed: reports=%v draining=%v", rep, snap.Draining)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := hub.Dispatch("w1", wsproto.Dispatch{JobID: "j3"}); err != nil {
		t.Fatalf("worker should take jobs again after a rollback: %v", err)
	}
}

func TestWorkerUpgradeRejectionLiftsDrain(t *testing.T) {
	hub := New(map[string]string{"w1": "w1"})
	_, wsURL := hubServer(t, hub, "w1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialAndRegisterFull(t, ctx, wsURL, wsproto.Register{WorkerID: "w1", InstanceID: "a", ProtocolVersion: wsproto.CurrentProtocolVersion})
	waitWorkerOnline(t, hub, "w1")
	done := make(chan error, 1)
	go func() {
		_, err := hub.UpgradeWorker(ctx, "w1", wsproto.Upgrade{RequestID: "up3", SHA256: "abc", Size: 3, URLPath: "/x"})
		done <- err
	}()
	readUpgradeFrame(t, ctx, conn)
	sendUpgradeResult(t, ctx, conn, wsproto.UpgradeResult{RequestID: "up3", Phase: wsproto.UpgradePhaseFailed, Error: "checksum mismatch"})
	if err := <-done; err == nil || err.Error() != "checksum mismatch" {
		t.Fatalf("err = %v", err)
	}
	if snap, _ := hub.WorkerSnapshot("w1"); snap.Draining {
		t.Fatal("a rejected upgrade must lift the drain mark")
	}
}
