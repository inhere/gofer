package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// TestE2EWorkerSessionKnowsItCanBeReloaded: the local row of a session a worker runs learns,
// from the agent's initialize, that the agent can load the session again; that is what lets an
// upgrade stop waiting for it while it idles. A session whose agent does not advertise it is
// not reloadable.
func TestE2EWorkerSessionKnowsItCanBeReloaded(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts acptest.Options
		want bool
	}{
		{"agent advertises loadSession", acptest.Options{}, true},
		{"agent refuses to load", acptest.Options{RefuseLoad: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := buildHubSideSel(t, t.TempDir(), t.TempDir(), func(h *wshub.Hub) job.WorkerSelector { return sessionSelector{hub: h} })
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			cli, localJobs := buildWorkerWithACPAgent(t, hub.ts.URL, nil, tc.opts)
			clientErr := worker.StartClient(t, ctx, cli)
			defer stopWorkerClient(t, cli, cancel, clientErr)
			waitWorkerOnline(t, hub.hub)
			sess, err := hub.jobs.Submit(job.JobRequest{
				ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
				Prompt: "first turn", Cwd: ".", Session: true, Hosted: true, TimeoutSec: 120,
			})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			waitHostAwaitingTurn(t, hub.jobs, sess.ID, 1)
			wait.For(t, 20*time.Second, "worker session reloadable verdict", func() (bool, any) {
				list, _ := localJobs.ListJobs(job.ListOpts{Limit: 10})
				for _, r := range list {
					if r.Session && r.SessionID != "" {
						cur, _ := localJobs.Get(r.ID)
						return localJobs.SessionReloadable(cur) == tc.want, localJobs.SessionReloadable(cur)
					}
				}
				return false, "no session row"
			})
			postHub(t, hub.ts, "/v1/jobs/"+sess.ID+"/end", map[string]string{})
			hub.jobs.Wait(sess.ID)
		})
	}
}
