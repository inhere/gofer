package wshub

import (
	"context"
	"errors"
	"fmt"

	"github.com/inhere/gofer/internal/wsproto"
)

// ErrTranscriptTailUnsupported is returned when the worker is connected but speaks a
// protocol older than the transcript_tail frames. It is not a fault of the worker; the
// caller degrades (the summarizer falls back to the session's last message).
var ErrTranscriptTailUnsupported = errors.New("worker protocol too old for transcript tail")

// SendTranscriptTail asks a worker for the end of a session transcript and waits for
// the answer. The wait ends on exactly one of: the matching result, the connection
// dying, or ctx expiring; the pending entry is removed on all of them.
func (h *Hub) SendTranscriptTail(ctx context.Context, workerID string, req wsproto.TranscriptTail) (wsproto.TranscriptTailResult, error) {
	if req.ReqID == "" {
		return wsproto.TranscriptTailResult{}, errors.New("transcript_tail requires a req_id")
	}
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return wsproto.TranscriptTailResult{}, ErrWorkerOffline
	}
	if !wsproto.SupportsTranscriptTail(wc.protocolVersion()) {
		return wsproto.TranscriptTailResult{}, fmt.Errorf("%w: worker %s speaks protocol v%d, transcript tail needs v%d",
			ErrTranscriptTailUnsupported, workerID, wc.protocolVersion(), wsproto.TranscriptTailMinProtocolVersion)
	}
	ch := wc.registerTranscriptTail(req.ReqID)
	defer wc.deleteTranscriptTail(req.ReqID)
	if err := wc.writeFrame(ctx, wsproto.TypeTranscriptTail, "", req); err != nil {
		return wsproto.TranscriptTailResult{}, fmt.Errorf("send transcript_tail frame to worker %s: %w", workerID, err)
	}
	select {
	case res := <-ch:
		return res, nil
	case <-wc.done:
		select {
		case res := <-ch:
			return res, nil
		default:
		}
		return wsproto.TranscriptTailResult{}, ErrWorkerOffline
	case <-ctx.Done():
		select {
		case res := <-ch:
			return res, nil
		default:
		}
		return wsproto.TranscriptTailResult{}, ctx.Err()
	}
}

// TranscriptTailSupported reports whether the worker is live and speaks the frame.
func (h *Hub) TranscriptTailSupported(workerID string) (supported, online bool) {
	wc, ok := h.reg.Get(workerID)
	if !ok {
		return false, false
	}
	return wsproto.SupportsTranscriptTail(wc.protocolVersion()), true
}

func (wc *workerConn) registerTranscriptTail(reqID string) chan wsproto.TranscriptTailResult {
	ch := make(chan wsproto.TranscriptTailResult, 1)
	wc.mu.Lock()
	if wc.pendingTail == nil {
		wc.pendingTail = map[string]chan wsproto.TranscriptTailResult{}
	}
	wc.pendingTail[reqID] = ch
	wc.mu.Unlock()
	return ch
}

func (wc *workerConn) deleteTranscriptTail(reqID string) {
	wc.mu.Lock()
	delete(wc.pendingTail, reqID)
	wc.mu.Unlock()
}

func (wc *workerConn) resolveTranscriptTail(res wsproto.TranscriptTailResult) bool {
	wc.mu.Lock()
	ch := wc.pendingTail[res.ReqID]
	delete(wc.pendingTail, res.ReqID)
	wc.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- res:
	default:
	}
	return true
}

func (wc *workerConn) revokeTranscriptTails() {
	wc.mu.Lock()
	wc.pendingTail = nil
	wc.mu.Unlock()
}

func (wc *workerConn) pendingTranscriptTails() int {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return len(wc.pendingTail)
}
