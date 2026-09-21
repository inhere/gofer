package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wshub"
)

// doctorCmd builds the `worker doctor` command with its options bound, the same
// way the CLI does (bindCmd lives in config_test.go).
func doctorCmd() *gcli.Command { return bindCmd(NewWorkerDoctorCmd(buildinfo.Info{})) }

// saveWorkerDoctorOpts points the doctor's flag struct at a fixture config for
// one test and restores the previous values afterwards.
func saveWorkerDoctorOpts(t *testing.T, path string) {
	t.Helper()
	old := workerDoctorOpts
	workerDoctorOpts.config = path
	workerDoctorOpts.timeout = "10s"
	workerDoctorOpts.connect = true
	workerDoctorOpts.json = false
	t.Cleanup(func() { workerDoctorOpts = old })
}

// writeWorkerDoctorConfig writes a worker.yaml fixture into dir and returns its path.
func writeWorkerDoctorConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "worker.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture worker.yaml: %v", err)
	}
	return path
}

// slashPath renders a host path with forward slashes so it can be embedded in a
// yaml fixture verbatim.
func slashPath(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// doctorRow returns the single row named check (fails when it is missing).
func doctorRow(t *testing.T, rep *workerDoctorReport, check string) workerDoctorRow {
	t.Helper()
	for _, r := range rep.Rows {
		if r.Check == check {
			return r
		}
	}
	t.Fatalf("no %q row in report: %+v", check, rep.Rows)
	return workerDoctorRow{}
}

// doctorHubServer stands up a real in-process hub behind an httptest server and
// returns the ws URL a worker would dial. registers counts the accepted websocket
// upgrades, so a test can prove the doctor did NOT touch the hub.
func doctorHubServer(t *testing.T, bindings map[string]string, callerID string, registers *atomic.Int32) string {
	t.Helper()
	hub := wshub.New(bindings)
	r := rux.New()
	r.GET("/v1/workers/connect", func(c *rux.Context) {
		if registers != nil {
			registers.Add(1)
		}
		hub.Accept(c.Resp, c.Req, callerID)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
}

// TestWorkerDoctorReportsMissingConfig: an unreadable worker.yaml is the FIRST
// check and the only FAIL — the doctor must say which file it could not read and
// exit non-zero instead of panicking on a nil config.
func TestWorkerDoctorReportsMissingConfig(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv("GOFER_WORKER_TOKEN", "tok-doctor")
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	saveWorkerDoctorOpts(t, missing)

	rep, err := runWorkerDoctor(doctorCmd(), buildinfo.Info{})
	if err == nil {
		t.Fatal("a missing worker config must fail the doctor (non-zero exit)")
	}
	row := doctorRow(t, rep, "config")
	if row.Status != doctorFail {
		t.Fatalf("config row = %+v, want FAIL", row)
	}
	if !strings.Contains(row.Detail, "nope.yaml") {
		t.Fatalf("config row must name the unreadable file, got %q", row.Detail)
	}
	if n := rep.failures(); n != 1 {
		t.Fatalf("failures() = %d, want 1 (the doctor stops at an unreadable config)", n)
	}
}

// TestWorkerDoctorFlagsUnresolvableHost: an unresolvable hub host is a FAIL and
// the detail must carry the container hint — this is the host.docker.internal
// trap that left the container worker disconnected (CFG-09).
func TestWorkerDoctorFlagsUnresolvableHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv("GOFER_WORKER_TOKEN", "tok-doctor")
	path := writeWorkerDoctorConfig(t, dir, fmt.Sprintf(`worker_id: w-doctor
server_link:
  urls: [ws://gofer-doctor-host.invalid:8767/v1/workers/connect]
  token_env: GOFER_WORKER_TOKEN
roots:
  - from: D:/work/demo
    to: %s
`, slashPath(t.TempDir())))
	saveWorkerDoctorOpts(t, path)

	rep, err := runWorkerDoctor(doctorCmd(), buildinfo.Info{})
	if err == nil {
		t.Fatal("an unresolvable hub host must exit non-zero")
	}
	row := doctorRow(t, rep, "url")
	if row.Status != doctorFail {
		t.Fatalf("url row = %+v, want FAIL", row)
	}
	if !strings.Contains(row.Detail, "gofer-doctor-host.invalid") {
		t.Fatalf("url row must name the unresolvable host, got %q", row.Detail)
	}
	if !strings.Contains(row.Detail, "192.168.65.254") {
		t.Fatalf("url row must carry the container hint (host IP), got %q", row.Detail)
	}
}

// TestWorkerDoctorRootsAndToken: the token resolves from token_env, an existing
// root passes while a duplicate `from` and a missing `to` fail.
func TestWorkerDoctorRootsAndToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv("GOFER_WORKER_TOKEN", "tok-doctor")
	root := t.TempDir()
	path := writeWorkerDoctorConfig(t, dir, fmt.Sprintf(`worker_id: w-doctor
server_link:
  urls: [ws://127.0.0.1:1/v1/workers/connect]
  token_env: GOFER_WORKER_TOKEN
max_concurrent: 3
guards:
  allow_exec: true
roots:
  - from: D:/work/demo
    to: %s
  - from: D:/work/demo
    to: %s
  - from: D:/work/other
    to: %s
`, slashPath(root), slashPath(root), slashPath(filepath.Join(root, "missing"))))
	saveWorkerDoctorOpts(t, path)

	rep, err := runWorkerDoctor(doctorCmd(), buildinfo.Info{})
	if err == nil {
		t.Fatal("a missing root and a duplicate from must exit non-zero")
	}
	if row := doctorRow(t, rep, "token"); row.Status != doctorPass {
		t.Fatalf("token row = %+v, want PASS (GOFER_WORKER_TOKEN is set)", row)
	}
	if row := doctorRow(t, rep, "roots[0]"); row.Status != doctorPass {
		t.Fatalf("roots[0] = %+v, want PASS", row)
	}
	if row := doctorRow(t, rep, "roots[1]"); row.Status != doctorFail || !strings.Contains(row.Detail, "重复") {
		t.Fatalf("roots[1] = %+v, want FAIL for the duplicate from", row)
	}
	if row := doctorRow(t, rep, "roots[2]"); row.Status != doctorFail || !strings.Contains(row.Detail, "不存在") {
		t.Fatalf("roots[2] = %+v, want FAIL for the missing to dir", row)
	}
	if row := doctorRow(t, rep, "guards"); row.Status != doctorPass {
		t.Fatalf("guards row = %+v, want PASS (both guards declared)", row)
	}
	if row := doctorRow(t, rep, "max_concurrent"); !strings.Contains(row.Detail, "3") {
		t.Fatalf("max_concurrent row = %+v, want the configured 3", row)
	}
}

// TestWorkerDoctorConnectsToTestHub: the register probe against a REAL hub —
// accepted → PASS with the negotiated protocol; unbound worker_id → FAIL carrying
// the server's own reason verbatim.
func TestWorkerDoctorConnectsToTestHub(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		t.Setenv("GOFER_WORKER_TOKEN", "tok-doctor")
		wsURL := doctorHubServer(t, map[string]string{"w-doctor": "w-doctor"}, "w-doctor", nil)
		path := writeWorkerDoctorConfig(t, dir, fmt.Sprintf(`worker_id: w-doctor
server_link:
  urls: [%s]
  token_env: GOFER_WORKER_TOKEN
max_concurrent: 2
roots:
  - from: D:/work/demo
    to: %s
`, wsURL, slashPath(t.TempDir())))
		saveWorkerDoctorOpts(t, path)

		rep, err := runWorkerDoctor(doctorCmd(), buildinfo.Info{})
		if err != nil {
			t.Fatalf("doctor should pass against a live hub, got %v (rows %+v)", err, rep.Rows)
		}
		row := doctorRow(t, rep, "connect")
		if row.Status != doctorPass {
			t.Fatalf("connect row = %+v, want PASS", row)
		}
		if !strings.Contains(row.Detail, "accepted") || !strings.Contains(row.Detail, "protocol") {
			t.Fatalf("connect row must report accepted + protocol, got %q", row.Detail)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		t.Setenv("GOFER_WORKER_TOKEN", "tok-doctor")
		// No server.workers binding for w-doctor: the hub rejects the registration.
		wsURL := doctorHubServer(t, map[string]string{"other": "other"}, "w-doctor", nil)
		path := writeWorkerDoctorConfig(t, dir, fmt.Sprintf(`worker_id: w-doctor
server_link:
  urls: [%s]
  token_env: GOFER_WORKER_TOKEN
roots:
  - from: D:/work/demo
    to: %s
`, wsURL, slashPath(t.TempDir())))
		saveWorkerDoctorOpts(t, path)

		rep, err := runWorkerDoctor(doctorCmd(), buildinfo.Info{})
		if err == nil {
			t.Fatal("a rejected registration must exit non-zero")
		}
		row := doctorRow(t, rep, "connect")
		if row.Status != doctorFail {
			t.Fatalf("connect row = %+v, want FAIL", row)
		}
		if !strings.Contains(row.Detail, "worker_id not bound to this token") {
			t.Fatalf("connect row must carry the server's reason verbatim, got %q", row.Detail)
		}
	})
}

// TestWorkerDoctorSkipsRegisterProbeWhileWorkerRuns: registering while this
// worker_id is already connected REPLACES the live connection on the hub (its
// in-flight jobs are failed), so the doctor must not send a register frame — it
// reports a WARN and points at the worker log instead.
func TestWorkerDoctorSkipsRegisterProbeWhileWorkerRuns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv("GOFER_WORKER_TOKEN", "tok-doctor")
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	pidFile := filepath.Join(runDir, "worker-w-doctor.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	var registers atomic.Int32
	wsURL := doctorHubServer(t, map[string]string{"w-doctor": "w-doctor"}, "w-doctor", &registers)
	path := writeWorkerDoctorConfig(t, dir, fmt.Sprintf(`worker_id: w-doctor
server_link:
  urls: [%s]
  token_env: GOFER_WORKER_TOKEN
roots:
  - from: D:/work/demo
    to: %s
`, wsURL, slashPath(t.TempDir())))
	saveWorkerDoctorOpts(t, path)

	rep, err := runWorkerDoctor(doctorCmd(), buildinfo.Info{})
	if err != nil {
		t.Fatalf("a skipped probe is a WARN, not a failure: %v (rows %+v)", err, rep.Rows)
	}
	if n := registers.Load(); n != 0 {
		t.Fatalf("the doctor registered %d time(s) while the worker was running", n)
	}
	row := doctorRow(t, rep, "connect")
	if row.Status != doctorWarn {
		t.Fatalf("connect row = %+v, want WARN", row)
	}
	if !strings.Contains(row.Detail, strconv.Itoa(os.Getpid())) {
		t.Fatalf("the skip must name the running worker's pid, got %q", row.Detail)
	}
}

// TestWorkerDoctorJSONIsMachineReadable: --json prints the same rows as one JSON
// document (no table around it).
func TestWorkerDoctorJSONIsMachineReadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv("GOFER_WORKER_TOKEN", "tok-doctor")
	path := writeWorkerDoctorConfig(t, dir, fmt.Sprintf(`worker_id: w-doctor
server_link:
  urls: [ws://127.0.0.1:1/v1/workers/connect]
  token_env: GOFER_WORKER_TOKEN
roots:
  - from: D:/work/demo
    to: %s
`, slashPath(t.TempDir())))
	saveWorkerDoctorOpts(t, path)
	workerDoctorOpts.connect = false
	workerDoctorOpts.json = true

	rep, err := runWorkerDoctor(doctorCmd(), buildinfo.Info{})
	if err != nil {
		t.Fatalf("doctor run: %v", err)
	}
	out, err := renderWorkerDoctor(rep, true)
	if err != nil {
		t.Fatalf("render --json: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, `"worker_id": "w-doctor"`) {
		t.Fatalf("--json output is not a JSON document: %q", out)
	}
	if !strings.Contains(out, `"check": "config"`) {
		t.Fatalf("--json output must carry the rows: %q", out)
	}
}
