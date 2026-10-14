package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// addDirAgentCfg is the agent under test: an argv-echoing CLI (testcmd's `argv` prints
// what the child actually received, one argument per line), the add_dir_args fragment the
// built-in suga template carries, and the session pair a continuation is driven by.
func addDirAgentCfg(t *testing.T, withField bool) config.AgentConfig {
	t.Helper()
	ac := config.AgentConfig{
		Type:          agent.TypeCLIAgent,
		Command:       testcmd.Path(t),
		Args:          []string{"argv", "{{prompt}}"},
		SessionInject: []string{"--session-id", "{{session_id}}"},
		SessionResume: []string{"argv", "--resume", "{{session_id}}", "{{prompt}}"},
	}
	if withField {
		ac.AddDirArgs = []string{"--add-dir", "{{dir}}:ro"}
	}
	return ac
}

// newAddDirService builds a workspace project whose root is a git repository with a
// NESTED repository `sub` inside it — the shape a job dispatched into an inner checkout
// runs in. agentName names the agent to submit with: "suga" inherits the built-in
// add_dir_args, "omp" (no built-in default) is the control case.
func newAddDirService(t *testing.T, agentName string, withField bool) (s *Service, root, nested string) {
	t.Helper()
	root = t.TempDir()
	initGitRepo(t, root)
	nested = filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	initGitRepo(t, nested)

	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"suga", "omp", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true},
		},
		Agents: map[string]config.AgentConfig{agentName: addDirAgentCfg(t, withField)},
	}
	return newServiceFromCfg(t, root, cfg), root, nested
}

// argvDirAfter returns the directory following the first --add-dir of a testcmd `argv`
// echo, and whether one was there at all. The comparison is against the recorded argv of
// the child, not against what gofer intended to send.
func argvDirAfter(t *testing.T, out string) (string, bool) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "--add-dir" {
			continue
		}
		if i+1 >= len(lines) {
			t.Fatalf("--add-dir carries no value:\n%s", out)
		}
		return strings.TrimSpace(lines[i+1]), true
	}
	return "", false
}

// assertAddedRoot asserts the echo carries `--add-dir <root>:ro`.
func assertAddedRoot(t *testing.T, out, root string) {
	t.Helper()
	dir, ok := argvDirAfter(t, out)
	if !ok {
		t.Fatalf("no --add-dir in the agent's argv:\n%s", out)
	}
	if !strings.HasSuffix(dir, ":ro") {
		t.Fatalf("extra directory %q is not read-only", dir)
	}
	if !sameDir(strings.TrimSuffix(dir, ":ro"), root) {
		t.Fatalf("extra directory = %q, want the project root %q", dir, root)
	}
}

// TestAddDirArgsNestedRepo: the fragment is rendered with the PROJECT ROOT when the job's
// cwd is a different repository than the root.
func TestAddDirArgsNestedRepo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initGitRepo(t, root)
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepo(t, nested)

	cfg := &config.Config{Projects: map[string]config.ProjectConfig{"self": {HostPath: root}}}
	ac := config.AgentConfig{Type: agent.TypeCLIAgent, Command: "suga",
		AddDirArgs: []string{"--add-dir", "{{dir}}:ro"}}

	got := addDirArgs(cfg, cfg.Projects["self"], ac, nested)
	if len(got) != 2 || got[0] != "--add-dir" || got[1] != root+":ro" {
		t.Fatalf("nested repo args = %v, want [--add-dir %s:ro]", got, root)
	}
}

// TestAddDirArgsNotAdded: nothing is appended when the cwd sits in the project root's OWN
// repository (the root itself, or a plain subdirectory of it), when the agent declares no
// fragment, when it is not a cli-agent, and when the cwd's repository cannot be
// established at all (not a checkout, missing directory, no project root, no config).
func TestAddDirArgsNotAdded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initGitRepo(t, root)
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepo(t, nested)
	inside := filepath.Join(root, "pkg")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	plain := t.TempDir() // no git checkout anywhere above it

	cfg := &config.Config{Projects: map[string]config.ProjectConfig{"self": {HostPath: root}}}
	proj := cfg.Projects["self"]
	withField := config.AgentConfig{Type: agent.TypeCLIAgent, Command: "suga",
		AddDirArgs: []string{"--add-dir", "{{dir}}:ro"}}
	noField := config.AgentConfig{Type: agent.TypeCLIAgent, Command: "suga"}
	acpAgent := withField
	acpAgent.Type = agent.TypeACPAgent

	cases := []struct {
		name string
		ac   config.AgentConfig
		cwd  string
	}{
		{"cwd is the project's own repository", withField, root},
		{"cwd is a plain subdirectory of it", withField, inside},
		{"agent declares no add_dir_args", noField, nested},
		{"agent is an acp-agent", acpAgent, nested},
		{"cwd is not in a git checkout", withField, plain},
		{"cwd does not exist", withField, filepath.Join(plain, "missing")},
		{"no cwd", withField, ""},
	}
	for _, tc := range cases {
		if got := addDirArgs(cfg, proj, tc.ac, tc.cwd); got != nil {
			t.Fatalf("%s: appended %v, want nothing", tc.name, got)
		}
	}
	if got := addDirArgs(cfg, config.ProjectConfig{}, withField, nested); got != nil {
		t.Fatalf("no project root: appended %v, want nothing", got)
	}
	if got := addDirArgs(nil, proj, withField, nested); got != nil {
		t.Fatalf("nil config: appended %v, want nothing", got)
	}
}

// TestSubmitAddsProjectRootForNestedRepo (end to end): a job dispatched into a nested
// repository receives the project root as an extra read-only directory; the same job in
// the project's own repository, and an agent that declares no fragment, do not.
func TestSubmitAddsProjectRootForNestedRepo(t *testing.T) {
	t.Parallel()

	t.Run("nested repo", func(t *testing.T) {
		t.Parallel()
		s, root, _ := newAddDirService(t, "suga", true)
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "suga", Runner: "local", Prompt: "go", Cwd: "sub", TimeoutSec: 60,
		})
		if final.Status != StatusDone {
			t.Fatalf("status = %s (err=%q)", final.Status, final.Error)
		}
		assertAddedRoot(t, readJobLog(t, final, store.StdoutFile), root)
	})

	t.Run("project root repo", func(t *testing.T) {
		t.Parallel()
		s, _, _ := newAddDirService(t, "suga", true)
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "suga", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
		})
		if final.Status != StatusDone {
			t.Fatalf("status = %s (err=%q)", final.Status, final.Error)
		}
		out := readJobLog(t, final, store.StdoutFile)
		if _, ok := argvDirAfter(t, out); ok {
			t.Fatalf("the project's own repository needs no extra directory:\n%s", out)
		}
	})

	// The control case: an agent WITHOUT the fragment — "omp" carries no built-in
	// add_dir_args, so nothing may be appended even in a nested repository (a
	// suga-named agent would inherit the built-in default, which is the point of it).
	t.Run("agent without the field", func(t *testing.T) {
		t.Parallel()
		s, _, _ := newAddDirService(t, "omp", false)
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "omp", Runner: "local", Prompt: "go", Cwd: "sub", TimeoutSec: 60,
		})
		if final.Status != StatusDone {
			t.Fatalf("status = %s (err=%q)", final.Status, final.Error)
		}
		out := readJobLog(t, final, store.StdoutFile)
		if _, ok := argvDirAfter(t, out); ok {
			t.Fatalf("an agent that declares no add_dir_args must not receive one:\n%s", out)
		}
	})
}

// TestResumeAddsProjectRootForNestedRepo: the continuation argv carries the extra
// directory too — a resumed job runs in the same nested checkout, so it must see the
// workspace exactly like the first turn did.
func TestResumeAddsProjectRootForNestedRepo(t *testing.T) {
	t.Parallel()
	s, root, _ := newAddDirService(t, "suga", true)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "suga", Runner: "local", Prompt: "go", Cwd: "sub", TimeoutSec: 60,
	})
	if src.SessionID == "" {
		t.Fatal("the source job captured no session id")
	}
	resumed, err := s.ResumeJob(src.ID, "again", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	final, ok := s.Wait(resumed.ID)
	if !ok {
		t.Fatalf("continuation job %s not found", resumed.ID)
	}
	if final.Status != StatusDone {
		t.Fatalf("continuation status = %s (err=%q)", final.Status, final.Error)
	}
	assertAddedRoot(t, readJobLog(t, final, store.StdoutFile), root)
}

// TestAddDirProbeIsNotFatal: the repository probe never fails a job. A cancelled probe
// (its own budget exhausted) yields nothing to append, exactly like a missing git.
func TestAddDirProbeIsNotFatal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := runGit(ctx, dir, 256, "rev-parse", "--show-toplevel"); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("a cancelled probe must yield nothing, got %q", out)
	}
}
