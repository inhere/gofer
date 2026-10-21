package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestDispatchEnvReachesJobUnderGoferKeys pins design 一·4 on the worker side: the job env
// the hub resolved rides dispatch.env and is applied to this machine's own Submit, layered
// with G042's EnvWith — the step's variables are added, gofer's metadata keys stay on top
// (a step env must not redirect the job's server address or rename its worker).
func TestDispatchEnvReachesJobUnderGoferKeys(t *testing.T) {
	jobs := &stubJobs{
		submitID:   "local-1",
		getResult:  job.JobResult{Status: job.StatusDone},
		getOK:      true,
		waitResult: job.JobResult{Status: job.StatusDone},
		waitOK:     true,
	}
	cl, _, url := dialLiveClient(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cl.handleDispatch(ctx, url, wsproto.Dispatch{JobID: "d1", Env: map[string]string{
		"GOOS":            "darwin",
		job.EnvServerAddr: "http://evil.example",
	}})

	jobs.mu.Lock()
	env := jobs.gotEnv
	jobs.mu.Unlock()
	if env == nil {
		t.Fatal("the worker submitted the job with no env at all")
	}
	if env["GOOS"] != "darwin" {
		t.Fatalf("submit env GOOS = %q, want the dispatch's darwin", env["GOOS"])
	}
	if strings.HasPrefix(env[job.EnvServerAddr], "http://evil") {
		t.Fatalf("submit env %s = %q: a step env must not override gofer's own key", job.EnvServerAddr, env[job.EnvServerAddr])
	}
	if env[job.EnvWorkerID] != "w1" {
		t.Fatalf("submit env %s = %q, want w1", job.EnvWorkerID, env[job.EnvWorkerID])
	}
}
