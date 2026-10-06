package commands

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func newStewardCLIServer(t *testing.T, enabled bool) string {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{AllowEmptyToken: true},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			config.DefaultProjectKey: {HostPath: root, AllowedAgents: []string{"acpa"}, AllowedRunners: []string{"local"}},
		},
		Agents:  map[string]config.AgentConfig{"acpa": {Type: agent.TypeACPAgent, Command: testcmd.Path(t), Args: acptest.CmdArgs(acptest.Options{EchoPrompt: true})}},
		Steward: config.StewardConfig{Enabled: enabled, Agent: "acpa"},
	}
	st, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New(), acprunner.Name: acprunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, st, nil)
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	srv := httpapi.New(&cfg.Server, "", true, jobs, eng, projects, agents, nil, nil, nil, nil)
	srv.Steward().SetConfigFn(func() (config.StewardConfig, config.WorkConfig) { c := projects.Config(); return c.Steward, c.Work })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_, _ = srv.Steward().Stop()
		srv.Steward().WaitIdle()
	})
	return ts.URL
}

func stewardCLI(t *testing.T, server string, args ...string) (string, int) {
	t.Helper()
	stewardOpts = stewardOptions{}
	workOpts = workOptions{}
	var code int
	out := captureOutput(t, func() {
		app := NewApp("test")
		code = app.Run(append([]string{"steward", args[0], "--server", server}, args[1:]...))
	})
	return out, code
}

func stewardCLIOK(t *testing.T, server string, args ...string) string {
	t.Helper()
	out, code := stewardCLI(t, server, args...)
	if code != 0 {
		t.Fatalf("gofer steward %v failed (exit %d):\n%s", args, code, out)
	}
	return out
}

func TestStewardCLIStatusAskNotesAndStop(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	server := newStewardCLIServer(t, true)

	out := stewardCLIOK(t, server, "status")
	for _, want := range []string{"steward:   on", "acpa", "not_started"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status lacks %q:\n%s", want, out)
		}
	}
	// A work item for the steward to know about.
	workCLIOK(t, server, "new", "换门禁设备", "--next", "周三去现场")

	// ask starts it and prints the reply (the fake agent's text).
	out = stewardCLIOK(t, server, "ask", "我手上还有什么没完成？", "--timeout", "60")
	if !strings.Contains(out, "Hello world.") {
		t.Fatalf("ask did not print the steward's reply:\n%s", out)
	}
	out = stewardCLIOK(t, server, "status")
	if !strings.Contains(out, "idle") || !strings.Contains(out, "job:") {
		t.Fatalf("status after ask:\n%s", out)
	}
	// A second question reaches the live session and prints ITS reply (turn 2).
	out = stewardCLIOK(t, server, "ask", "今天去现场要做什么？", "--timeout", "60")
	if !strings.Contains(out, "今天去现场要做什么") {
		t.Fatalf("second reply must echo the second question:\n%s", out)
	}

	// notes: write from a file, read back, history, version, conflict-free overwrite.
	dir := t.TempDir()
	f := filepath.Join(dir, "n.md")
	if err := os.WriteFile(f, []byte("- 周三去现场\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := stewardCLIOK(t, server, "notes", "--set-file", f); !strings.Contains(out, "saved steward notes v1") {
		t.Fatalf("set-file:\n%s", out)
	}
	if err := os.WriteFile(f, []byte("- 周三去现场\n- 设备找老王\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stewardCLIOK(t, server, "notes", "--set-file", f)
	out = stewardCLIOK(t, server, "notes")
	if !strings.Contains(out, "v2") || !strings.Contains(out, "设备找老王") {
		t.Fatalf("notes:\n%s", out)
	}
	if out := stewardCLIOK(t, server, "notes", "--version", "1"); !strings.Contains(out, "周三去现场") || strings.Contains(out, "设备找老王") {
		t.Fatalf("notes v1:\n%s", out)
	}
	if out := stewardCLIOK(t, server, "notes", "--history"); !strings.Contains(out, "2") || !strings.Contains(out, "1") {
		t.Fatalf("history:\n%s", out)
	}

	// review: a forced one runs on the live session.
	out = stewardCLIOK(t, server, "review", "--force")
	if !strings.Contains(out, "review") || !strings.Contains(out, "started") {
		t.Fatalf("review:\n%s", out)
	}

	out = stewardCLIOK(t, server, "stop")
	if !strings.Contains(out, "ended") {
		t.Fatalf("stop:\n%s", out)
	}
	if out := stewardCLIOK(t, server, "stop"); !strings.Contains(out, "no steward session") {
		t.Fatalf("second stop:\n%s", out)
	}
}

func TestStewardCLIDisabledExplainsItself(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	server := newStewardCLIServer(t, false)
	if out := stewardCLIOK(t, server, "status"); !strings.Contains(out, "steward:   off") || !strings.Contains(out, "settings page") {
		t.Fatalf("status:\n%s", out)
	}
	if out, code := stewardCLI(t, server, "ask", "在吗"); code == 0 || !strings.Contains(out, "not enabled") {
		t.Fatalf("ask while disabled must fail with the reason:\n%s", out)
	}
}

func TestLastTurnText(t *testing.T) {
	if got := lastTurnText("--- turn 1 ---\n第一轮\n--- turn 2 ---\n第二轮的回答\n"); got != "第二轮的回答" {
		t.Fatalf("got %q", got)
	}
	if got := lastTurnText("没有标记的输出\n"); got != "没有标记的输出" {
		t.Fatalf("got %q", got)
	}
}
