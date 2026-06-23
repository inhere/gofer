package commands

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
)

// parseRun runs the gcli arg pipeline (NormalizeArgs -> app.Run) up to the point
// where `job run` binds its flags. It returns the bound jobRunOpts snapshot, the
// resolved prompt (--prompt flag) and the captured raw cmd (remainArgs, i.e. the
// tokens after `--` that gcli leaves for the Func handler), so tests can assert
// the JobRequest mapping without a live server.
func parseRun(t *testing.T, in []string) (project, agent, runner, cwd, prompt string, cmd []string) {
	t.Helper()
	// Reset shared state so tests don't leak into each other.
	jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner = "", "", ""
	jobRunOpts.cwd, jobRunOpts.prompt = "", ""

	app := NewApp("test")
	// Replace job run's Func with a capturing one so we never hit the network.
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		// Mirror runJobRun: prompt from the --prompt flag; cmd from the arrayed
		// `cmd` arg (the post-`--` tokens gcli binds natively).
		prompt = jobRunOpts.prompt
		if a := c.Arg("cmd"); a != nil {
			cmd = a.Strings()
		}
		return nil
	}

	// app.Run returns the process exit code (0 on success); flag-binding happens
	// inside, so a non-zero code here would signal a parse failure. gcli handles
	// `--` natively and binds the post-`--` tokens to the arrayed `cmd` arg.
	if code := app.Run(NormalizeArgs(app, in)); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, in)
	}
	return jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner, jobRunOpts.cwd, prompt, cmd
}

func TestJobRunRawCmdMapping(t *testing.T) {
	p, a, _, _, prompt, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "version"})
	if p != "self" || a != "exec" {
		t.Fatalf("flags not bound: project=%q agent=%q", p, a)
	}
	if prompt != "" {
		t.Fatalf("prompt should be empty for raw cmd, got %q", prompt)
	}
	if !reflect.DeepEqual(cmd, []string{"go", "version"}) {
		t.Fatalf("remainArgs=%v want [go version]", cmd)
	}
}

func TestJobRunRawCmdWithFlagsInside(t *testing.T) {
	// Flags after `--` belong to the raw command, not to job run.
	_, _, _, _, _, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "test", "-run", "X"})
	if !reflect.DeepEqual(cmd, []string{"go", "test", "-run", "X"}) {
		t.Fatalf("remainArgs=%v want [go test -run X]", cmd)
	}
}

func TestJobRunPromptFlag(t *testing.T) {
	// prompt is supplied via the --prompt flag (cli-agents); no positional arg.
	_, _, _, _, prompt, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "claude", "--prompt", "summarize the repo"})
	if prompt != "summarize the repo" {
		t.Fatalf("prompt=%q want 'summarize the repo'", prompt)
	}
	if len(cmd) != 0 {
		t.Fatalf("remainArgs should be empty, got %v", cmd)
	}
}

func TestJobRunDefaults(t *testing.T) {
	_, _, runner, cwd, _, _ := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--", "ls"})
	if runner != "local" {
		t.Fatalf("runner default=%q want local", runner)
	}
	if cwd != "." {
		t.Fatalf("cwd default=%q want .", cwd)
	}
}

// TestResolveProjectByCwdUnique: a single project whose host_path is a prefix of
// the cwd is detected, and cwd is relativised against that root.
func TestResolveProjectByCwdUnique(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"siv": {HostPath: root},
	}}
	// cwd == root → rel ".".
	key, rel, ok := resolveProjectByCwd(cfg, root)
	if !ok || key != "siv" || rel != "." {
		t.Fatalf("root cwd: got (%q,%q,%v) want (siv,.,true)", key, rel, ok)
	}
	// cwd in a subdir → rel = the subpath.
	sub := filepath.Join(root, "module", "a")
	key, rel, ok = resolveProjectByCwd(cfg, sub)
	if !ok || key != "siv" || rel != filepath.Join("module", "a") {
		t.Fatalf("sub cwd: got (%q,%q,%v) want (siv,module/a,true)", key, rel, ok)
	}
}

// TestResolveProjectByCwdLongestPrefix: nested projects (one root under another)
// resolve to the DEEPER (longest-prefix) project.
func TestResolveProjectByCwdLongestPrefix(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "nested")
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"outer": {HostPath: outer},
		"inner": {HostPath: inner},
	}}
	// cwd inside inner → inner wins (longer prefix), not outer.
	key, rel, ok := resolveProjectByCwd(cfg, filepath.Join(inner, "x"))
	if !ok || key != "inner" || rel != "x" {
		t.Fatalf("nested cwd: got (%q,%q,%v) want (inner,x,true)", key, rel, ok)
	}
	// cwd in outer but NOT inner → outer wins.
	key, _, ok = resolveProjectByCwd(cfg, filepath.Join(outer, "other"))
	if !ok || key != "outer" {
		t.Fatalf("outer-only cwd: got (%q,%v) want (outer,true)", key, ok)
	}
}

// TestResolveProjectByCwdTie: two distinct projects with the SAME root (equal
// prefix length) → ambiguous → ok=false (user must pass -p explicitly, D7).
func TestResolveProjectByCwdTie(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"a": {HostPath: root},
		"b": {HostPath: root},
	}}
	if _, _, ok := resolveProjectByCwd(cfg, filepath.Join(root, "sub")); ok {
		t.Fatal("tie (two projects, same root) must not auto-detect")
	}
}

// TestResolveProjectByCwdNoMatch: a cwd outside every registered root → no match.
func TestResolveProjectByCwdNoMatch(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"siv": {HostPath: root},
	}}
	if _, _, ok := resolveProjectByCwd(cfg, other); ok {
		t.Fatalf("cwd outside all roots must not match")
	}
	// A sibling dir sharing a string prefix but NOT a path-component boundary must
	// not match (root="/tmp/x", cwd="/tmp/x-other").
	if _, _, ok := resolveProjectByCwd(cfg, root+"-other"); ok {
		t.Fatalf("string-prefix-but-not-path-prefix must not match")
	}
}

// TestResolveProjectByCwdContainerPath: the container_path 注册锚 also participates
// in matching (gofer runs in-container, so cwd may sit under container_path).
func TestResolveProjectByCwdContainerPath(t *testing.T) {
	host := t.TempDir()
	container := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"siv": {HostPath: host, ContainerPath: container},
	}}
	key, rel, ok := resolveProjectByCwd(cfg, filepath.Join(container, "src"))
	if !ok || key != "siv" || rel != "src" {
		t.Fatalf("container cwd: got (%q,%q,%v) want (siv,src,true)", key, rel, ok)
	}
}
