package xfer

import (
	"context"
	"fmt"
	"time"
)

// put_wait.go is the one-call placement seam of the file-delivery path (the gofer
// console's image attachments): a caller that holds a local file and knows WHERE on the
// executing machine it has to end up asks for the transfer and gets back the absolute
// path that machine reported — without touching the journal, the wire or the limits.
//
// It is deliberately NOT an HTTP handler: delivery needs a transfer, not a round trip
// through gofer's own API, and the xfer business layer is where a transfer is driven
// (G021/G022).

// DefaultPutWaitTimeout bounds PutAndWait. The far side is bounded by its own
// worker.xfer_timeout_sec (10 minutes by default), so a SHORTER wait is a caller-side
// budget, not a transfer deadline: it is what keeps one slow machine from stalling a
// message the user is waiting on.
const DefaultPutWaitTimeout = 60 * time.Second

// PutAndWait stages srcPath as a put to <runner>:<project>/<dest>, executes it and waits
// for it to settle. It returns the ABSOLUTE path the executing machine reported for the
// destination — empty when that machine cannot report one (a worker below
// wsproto.XferAbsPathMinProtocolVersion) — or the transfer's own failure reason (worker
// offline, path escapes project, exists, too large…).
//
// The staging overwrites an existing destination: a re-delivery of the same attachment is
// then idempotent instead of failing on the copy the previous attempt left. timeout <= 0
// means DefaultPutWaitTimeout; on a timeout the record is settled FAILED, so a stalled far
// side can never leave it dispatched forever.
func (m *Manager) PutAndWait(ctx context.Context, caller, runner, projectKey, srcPath, dest string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = DefaultPutWaitTimeout
	}
	rec, err := m.StagePutFromFile(caller, runner, projectKey, srcPath, dest, true)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Deliver is synchronous here on purpose: this caller has nothing to do until the
	// bytes are in place, and the context above is what bounds the wait.
	if err := m.Deliver(ctx, rec.ID); err != nil {
		return "", err
	}
	settled, ok, err := m.repo.GetXfer(rec.ID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotFound
	}
	if State(settled.State) != StateDone {
		if cerr := ctx.Err(); cerr != nil {
			return "", fmt.Errorf("xfer: put %s timed out after %s: %s", rec.ID, timeout, settled.Error)
		}
		if settled.Error == "" {
			return "", fmt.Errorf("xfer: put %s ended %s", rec.ID, settled.State)
		}
		return "", fmt.Errorf("xfer: put %s failed: %s", rec.ID, settled.Error)
	}
	return settled.AbsPath, nil
}
