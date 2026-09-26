package commands

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/rux/v2"

	yaml "github.com/goccy/go-yaml"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// workerInitTestHub serves the two routes the wizard talks to on ONE address: the
// read-only /v1/workers/{id}/assignable it reads, and the real register handshake
// (POST-free websocket /v1/workers/connect) the doctor inside the wizard probes.
// Both must share a port: the generated worker.yaml can only name one hub.
func workerInitTestHub(t *testing.T, workerID, token string, projects []map[string]string) string {
	t.Helper()
	// The hub binding maps worker_id → the CALLER ID its token authenticates as, which
	// in this fixture is the worker id itself.
	hub := wshub.New(map[string]string{workerID: workerID})
	r := rux.New()
	r.GET("/v1/workers/connect", func(c *rux.Context) { hub.Accept(c.Resp, c.Req, workerID) })
	r.GET("/v1/workers/{id}/assignable", func(c *rux.Context) {
		c.JSON(http.StatusOK, map[string]any{
			"worker_id":        c.Param("id"),
			"projects":         projects,
			"server_version":   "v0.99-test",
			"protocol_version": wsproto.CurrentProtocolVersion,
		})
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL
}

// workerInitCmdAndOpts binds `worker init` the way the CLI does and replaces the
// package-level option/detector seams with the test's fixtures (restored after).
// Binding FIRST matters: gcli writes the flag defaults into workerInitOpts.
func workerInitCmdAndOpts(t *testing.T, det agent.Detector) *gcli.Command {
	t.Helper()
	c := bindCmd(NewWorkerInitCmd(buildinfo.Info{}))
	oldOpts, oldDet := workerInitOpts, workerInitDetector
	t.Cleanup(func() { workerInitOpts, workerInitDetector = oldOpts, oldDet })
	workerInitOpts = workerInitFlags{}
	workerInitDetector = det
	return c
}

// workerInitFixtureDetector reports claude and codex as installed (everything else —
// the acp adapters, the tty variants — as not), so the generated worker.yaml's agents
// block is deterministic no matter what the machine running the tests has on PATH.
func workerInitFixtureDetector() *fakeDetector {
	return &fakeDetector{res: map[string]agent.DetectResult{
		agent.ExecAgentKey: {Available: true, Version: "builtin"},
		"claude":           {Available: true, Version: "2.1.278"},
		"codex":            {Available: true},
	}}
}

// TestWorkerInitInfersRoots pins the roots inference: the LONGEST COMMON PREFIX of the
// server-side project paths becomes `from`, its local form becomes `to` (same path
// first, then the drive-letter translation), and every project is checked against the
// LOCAL filesystem so an unmapped/missing tree is reported instead of silently
// producing a root that maps to nothing.
func TestWorkerInitInfersRoots(t *testing.T) {
	t.Run("same_path", func(t *testing.T) {
		parent := t.TempDir()
		a, b := filepath.Join(parent, "a"), filepath.Join(parent, "b")
		for _, d := range []string{a, b} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", d, err)
			}
		}
		missing := filepath.Join(parent, "c")

		roots, notFound := inferWorkerRoots([]string{a, b, missing}, osStatExists)
		if len(roots) != 1 {
			t.Fatalf("roots = %+v, want exactly one inferred root", roots)
		}
		// `from` is normalised to the forward-slash spelling the server configs use.
		if want := slashPath(parent); roots[0].From != want || roots[0].To != want {
			t.Fatalf("root = %+v, want {%s %s} (same path exists locally)", roots[0], want, want)
		}
		if len(notFound) != 1 || notFound[0] != missing {
			t.Fatalf("notFound = %v, want [%s]", notFound, missing)
		}
	})

	t.Run("drive_translation", func(t *testing.T) {
		// The container form of the design's example: the server says D:/work/inhere,
		// the same tree is mounted at /d/work/inhere on this machine.
		exists := func(p string) bool {
			return strings.HasPrefix(strings.ReplaceAll(p, `\`, "/"), "/d/work/inhere")
		}
		roots, notFound := inferWorkerRoots([]string{"D:/work/inhere/a", "D:/work/inhere/b"}, exists)
		if len(roots) != 1 || roots[0].From != "D:/work/inhere" || roots[0].To != "/d/work/inhere" {
			t.Fatalf("roots = %+v, want [{D:/work/inhere /d/work/inhere}]", roots)
		}
		if len(notFound) != 0 {
			t.Fatalf("notFound = %v, want none (both projects exist under the translated root)", notFound)
		}
	})
}

// TestWorkerInitNonInteractive pins the `--yes` path end to end: explicit --roots win
// over inference, the worker.yaml carries worker_id/urls/roots/detected agents, the
// .env carries the token, and the wizard runs the doctor itself and prints its table.
func TestWorkerInitNonInteractive(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv("GOFER_WORKER_TOKEN", "tok-w-smoke")
	parent := t.TempDir()
	projA, projB := filepath.Join(parent, "a"), filepath.Join(parent, "b")
	for _, d := range []string{projA, projB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	base := workerInitTestHub(t, "w-smoke", "tok-w-smoke", []map[string]string{
		{"key": "p-a", "host_path": slashPath(projA)},
		{"key": "p-b", "host_path": slashPath(projB)},
	})

	c := workerInitCmdAndOpts(t, workerInitFixtureDetector())
	workerInitOpts.server = base
	workerInitOpts.token = "tok-w-smoke"
	workerInitOpts.id = "w-smoke"
	workerInitOpts.yes = true
	workerInitOpts.roots = []string{parent + "=" + parent}

	var err error
	out := captureOutput(t, func() { err = runWorkerInit(c, buildinfo.Info{}) })
	if err != nil {
		t.Fatalf("worker init --yes: %v\noutput:\n%s", err, out)
	}

	raw, err := os.ReadFile(filepath.Join(dir, config.WorkerConfigFileName))
	if err != nil {
		t.Fatalf("read generated worker.yaml: %v", err)
	}
	var wc config.WorkerConfig
	if err := yaml.Unmarshal(raw, &wc); err != nil {
		t.Fatalf("generated worker.yaml does not parse: %v\n%s", err, raw)
	}
	if wc.WorkerID != "w-smoke" {
		t.Fatalf("worker_id = %q, want w-smoke", wc.WorkerID)
	}
	if len(wc.ServerLink.URLs) != 1 || !strings.HasPrefix(wc.ServerLink.URLs[0], "ws://") ||
		!strings.HasSuffix(wc.ServerLink.URLs[0], "/v1/workers/connect") {
		t.Fatalf("server_link.urls = %v, want one ws://<host>/v1/workers/connect", wc.ServerLink.URLs)
	}
	if wc.ServerLink.TokenEnv != "GOFER_WORKER_TOKEN" {
		t.Fatalf("token_env = %q, want GOFER_WORKER_TOKEN", wc.ServerLink.TokenEnv)
	}
	if len(wc.Roots) != 1 || wc.Roots[0].From != parent || wc.Roots[0].To != parent {
		t.Fatalf("roots = %+v, want the EXPLICIT --roots mapping to win over inference", wc.Roots)
	}
	if _, ok := wc.Agents["claude"]; !ok {
		t.Fatalf("agents = %v, want the detected claude", wc.Agents)
	}
	if _, ok := wc.Agents["codex"]; !ok {
		t.Fatalf("agents = %v, want the detected codex", wc.Agents)
	}
	if _, ok := wc.Agents["jcode-acp"]; ok {
		t.Fatalf("agents = %v, must NOT declare an undetected agent", wc.Agents)
	}

	env, err := os.ReadFile(filepath.Join(dir, config.EnvFileName))
	if err != nil {
		t.Fatalf("read generated .env: %v", err)
	}
	if !strings.Contains(string(env), "GOFER_WORKER_TOKEN=tok-w-smoke") {
		t.Fatalf(".env = %q, want GOFER_WORKER_TOKEN=tok-w-smoke", env)
	}

	if !strings.Contains(out, "worker_id") || !strings.Contains(out, "connect") {
		t.Fatalf("doctor output not printed by the wizard:\n%s", out)
	}
	// The wizard also creates the default workspace locally and says where to register it.
	if fi, err := os.Stat(filepath.Join(home, ".gofer", "workspace")); err != nil || !fi.IsDir() {
		t.Fatalf("worker wizard did not create the default workspace (err %v)", err)
	}
}

// TestWorkerInitRefusesOverwriteWithoutForce: an existing worker.yaml is refused
// (coded, non-zero exit) BEFORE any network work; --force backs the old file up to
// worker.yaml.bak-<time> and then writes the new one.
func TestWorkerInitRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	setTestHome(t, t.TempDir())
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv("GOFER_WORKER_TOKEN", "tok-w-smoke")
	path := filepath.Join(dir, config.WorkerConfigFileName)
	if err := os.WriteFile(path, []byte("worker_id: old\n"), 0o600); err != nil {
		t.Fatalf("seed worker.yaml: %v", err)
	}
	// A dead address proves no request was attempted: the refusal must come first.
	c := workerInitCmdAndOpts(t, workerInitFixtureDetector())
	workerInitOpts.server = "http://127.0.0.1:1"
	workerInitOpts.token = "tok-w-smoke"
	workerInitOpts.id = "w-smoke"
	workerInitOpts.yes = true

	err := runWorkerInit(c, buildinfo.Info{})
	if err == nil {
		t.Fatal("an existing worker.yaml must be refused without --force")
	}
	assertCodedExit(t, err)
	if !strings.Contains(err.Error(), "worker.yaml") && !strings.Contains(err.Error(), "--force") {
		t.Fatalf("refusal must name the file or --force, got %v", err)
	}

	// --force: the old bytes survive as a timestamped backup.
	parent := t.TempDir()
	base := workerInitTestHub(t, "w-smoke", "tok-w-smoke", nil)
	c2 := workerInitCmdAndOpts(t, workerInitFixtureDetector())
	workerInitOpts.server = base
	workerInitOpts.token = "tok-w-smoke"
	workerInitOpts.id = "w-smoke"
	workerInitOpts.yes = true
	workerInitOpts.force = true
	workerInitOpts.roots = []string{parent + "=" + parent}
	var runErr error
	out := captureOutput(t, func() { runErr = runWorkerInit(c2, buildinfo.Info{}) })
	if runErr != nil {
		t.Fatalf("worker init --force: %v\n%s", runErr, out)
	}

	backups, err := filepath.Glob(filepath.Join(dir, "worker.yaml.bak-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v (err %v), want exactly one worker.yaml.bak-<time>", backups, err)
	}
	old, err := os.ReadFile(backups[0])
	if err != nil || string(old) != "worker_id: old\n" {
		t.Fatalf("backup content = %q (err %v), want the previous file verbatim", old, err)
	}
	now, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(now), "w-smoke") {
		t.Fatalf("worker.yaml after --force = %q (err %v), want the new config", now, err)
	}
}
