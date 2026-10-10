package serve

import (
	"time"

	"github.com/gookit/gcli/v3"
)

// holdSweepInterval is the hold-expiry sweep cadence (gofer-9b1b). A hold lives for
// hours by default, so half a minute of lateness is invisible, while an idle tick is
// one indexed query on the rows still awaiting approval.
const holdSweepInterval = 30 * time.Second

// holdSweeper is the slice of the job service the expiry loop drives.
type holdSweeper interface {
	SweepExpiredHolds(now int64) (int, error)
}

// startHoldExpiryLoop launches the hold-expiry sweeper (gofer-9b1b, design §超时): it
// cancels every held job whose deadline passed. It sweeps once at startup — a hold that
// expired while serve was down ends on boot — and then on every tick, mirroring
// startRetryLoop. Deadlines live in the store (hold_expires_at), so nothing is lost
// across a restart. The goroutine exits when stop closes (serve shutdown).
func startHoldExpiryLoop(c *gcli.Command, jobs holdSweeper, stop <-chan struct{}) {
	runHoldExpiryLoop(jobs, holdSweepInterval, time.Now, func(n int, err error) {
		if err != nil {
			c.Errorf("gofer: hold expiry sweep failed: %v\n", err)
			return
		}
		if n > 0 {
			c.Printf("gofer: hold expiry sweep cancelled %d expired hold(s)\n", n)
		}
	}, stop)
}

// runHoldExpiryLoop is startHoldExpiryLoop with its clock, cadence and reporting
// injected; the returned channel closes once the goroutine has exited.
func runHoldExpiryLoop(jobs holdSweeper, interval time.Duration, now func() time.Time, report func(int, error), stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweep := func() { report(jobs.SweepExpiredHolds(now().Unix())) }
		sweep() // startup: end the holds that expired while serve was down
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
	return done
}
