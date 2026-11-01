package jobstore

import (
	"context"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// A writer that does not go through writeMu (previews, rules, ...) may hold SQLite's write
// lock when a handoff write begins. The handoff transaction reads before it writes; if it
// started deferred, upgrading to a write lock failed at once with "database is locked"
// (SQLite never calls the busy handler for a read-to-write upgrade), which the HTTP layer
// answered with a 500. A write transaction must wait for the lock like any other writer.
func TestSetPlanHandoffWaitsForForeignWriter(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	conn, err := s.db.Conn(ctx)
	assert.NoErr(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	assert.NoErr(t, err)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = conn.ExecContext(ctx, `COMMIT`)
	}()

	h, err := s.SetPlanHandoff("plan-busy", "body", "alice", 0)
	assert.NoErr(t, err)
	assert.Eq(t, 1, h.Version)
}
