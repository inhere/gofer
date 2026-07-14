package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
)

// resetWorkerReloadOpts clears the package-level flag state so the CLI tests do not
// leak into each other.
func resetWorkerReloadOpts(t *testing.T) {
	t.Helper()
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	jobConnOpts.server, jobConnOpts.token = "", ""
	workerReloadOpts.reason, workerReloadOpts.timeout = "", 0
	t.Cleanup(func() {
		config.InputCfgFile = ""
		jobConnOpts.server, jobConnOpts.token = "", ""
		workerReloadOpts.reason, workerReloadOpts.timeout = "", 0
	})
}

// TestWorkerReloadCmdPrintsWorkerReasonVerbatim is the user-visible half of the
// "a rejected reload must say WHY" contract: whatever the worker answered has to
// come out of the CLI unchanged, not flattened into "reload failed".
func TestWorkerReloadCmdPrintsWorkerReasonVerbatim(t *testing.T) {
	resetWorkerReloadOpts(t)
	const workerReason = "load worker config: yaml: line 7: mapping values are not allowed in this context"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workers/w1/reload" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"worker_id": "w1",
			"applied":   false,
			"error":     workerReason,
			"detail":    "worker w1 rejected the config reload and still runs its previous config",
		})
	}))
	defer ts.Close()

	jobConnOpts.server = ts.URL
	cmd := gcli.NewCommand("reload", "", nil)
	cmd.AddArg("id", "worker id", true)
	if err := cmd.ParseArgs([]string{"w1"}); err != nil {
		t.Fatalf("bind arg: %v", err)
	}

	err := runWorkerReload(cmd, nil)
	if err == nil {
		t.Fatal("a rejected reload must fail the command (non-zero exit), not succeed quietly")
	}
	if !strings.Contains(err.Error(), workerReason) {
		t.Fatalf("CLI error = %q, want it to carry the worker's reason verbatim (%q)", err, workerReason)
	}
}

// TestWorkerReloadCmdSendsReasonAndTimeout checks the flags reach the endpoint and
// that a successful reload exits 0.
func TestWorkerReloadCmdSendsReasonAndTimeout(t *testing.T) {
	resetWorkerReloadOpts(t)
	var got struct {
		Reason     string `json:"reason"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode reload body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"worker_id": "w1",
			"applied":   true,
			"caps": map[string]any{
				"labels": []string{}, "projects": []string{"p1"},
				"agents": []string{"exec", "tty-demo"}, "max_concurrent": 2,
			},
		})
	}))
	defer ts.Close()

	jobConnOpts.server = ts.URL
	workerReloadOpts.reason = "config updated"
	workerReloadOpts.timeout = 5

	cmd := gcli.NewCommand("reload", "", nil)
	cmd.AddArg("id", "worker id", true)
	if err := cmd.ParseArgs([]string{"w1"}); err != nil {
		t.Fatalf("bind arg: %v", err)
	}
	if err := runWorkerReload(cmd, nil); err != nil {
		t.Fatalf("runWorkerReload: %v", err)
	}
	if got.Reason != "config updated" || got.TimeoutSec != 5 {
		t.Fatalf("server saw %+v, want reason/timeout forwarded", got)
	}
}

// TestWorkerReloadCmdRegistered guards the wiring: `worker reload` must exist as a
// sub-command of `worker` and bind the shared -c/--config flag (G011).
func TestWorkerReloadCmdRegistered(t *testing.T) {
	sub := findSub(t, NewWorkerCmd(buildinfo.Info{}), "reload")
	c := gcli.NewCommand(sub.Name, sub.Desc, nil)
	sub.Config(c) // must not panic
	if c.Opt("config") == nil {
		t.Error("worker reload must bind the shared -c/--config flag (G011)")
	}
	if c.Opt("server") == nil {
		t.Error("worker reload must bind --server (it drives the reload through the hub)")
	}
}
