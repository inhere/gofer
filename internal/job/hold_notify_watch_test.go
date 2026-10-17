package job

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestParkedJobIsWatchedByItsNotifyTarget (gofer-fxbu): a held job registers the notify
// target's watch when it is parked, not only once it is approved, so the session that
// submitted it is waiting on it for the whole time it waits for a decision. A held job
// without a notify target leaves no watch behind.
func TestParkedJobIsWatchedByItsNotifyTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	svc := newHoldService(t, root, nil)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "hold-notify-target", Agent: "claude", ProjectKey: "other-project",
		Runner: "remote-box", Cwd: root, CallerID: "caller-a",
	})
	assert.NoErr(t, err)

	req := probeRequest("go", root+"/probe")
	req.Cmd = []string{"go", "version"}
	req.CallerID = "caller-a"
	req.Notify, req.NotifySession = true, "hold-notify-target"
	res, err := svc.Submit(req)
	assert.NoErr(t, err)
	assert.Eq(t, StatusAwaitingApproval, res.Status)

	watches, err := svc.meta.ListSessionJobWatches("hold-notify-target")
	assert.NoErr(t, err)
	if len(watches) != 1 || watches[0].JobID != res.ID {
		t.Fatalf("watches while parked = %+v, want the held job %s", watches, res.ID)
	}

	quiet := probeRequest("go", root+"/probe")
	quiet.Cmd = []string{"go", "version"}
	quiet.CallerID = "caller-a"
	res2, err := svc.Submit(quiet)
	assert.NoErr(t, err)
	watches, err = svc.meta.ListSessionJobWatches("hold-notify-target")
	assert.NoErr(t, err)
	if len(watches) != 1 || watches[0].JobID == res2.ID {
		t.Fatalf("a held job without a notify target left a watch: %+v", watches)
	}
}
