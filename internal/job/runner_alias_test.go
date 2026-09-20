package job

import (
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newAliasService wires a Service whose "self" project allows exactly the runner
// spellings in allowed (so a test can prove the allowlist check, not only the
// spelling normalization), with the exec agent enabled and a harmless argv.
func newAliasService(t *testing.T, allowed []string) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: allowed,
				AllowExec:      true,
			},
		},
	}
	return newServiceFromCfg(t, root, cfg), root
}

// TestSubmitAcceptsBuiltinRunnerAlias: `server` is the spelling the CLI advertises
// (`--runner`'s default: "server = server-local"), so an HTTP/SDK/MCP caller, a `-f`
// task file's frontmatter or a task-book template may spell the built-in runner that
// way too. It must run, and it must be STORED under the canonical key so the web
// console, /v1/runners and the job list keep one vocabulary.
//
// Regression: this path used to fail with
// `400 invalid request: runner "server" is not allowed in project`, because only the
// CLI translated the alias.
func TestSubmitAcceptsBuiltinRunnerAlias(t *testing.T) {
	s, _ := newAliasService(t, []string{"local"})

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: config.BuiltinLocalRunnerAlias,
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Runner != config.BuiltinLocalRunner {
		t.Fatalf("stored runner = %q, want the canonical %q", final.Runner, config.BuiltinLocalRunner)
	}
}

// TestAllowedRunnersAcceptsAliasSpelling: `allowed_runners: [server]` is what an
// operator writes after reading the CLI help, and both spellings of the built-in
// runner must be admitted by such a project — the two name one runner, so a request
// may use either. The reverse (a project listing "local" admitting a "server"
// request) is covered by TestSubmitAcceptsBuiltinRunnerAlias.
func TestAllowedRunnersAcceptsAliasSpelling(t *testing.T) {
	s, _ := newAliasService(t, []string{config.BuiltinLocalRunnerAlias})

	for _, spelling := range []string{config.BuiltinLocalRunnerAlias, config.BuiltinLocalRunner} {
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: spelling,
			Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		})
		if final.Status != StatusDone {
			t.Fatalf("runner %q: status = %s (err=%s), want done", spelling, final.Status, final.Error)
		}
	}
}

// TestValidateAcceptsBuiltinRunnerAlias: the exported Validate (the workflow engine's
// and the schedule validator's gate) admits the alias exactly like Submit does — a
// step or a cron job that names the built-in runner as "server" must not be rejected
// by the gate that is supposed to be the SAME gate as a normal submit's.
func TestValidateAcceptsBuiltinRunnerAlias(t *testing.T) {
	s, _ := newAliasService(t, []string{"local"})
	cfg := s.Config()

	req := JobRequest{ProjectKey: "self", Agent: "exec", Runner: config.BuiltinLocalRunnerAlias, Cmd: []string{"go", "version"}}
	if _, err := s.Validate(cfg, req, false); err != nil {
		t.Fatalf("Validate(server) = %v, want nil", err)
	}
}

// TestDeclaredServerRunnerBeatsAlias pins the declare-wins rule: an operator who
// declares a runner LITERALLY named "server" keeps it — the alias must never silently
// re-route such a request onto the built-in local runner (the same escape hatch the
// agent templates use for a declared agent).
func TestDeclaredServerRunnerBeatsAlias(t *testing.T) {
	declared := &config.Config{Runners: map[string]config.RunnerConfig{
		config.BuiltinLocalRunnerAlias: {Type: "worker", WorkerID: "w-1"},
	}}
	cases := []struct {
		name string
		cfg  *config.Config
		in   string
		want string
	}{
		{"declared server wins", declared, "server", "server"},
		{"canonical untouched", declared, "local", "local"},
		{"alias without declaration", &config.Config{}, "server", "local"},
		{"nil config is safe", nil, "server", "local"},
		{"empty stays empty (runner is required)", &config.Config{}, "", ""},
		{"configured runner untouched", &config.Config{}, "builder", "builder"},
	}
	for _, tc := range cases {
		if got := normalizeRunner(tc.cfg, tc.in); got != tc.want {
			t.Errorf("%s: normalizeRunner(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestListJobsAcceptsBuiltinRunnerAlias: the `--runner` FILTER is an input spelling
// too, and it must match the canonical value rows are stored under — otherwise
// `job ls --runner server` answers "no jobs" for the very jobs that ran on it.
func TestListJobsAcceptsBuiltinRunnerAlias(t *testing.T) {
	s, _ := newAliasService(t, []string{"local"})

	submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: config.BuiltinLocalRunnerAlias,
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
	})

	for _, filter := range []string{config.BuiltinLocalRunnerAlias, config.BuiltinLocalRunner} {
		list, err := s.ListJobs(ListOpts{Runner: filter, Limit: 20})
		if err != nil {
			t.Fatalf("ListJobs(%q): %v", filter, err)
		}
		if len(list) != 1 {
			t.Fatalf("filter %q matched %d jobs, want the 1 that ran on it", filter, len(list))
		}
	}
	// A different runner key still filters it out (the normalization must not turn
	// every filter into "match everything").
	list, err := s.ListJobs(ListOpts{Runner: "builder", Limit: 20})
	if err != nil {
		t.Fatalf("ListJobs(builder): %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("filter builder matched %d jobs, want 0", len(list))
	}
}

// TestApplyTemplateRunnerIsHonoured: a task-book template's `runner:` field is the
// case the CLI's "no --runner" sentinel exists for (SUP-01 P5), so it must actually
// be applied — and normalized, because it comes from a file like any other field.
// Before this, the field was parsed and then dropped on the floor: every template
// submit silently ran on the built-in local runner.
func TestApplyTemplateRunnerIsHonoured(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"fake"}, AllowedRunners: []string{"local", "builder"}},
		},
		Agents: map[string]config.AgentConfig{
			"fake": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"printf", "{{prompt}}"}},
		},
		Runners: map[string]config.RunnerConfig{"builder": {Type: "worker", WorkerID: "w-1"}},
	}
	s := newServiceFromCfg(t, root, cfg)

	// 模板写 worker runner → 真的用模板的 runner（不是静默回落到 local）。
	writeTemplate(t, root, "to-builder", "---\nagent: fake\nrunner: builder\n---\ngo\n")
	req := JobRequest{ProjectKey: "self", Template: "to-builder"}
	if err := s.applyTemplate(cfg, &req); err != nil {
		t.Fatalf("applyTemplate: %v", err)
	}
	if req.Runner != "builder" {
		t.Fatalf("template runner = %q, want the template's builder", req.Runner)
	}

	// 模板用别名拼法 → 归一化成 canonical key。
	writeTemplate(t, root, "to-server", "---\nagent: fake\nrunner: server\n---\ngo\n")
	req = JobRequest{ProjectKey: "self", Template: "to-server"}
	if err := s.applyTemplate(cfg, &req); err != nil {
		t.Fatalf("applyTemplate: %v", err)
	}
	if req.Runner != config.BuiltinLocalRunner {
		t.Fatalf("template runner = %q, want the canonical %q", req.Runner, config.BuiltinLocalRunner)
	}

	// 模板没写 runner → 仍旧回落内置 local（既有行为不变）。
	writeTemplate(t, root, "no-runner", "---\nagent: fake\n---\ngo\n")
	req = JobRequest{ProjectKey: "self", Template: "no-runner"}
	if err := s.applyTemplate(cfg, &req); err != nil {
		t.Fatalf("applyTemplate: %v", err)
	}
	if req.Runner != config.BuiltinLocalRunner {
		t.Fatalf("template runner = %q, want the built-in fallback", req.Runner)
	}
}
