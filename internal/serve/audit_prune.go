package serve

import (
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/jobstore"
)

// auditPruner is the store seam of the audit retention sweep.
type auditPruner interface {
	PruneAuditBefore(cutoff int64) (admin, auth int64, err error)
}

var _ auditPruner = (*jobstore.Store)(nil)

// pruneAudit deletes the admin_audit and auth_audit rows older than the retention window.
// days <= 0 keeps everything (server.audit_retention_days: negative = forever).
func pruneAudit(c *gcli.Command, st auditPruner, days int, now time.Time) {
	if st == nil || days <= 0 {
		return
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
	admin, auth, err := st.PruneAuditBefore(cutoff)
	if err != nil {
		c.Errorf("gofer: prune audit failed: %v\n", err)
		return
	}
	if admin+auth > 0 {
		c.Printf("gofer: pruned %d admin audit and %d auth audit row(s)\n", admin, auth)
	}
}

// startAuditPruneLoop runs the audit retention sweep once at startup and then every
// interval until stop closes. UNCONDITIONAL, unlike the job retention loop: the audit
// tables always have a retention window (server.audit_retention_days, default 90), so the
// sweep does not wait for storage.retention to be configured. days is read per tick, so a
// hot edit of the window applies to the next sweep.
func startAuditPruneLoop(c *gcli.Command, st auditPruner, days func() int, interval time.Duration, stop <-chan struct{}) {
	if st == nil {
		return
	}
	go func() {
		pruneAudit(c, st, days(), time.Now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				pruneAudit(c, st, days(), time.Now())
			}
		}
	}()
}
