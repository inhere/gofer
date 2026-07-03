package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

func TestLoadEnvFilesMap(t *testing.T) {
	cfgDir := t.TempDir()
	projectRoot := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	mustWrite(t, filepath.Join(cfgDir, "secret", "app.env"), "SECRET_KEY=topsecret\n# comment\nQUOTED=\"two words\"\n")
	mustWrite(t, filepath.Join(cfgDir, "secret", "bare.env"), "BARE=ok\n")
	mustWrite(t, filepath.Join(projectRoot, "sub", "local.env"), "PROJECT_ENV=local\nSECRET_KEY=override\n")

	cfg := &config.Config{}
	proj := config.ProjectConfig{HostPath: projectRoot}
	got, err := LoadEnvFilesMap([]string{"secret/app.env", "bare.env", "sub/local.env"}, cfg, proj)
	if err != nil {
		t.Fatalf("LoadEnvFilesMap: %v", err)
	}
	want := map[string]string{
		"SECRET_KEY":  "override",
		"QUOTED":      "two words",
		"BARE":        "ok",
		"PROJECT_ENV": "local",
	}
	if !mapsEqual(got, want) {
		t.Fatalf("env map = %#v, want %#v", got, want)
	}
}

func TestLoadEnvFilesMapRejectsMissingAndUnsafePaths(t *testing.T) {
	cfgDir := t.TempDir()
	projectRoot := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	cfg := &config.Config{}
	proj := config.ProjectConfig{HostPath: projectRoot}

	for _, paths := range [][]string{
		{"secret/missing.env"},
		{"../x.env"},
		{"secret/../x.env"},
		{filepath.Join(projectRoot, "abs.env")},
	} {
		if _, err := LoadEnvFilesMap(paths, cfg, proj); err == nil {
			t.Fatalf("LoadEnvFilesMap(%v) expected error", paths)
		}
	}
}

func TestSubmitEnvFilesSecretIsolation(t *testing.T) {
	cfgDir := t.TempDir()
	root := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	mustWrite(t, filepath.Join(cfgDir, "secret", "job.env"), "SECRET_KEY=topsecret\nAGENT_ONLY=from-secret\nJOB_WINS=from-secret\n")

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cap := &captureRunner{}
	s := newTestServiceWithRunner(t, root, cap)
	s.config().Agents["exec"] = config.AgentConfig{
		Type: agent.TypeExec,
		Env:  map[string]string{"AGENT_ONLY": "from-agent", "AGENT_KEY": "agent-value"},
	}
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"ignored"}, Cwd: ".", TimeoutSec: 30,
		EnvFiles: []string{"secret/job.env"},
		Env:      map[string]string{"JOB_WINS": "from-job"},
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}

	req := cap.request()
	if req.Env["SECRET_KEY"] != "topsecret" {
		t.Fatalf("runner env SECRET_KEY = %q, want topsecret", req.Env["SECRET_KEY"])
	}
	if req.Env["AGENT_ONLY"] != "from-agent" {
		t.Fatalf("agent env should override env_files, got %q", req.Env["AGENT_ONLY"])
	}
	if req.Env["JOB_WINS"] != "from-job" {
		t.Fatalf("job env should override env_files, got %q", req.Env["JOB_WINS"])
	}

	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("meta.GetJob(%s): ok=%v err=%v", final.ID, ok, err)
	}
	if strings.Contains(rec.RequestJSON, "topsecret") {
		t.Fatalf("request_json leaked secret value: %s", rec.RequestJSON)
	}
	if !strings.Contains(rec.RequestJSON, `"env_files":["secret/job.env"]`) {
		t.Fatalf("request_json missing env_files declaration: %s", rec.RequestJSON)
	}
	var stored JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &stored); err != nil {
		t.Fatalf("request_json unmarshal: %v", err)
	}
	if _, ok := stored.Env["SECRET_KEY"]; ok {
		t.Fatalf("secret value was copied into stored req.Env: %#v", stored.Env)
	}

	apiBody, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("marshal api body: %v", err)
	}
	if strings.Contains(string(apiBody), "topsecret") {
		t.Fatalf("JobResult API body leaked secret value: %s", apiBody)
	}
	logText := logs.String()
	if !strings.Contains(logText, "SECRET_KEY") || !strings.Contains(logText, "secret/job.env") {
		t.Fatalf("log should mention file and key names only, got: %s", logText)
	}
	if strings.Contains(logText, "topsecret") {
		t.Fatalf("log leaked secret value: %s", logText)
	}
}

func TestSubmitRemoteRejectsEnvFiles(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		Cmd: []string{"echo", "hi"}, Cwd: ".", EnvFiles: []string{"secret/job.env"},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
	if !strings.Contains(err.Error(), "env_files") {
		t.Fatalf("error should mention env_files, got %v", err)
	}
}

type captureRunner struct {
	mu  sync.Mutex
	req runner.Request
}

func (r *captureRunner) Name() string { return localrunner.Name }

func (r *captureRunner) Run(_ context.Context, req runner.Request) runner.Result {
	r.mu.Lock()
	r.req = req
	r.mu.Unlock()
	return runner.Result{ExitCode: 0}
}

func (r *captureRunner) request() runner.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.req
}

func newTestServiceWithRunner(t *testing.T, root string, run runner.Runner) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Agents:  map[string]config.AgentConfig{},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: run}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return NewService(cfg, projReg, agentReg, runners, meta, nil)
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if b[k] != av {
			return false
		}
	}
	return true
}
