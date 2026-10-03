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

func (h *Hub) UpgradeWorker(ctx context.Context, workerID string, req wsproto.Upgrade) error {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return ErrWorkerOffline
	}
	if !wc.supportsUpgrade() {
		return fmt.Errorf("%w: worker %s speaks protocol v%d, upgrade needs v%d", ErrUpgradeUnsupported, workerID, wc.protocolVersion(), wsproto.UpgradeMinProtocolVersion)
	}
	if req.RequestID == "" {
		req.RequestID = upgradeRequestID()
	}
	ch := wc.registerUpgrade(req.RequestID)
	defer wc.deleteUpgrade(req.RequestID)
	if err := wc.writeFrame(ctx, wsproto.TypeUpgrade, "", req); err != nil {
		return err
	}
	select {
	case result := <-ch:
		if !result.OK {
			return errors.New(result.Error)
		}
		return nil
	case <-wc.done:
		return ErrWorkerOffline
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrUpgradeTimeout
		}
		return ctx.Err()
	}
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

func (wc *workerConn) resolveUpgrade(result wsproto.UpgradeResult) {
	wc.mu.Lock()
	ch := wc.pendingUpgrade[result.RequestID]
	delete(wc.pendingUpgrade, result.RequestID)
	wc.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- result:
	default:
	}
}

func upgradeRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
