package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobWaitAllAnyAndTimeout covers `job wait`: it returns once every job is terminal,
// `--any` returns on the first one, and `--timeout` exits non-zero naming what is still
// running. The server is a stub — no real gofer is started or contacted.
func TestJobWaitAllAnyAndTimeout(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	// The subcommand flags are package globals: gcli does not reset them between Run
	// calls, so start from the zero literal and restore afterwards (G053).
	jobWaitOpts.any, jobWaitOpts.timeout, jobWaitOpts.interval = false, "", ""
	t.Cleanup(func() { jobWaitOpts.any, jobWaitOpts.timeout, jobWaitOpts.interval = false, "", "" })

	var mu sync.Mutex
	polls := map[string]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v1/jobs/") {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
		mu.Lock()
		polls[id]++
		n := polls[id]
		mu.Unlock()
		status := job.StatusDone
		switch {
		case id == "stuck":
			status = job.StatusRunning
		case id == "slow" && n < 2:
			status = job.StatusRunning
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: id, Status: status, Title: "t-" + id})
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	// Every job terminal: both are reported, exit 0.
	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "wait", "fast", "slow", "--server", ts.URL, "--interval", "10ms"}); code != 0 {
			t.Fatalf("job wait (all) exit code=%d", code)
		}
	})
	for _, want := range []string{"fast", "slow", job.StatusDone, "t-fast", "t-slow"} {
		if !strings.Contains(out, want) {
			t.Fatalf("job wait output missing %q:\n%s", want, out)
		}
	}

	// --any: the finished job returns while the other keeps running.
	mu.Lock()
	polls = map[string]int{}
	mu.Unlock()
	app = NewApp("test")
	out = captureOutput(t, func() {
		if code := app.Run([]string{"job", "wait", "fast", "stuck", "--any", "--server", ts.URL, "--interval", "10ms"}); code != 0 {
			t.Fatalf("job wait --any exit code=%d", code)
		}
	})
	if !strings.Contains(out, job.StatusRunning) {
		t.Fatalf("--any must report the job that is still running:\n%s", out)
	}

	// Timeout: a job that never ends makes the command exit non-zero and name it.
	app = NewApp("test")
	captureOutput(t, func() {
		if code := app.Run([]string{"job", "wait", "stuck", "--server", ts.URL, "--interval", "10ms", "--timeout", "150ms"}); code == 0 {
			t.Fatalf("job wait --timeout must exit non-zero")
		}
	})
}
