package serve

import (
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// recoveringFailReason is the error every job ended by the RECOV-01 expiry carries.
// "worker lost" is the same cause the hub stamps when a LIVE disconnect's window
// expires (wshub.errWorkerLost), so CLI/web/notification read one vocabulary for
// "the worker never came back".
const recoveringFailReason = "worker lost: worker did not reconnect after the serve restart (recovery window elapsed)"

// startRecoverExpiry arms the ONE-SHOT RECOV-01 expiry for the worker jobs that
// ReconcileOrphanJobs just held in `recovering` at serve startup. It is deliberately
// NOT a periodic sweeper: it is the FALLBACK for whatever the reconnecting worker never
// claims. The hub ADOPTS a held job (R4) as soon as the same worker process reconnects
// and reports it in `inflight` — that rewrites the row to `running`, so this window has
// nothing left to end for it. Whatever is still `recovering` when the window elapses
// was never claimed and is ended with `worker lost`.
//
// window is the RESOLVED server.job_recover_window_sec (config.JobRecoverWindow):
// > 0 arms one bounded timer that expires after it; <= 0 means recovery is DISABLED
// (the pre-RECOV-01 behaviour), so the held rows are failed right away instead of
// being left in `recovering` with no window to end them. The stop channel is closed
// when serve returns: the goroutine then exits WITHOUT failing anything — a serve
// that shuts down inside the window leaves the rows `recovering`, and the next serve
// re-reconciles (and re-arms) them. So does a full adoption: the window is cancelled
// once no held row is left (jobs.AdoptionWake), because it would have nothing to do.
//
// Freeze scope (like every other serve-owned loop's gate): the window is read once
// here, so a SIGHUP changing job_recover_window_sec needs a restart to apply.
func startRecoverExpiry(c *gcli.Command, jobs *job.Service, window time.Duration, stop <-chan struct{}) {
	if window <= 0 {
		// Recovery disabled: no window to hold the jobs under, so end them now —
		// exactly the pre-RECOV-01 outcome (a prior serve's worker jobs are failed).
		n, err := jobs.FailRecoveringJobs(recoveringFailReason)
		reportRecoveringFailed(c, n, err, 0)
		return
	}
	c.Printf("gofer: worker recovery window %s armed for jobs held in `recovering` by a prior serve "+
		"(a reconnecting worker process is ADOPTED; the window only ends what nobody claims)\n", window)
	go func() {
		timer := time.NewTimer(window)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				// serve is going away: leave the rows `recovering` for the next serve.
				return
			case <-timer.C:
				n, err := jobs.FailRecoveringJobs(recoveringFailReason)
				reportRecoveringFailed(c, n, err, window)
				return
			case <-jobs.AdoptionWake():
				// A worker process reclaimed some of the held jobs (RECOV-01 R4). Once
				// NONE are left `recovering` there is nothing the window could still end,
				// so it stops here rather than sitting until the deadline. A partial
				// adoption keeps the ORIGINAL deadline (the window is measured from the
				// serve restart, never extended) for whatever is still unclaimed.
				n, err := jobs.CountRecoveringJobs()
				if err != nil {
					continue
				}
				if n == 0 {
					c.Printf("gofer: every job held in `recovering` by a prior serve was adopted by its worker; recovery window cancelled\n")
					return
				}
			}
		}
	}()
}

// reportRecoveringFailed logs the outcome of an expiry fire (best-effort: a store
// error is reported, never fatal — serve keeps running). after is the window that
// elapsed, or 0 when recovery was disabled and the rows were failed at startup.
func reportRecoveringFailed(c *gcli.Command, n int, err error, after time.Duration) {
	switch {
	case err != nil:
		c.Errorf("gofer: fail recovering jobs failed: %v\n", err)
	case after <= 0:
		if n > 0 {
			c.Printf("gofer: failed %d recovering job(s) left by a prior serve (recovery window disabled)\n", n)
		}
	case n > 0:
		c.Printf("gofer: failed %d job(s) still recovering %s after the serve restart (worker did not reconnect)\n", n, after)
	}
}
