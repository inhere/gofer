package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func trackerCLI(t *testing.T, cwd string, args ...string) (string, int) {
	t.Helper()
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(before)
	args = append([]string{"-c", filepath.Join(cwd, "test-config.yaml")}, args...)
	var code int
	out := captureOutput(t, func() { code = NewApp("test").Run(args) })
	return out, code
}

func trackerRunOK(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	out, code := trackerCLI(t, cwd, args...)
	if code != 0 {
		t.Fatalf("gofer %s exit=%d: %s", strings.Join(args, " "), code, out)
	}
	return out
}

func TestMemoryCLIScopeFlags(t *testing.T) {
	root := t.TempDir()
	out, code := trackerCLI(t, root, "memory", "set", "--global", "--project", "proj", "key", "content")
	if code == 0 || !strings.Contains(out, "--global and --project are mutually exclusive") {
		t.Fatalf("scope conflict code=%d output=%q", code, out)
	}
}

func TestRepoInitIdempotent(t *testing.T) {
	root := t.TempDir()
	agents := filepath.Join(root, "AGENTS.md")
	beads := "<!-- BEGIN BEADS INTEGRATION v:1 -->\nUse bd.\n<!-- END BEADS INTEGRATION -->\n"
	if err := os.WriteFile(agents, []byte(beads), 0o644); err != nil {
		t.Fatal(err)
	}
	first := trackerRunOK(t, root, "repo", "init", "--prefix", "sample")
	if !strings.Contains(first, "repo migrate --from-bd") {
		t.Fatalf("missing BEADS migration hint: %s", first)
	}
	paths := []string{filepath.Join(root, ".gofer", "tracker", "config.yaml"), filepath.Join(root, ".gofer", ".gitignore"), agents}
	before := make([]string, len(paths))
	for i, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = string(b)
	}
	trackerRunOK(t, root, "repo", "init", "--prefix", "sample")
	for i, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != before[i] {
			t.Fatalf("init changed %s: %v", p, err)
		}
	}
	if !strings.Contains(before[2], beads) || strings.Count(before[2], "<!-- BEGIN GOFER TRACKER v:1 -->") != 1 || strings.Count(before[1], "tracker/.local/") != 1 {
		t.Fatal("BEADS block changed or managed block/gitignore duplicated")
	}
	for _, name := range []string{"issues.jsonl", "memories.jsonl"} {
		b, err := os.ReadFile(filepath.Join(root, ".gofer", "tracker", name))
		if err != nil || len(b) != 0 {
			t.Fatalf("initial %s = %q, %v", name, b, err)
		}
	}
}

func TestTrackerDiscovery(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init", "--prefix", "top")
	child := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	out := trackerRunOK(t, child, "repo", "status")
	if !strings.Contains(out, filepath.Join(root, ".gofer", "tracker")) {
		t.Fatalf("did not discover ancestor: %s", out)
	}
	other := t.TempDir()
	trackerRunOK(t, other, "repo", "init", "--prefix", "other")
	out = trackerRunOK(t, child, "repo", "status", "--tracker", filepath.Join(other, ".gofer", "tracker"))
	if !strings.Contains(out, filepath.Join(other, ".gofer", "tracker")) {
		t.Fatalf("--tracker did not override ancestor: %s", out)
	}
}

func TestMemoryCRUD(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	trackerRunOK(t, root, "memory", "set", "alpha", "first content")
	trackerRunOK(t, root, "memory", "remember", "beta", "second content")
	out := trackerRunOK(t, root, "memory", "ls", "second")
	if !strings.Contains(out, "beta") || strings.Contains(out, "alpha") {
		t.Fatalf("keyword filter: %s", out)
	}
	out = trackerRunOK(t, root, "memory", "show", "alpha")
	if !strings.Contains(out, "first content") {
		t.Fatalf("show: %s", out)
	}
	trackerRunOK(t, root, "memory", "forget", "alpha")
	out, code := trackerCLI(t, root, "memory", "show", "alpha")
	if code == 0 || !strings.Contains(strings.ToLower(out), "alpha") {
		t.Fatalf("removed key still found: code=%d out=%s", code, out)
	}
	trackerRunOK(t, root, "memory", "rm", "beta")
}

func TestIssueCLILocalFlow(t *testing.T) {
	root := t.TempDir()
	if out, code := trackerCLI(t, root, "issue", "ready"); code == 0 || !strings.Contains(out, "gofer repo init") {
		t.Fatalf("missing tracker must give init hint: code=%d out=%s", code, out)
	}
	trackerRunOK(t, root, "repo", "init", "--prefix", "flow")
	t.Setenv("GOFER_CALLER", "cli-tester")
	create := func(title string) string {
		t.Helper()
		out := trackerRunOK(t, root, "issue", "create", "-t", title, "--json")
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &item); err != nil || item.ID == "" {
			t.Fatalf("create JSON=%q err=%v", out, err)
		}
		return item.ID
	}
	a, b := create("A"), create("B")
	trackerRunOK(t, root, "issue", "dep", "add", b, a)
	if out := trackerRunOK(t, root, "issue", "ready", "--json"); strings.Contains(out, b) || !strings.Contains(out, a) {
		t.Fatalf("blocked issue in ready: %s", out)
	}
	out := trackerRunOK(t, root, "issue", "update", b, "--claim", "--append-notes", "working", "--json")
	if !strings.Contains(out, `"assignee":"cli-tester"`) || !strings.Contains(out, `"text":"working"`) || !strings.Contains(out, `"started_at":`) {
		t.Fatalf("claim/notes missing: %s", out)
	}
	trackerRunOK(t, root, "issue", "close", a, "--reason", "done")
	trackerRunOK(t, root, "issue", "update", b, "--status", "open")
	if out := trackerRunOK(t, root, "issue", "ready", "--json"); !strings.Contains(out, b) {
		t.Fatalf("unblocked issue missing: %s", out)
	}
}
