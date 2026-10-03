package wshub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

var ErrUpgradeUnsupported = errors.New("worker protocol too old for remote upgrade")
var ErrUpgradeTimeout = errors.New("worker did not answer the upgrade request in time")

// ErrWorkerDraining is returned by Dispatch while the worker is being upgraded: it
// finishes its in-flight jobs but takes no new ones.
var ErrWorkerDraining = errors.New("worker is being upgraded and takes no new jobs; retry shortly")

// UpgradeObserver receives the upgrade events that happen outside the request/answer
// of UpgradeWorker: a worker's later failure/rollback report, and the registration of
// the replacement process (the success signal of a handover).
type UpgradeObserver interface {
	UpgradeReported(workerID string, r wsproto.UpgradeResult)
	UpgradeRegistered(workerID, upgradeID, version string)
}

// SetUpgradeObserver wires the upgrade bookkeeping. Call before the hub accepts
// connections.
func (h *Hub) SetUpgradeObserver(o UpgradeObserver) { h.upgradeObs = o }

// drainGrace is slack added to the drain + ready budgets before the hub stops
// refusing dispatches on its own: a worker that never reports back must not stay
// unschedulable forever.
const drainGrace = 2 * time.Minute

// UpgradeWorker asks a connected worker to upgrade itself and waits for its FIRST
// answer: "accepted" (binary downloaded, verified and runnable; the handover now
// proceeds on its own, nil error, the version it reported is returned) or a failure.
// The final outcome arrives later through the UpgradeObserver (the new process
// registering with Register.UpgradeID, or a rollback/failure report).
//
// The worker is marked draining BEFORE the frame is written, so no new job can slip
// in between the request and the worker noticing it; a failed request lifts the mark.
func (h *Hub) UpgradeWorker(ctx context.Context, workerID string, req wsproto.Upgrade) (string, error) {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return "", ErrWorkerOffline
	}
	if !wc.supportsUpgrade() {
		return "", fmt.Errorf("%w: worker %s speaks protocol v%d, upgrade needs v%d (该 worker 版本过旧，需要手动升级一次)", ErrUpgradeUnsupported, workerID, wc.protocolVersion(), wsproto.UpgradeMinProtocolVersion)
	}
	if req.RequestID == "" {
		req.RequestID = upgradeRequestID()
	}
	drain := time.Duration(req.DrainTimeoutSec) * time.Second
	if drain <= 0 {
		drain = time.Duration(wsproto.DefaultUpgradeDrainSec) * time.Second
	}
	ready := time.Duration(req.ReadyTimeoutSec) * time.Second
	if ready <= 0 {
		ready = time.Duration(wsproto.DefaultUpgradeReadySec) * time.Second
	}
	ch := wc.registerUpgrade(req.RequestID)
	defer wc.deleteUpgrade(req.RequestID)
	wc.markDraining(drain + ready + drainGrace)
	if err := wc.writeFrame(ctx, wsproto.TypeUpgrade, "", req); err != nil {
		wc.clearDraining()
		return "", err
	}
	select {
	case result := <-ch:
		if !result.OK {
			wc.clearDraining()
			return "", errors.New(result.Error)
		}
		return result.Version, nil
	case <-wc.done:
		return "", ErrWorkerOffline
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ErrUpgradeTimeout
		}
		return "", ctx.Err()
	}
}

// markDraining makes the connection refuse new dispatches for at most d.
func (wc *workerConn) markDraining(d time.Duration) {
	wc.drainUntil.Store(time.Now().Add(d).UnixNano())
}

func (wc *workerConn) clearDraining() { wc.drainUntil.Store(0) }

// isDraining reports whether the connection is currently refusing new jobs.
func (wc *workerConn) isDraining() bool {
	until := wc.drainUntil.Load()
	return until != 0 && time.Now().UnixNano() < until
}

func (wc *workerConn) registerUpgrade(id string) chan wsproto.UpgradeResult {
	ch := make(chan wsproto.UpgradeResult, 1)
	wc.mu.Lock()
	if wc.pendingUpgrade == nil {
		wc.pendingUpgrade = map[string]chan wsproto.UpgradeResult{}
	}
	wc.pendingUpgrade[id] = ch
	wc.mu.Unlock()
	return ch
}

func (wc *workerConn) deleteUpgrade(id string) {
	wc.mu.Lock()
	delete(wc.pendingUpgrade, id)
	wc.mu.Unlock()
}

// resolveUpgrade hands the result to the UpgradeWorker call parked on it and reports
// whether there was one; a result nobody waits for (a later rollback/failure report)
// is the caller's to route elsewhere.
func (wc *workerConn) resolveUpgrade(result wsproto.UpgradeResult) bool {
	wc.mu.Lock()
	ch := wc.pendingUpgrade[result.RequestID]
	delete(wc.pendingUpgrade, result.RequestID)
	wc.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- result:
	default:
	}
	return true
}

func upgradeRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
