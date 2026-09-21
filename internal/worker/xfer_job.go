package worker

import (
	"context"
	"errors"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
	"github.com/inhere/gofer/internal/xfer"
)

// xfer_job.go is XFER-01 X2's worker side of the job file seam. A worker executes a
// dispatched job locally, so it carries out that job's file steps in ITS cwd.
//
// It is the ONE place in this package that links internal/xfer in, and only for the two
// definitions that must not drift: the content endpoint's path (ContentPath) and the
// limit shape the caller resolves from this worker's own config. The wire-level
// transfer code in xfer.go keeps its own literals on purpose (see errXferExists).
//
//   - FetchUpload downloads a staged upload from the hub's content endpoint with this
//     worker's own token (the hub only serves an id assigned to the caller, which is
//     what keeps a transfer id from being a capability). The destination was resolved
//     by the job service with project.SafeJoin against THIS machine's project root,
//     so the boundary is enforced here exactly like a transfer's path.
//   - Collect is REPORTED, not pushed: the hub owns the job row's result dir, so the
//     job service records what it matched and the hub pulls the bytes back
//     (job.CollectedPuller). OwnsArtifacts is false for that reason, and
//     PullCollected is never called on a worker (it returns an error if it ever is).
type jobXfer struct {
	cl     *Client
	limits job.CollectLimits
}

// JobXferBridge builds the worker's job file seam over a connected client. limits are
// this worker's own caps (its config's server.xfer), resolved by the command from the
// same core.Build the job service came from.
func JobXferBridge(cl *Client, lim xfer.Limits) job.XferBridge {
	return jobXfer{cl: cl, limits: job.CollectLimits{MaxFile: lim.MaxBytes, MaxTotal: lim.CollectMaxBytes}}
}

// FetchUpload streams the staged transfer onto this machine at dst.
func (x jobXfer) FetchUpload(ctx context.Context, xferID, _, dst string) error {
	base := x.cl.hubBase()
	if base == "" {
		return errors.New("worker has no live hub connection")
	}
	contentURL, err := xferContentURL(base, xfer.ContentPath(xferID))
	if err != nil {
		return err
	}
	// The transfer's own deadline (worker.xfer_timeout_sec) bounds this, not the
	// job's: a hung hub must fail the upload instead of eating the job's whole budget.
	tctx, cancel := context.WithTimeout(ctx, x.cl.effectiveXferTimeout())
	defer cancel()
	// Size/sha256 are unknown to the job request (it carries only the id), so the
	// fetch verifies against what the hub ANNOUNCES in the response headers — which is
	// the digest the staging area settled the payload with.
	_, _, err = x.cl.xferFetch(tctx, contentURL, wsproto.FileXfer{XferID: xferID, Op: "put"}, dst)
	return err
}

// PullCollected is a hub-side capability: a worker does not own the job's artifact
// directory, so it never pulls. The job package only asks a bridge that reports
// OwnsArtifacts, and this one reports false.
func (x jobXfer) PullCollected(context.Context, job.CollectedPull) (int64, error) {
	return 0, errors.New("worker: a collected file is pulled by the hub")
}

// CollectLimits are this worker's caps: the job service filters what it reports with
// them (an over-cap file is listed as skipped instead of being announced as a
// transfer the hub would then refuse).
func (x jobXfer) CollectLimits() job.CollectLimits { return x.limits }

// OwnsArtifacts is false: the job row lives on the hub, so its result dir does too.
func (x jobXfer) OwnsArtifacts() bool { return false }

// hubBase returns the HTTP base of the hub this worker is currently connected to, or
// "" when no session is up.
func (cl *Client) hubBase() string {
	cl.baseMu.RLock()
	defer cl.baseMu.RUnlock()
	return cl.hubBaseURL
}

// setHubBase records (or clears) the current session's hub address. It is called by
// runSession, so every session's fetches use the address that session registered on.
func (cl *Client) setHubBase(base string) {
	cl.baseMu.Lock()
	cl.hubBaseURL = base
	cl.baseMu.Unlock()
}
