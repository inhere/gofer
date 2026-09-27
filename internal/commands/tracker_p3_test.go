package commands

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/tracker"
)

func p3File(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func p3Write(t *testing.T, root, rel, value string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrimeSectionsAndCap(t *testing.T) {
	if out, code := trackerCLI(t, t.TempDir(), "repo", "prime", "--hook-json"); code != 0 || !strings.Contains(out, `"additionalContext":""`) {
		t.Fatalf("no tracker hook must return empty context, exit=%d out=%q", code, out)
	}
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	s := tracker.NewStore(filepath.Join(root, ".gofer", "tracker"))
	if err := s.WriteIssues([]tracker.Issue{
		{ID: "p3-a", Title: "Active", Status: "in_progress", Assignee: "sample", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "p3-b", Title: "Ready", Status: "open", CreatedAt: "2026-01-02T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMemories(func(_ []tracker.Memory) ([]tracker.Memory, error) {
		return []tracker.Memory{{Key: "old", Content: strings.Repeat("o", 5000), UpdatedAt: "2026-01-01T00:00:00Z"}, {Key: "latest", Content: strings.Repeat("n", 5000), UpdatedAt: "2026-01-02T00:00:00Z"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []struct{ name, want string }{
		{"local-commit", "按功能点本地提交是默认授权"}, {"ask", "提交前"}, {"none", "不提交"},
	} {
		cfg := p3File(t, root, ".gofer/tracker/config.yaml")
		for _, old := range []string{"local-commit", "ask", "none"} {
			cfg = strings.ReplaceAll(cfg, "commit_policy: "+old, "commit_policy: "+policy.name)
		}
		p3Write(t, root, ".gofer/tracker/config.yaml", cfg)
		out := trackerRunOK(t, root, "repo", "prime")
		if len([]byte(out)) > 8192 || !strings.Contains(out, policy.want) || !strings.Contains(out, "latest") || !strings.Contains(out, "截断") {
			t.Fatalf("policy=%s prime bytes=%d lacks policy/latest/truncation: %q", policy.name, len([]byte(out)), out)
		}
		order := []string{"提交策略", "进行中", "ready", "memory"}
		last := -1
		for _, section := range order {
			at := strings.Index(strings.ToLower(out), strings.ToLower(section))
			if at <= last {
				t.Fatalf("sections out of order %q: %q", section, out)
			}
			last = at
		}
	}
}

func TestPrimeHookJSONShape(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	out := trackerRunOK(t, root, "repo", "prime", "--hook-json")
	var payload struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil || !strings.Contains(payload.HookSpecificOutput.AdditionalContext, "提交策略") {
		t.Fatalf("invalid Claude SessionStart JSON: %q err=%v", out, err)
	}
}

func TestRepoInitInstallsHooks(t *testing.T) {
	root := t.TempDir()
	p3Write(t, root, ".claude/settings.json", `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"other-tool"}]}]}}`)
	trackerRunOK(t, root, "repo", "init")
	trackerRunOK(t, root, "repo", "init")
	for _, rel := range []string{".claude/settings.json", ".codex/hooks.json"} {
		body := p3File(t, root, rel)
		if strings.Count(body, "gofer repo prime --hook-json") != 1 {
			t.Fatalf("%s prime hook count != 1: %s", rel, body)
		}
		if rel == ".claude/settings.json" && !strings.Contains(body, "other-tool") {
			t.Fatalf("foreign hook lost: %s", body)
		}
	}
	if out := trackerRunOK(t, root, "repo", "status"); !strings.Contains(out, "hooks: claude=true codex=true") {
		t.Fatalf("status does not detect hooks: %s", out)
	}
	bare := t.TempDir()
	trackerRunOK(t, bare, "repo", "init", "--no-hooks")
	if _, err := os.Stat(filepath.Join(bare, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("--no-hooks created Claude settings: %v", err)
	}
}

func TestIssueTagsFilterAndQuery(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	out := trackerRunOK(t, root, "issue", "create", "-t", "title needle", "--description", "body", "--tag", "alpha,beta", "--json")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("create=%q err=%v", out, err)
	}
	trackerRunOK(t, root, "issue", "create", "-t", "other", "--description", "description needle", "--tag", "alpha", "--json")
	trackerRunOK(t, root, "issue", "update", created.ID, "--tag", "gamma", "--untag", "beta", "--json")
	issueJSON := p3File(t, root, ".gofer/tracker/issues.jsonl")
	if strings.Contains(issueJSON, `"labels"`) || !strings.Contains(issueJSON, `"tags"`) {
		t.Fatalf("issue JSONL must use tags only: %s", issueJSON)
	}
	out = trackerRunOK(t, root, "issue", "ls", "--tag", "alpha", "--tag", "gamma", "-q", "needle", "--json")
	if !strings.Contains(out, created.ID) || strings.Contains(out, `"beta"`) || strings.Contains(out, `"other"`) {
		t.Fatalf("tag intersection/query/untag failed: %s", out)
	}
	if out, code := trackerCLI(t, root, "issue", "ls", "--label", "alpha"); code == 0 {
		t.Fatalf("legacy --label still accepted: %s", out)
	}
}

func TestMemoryTags(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	trackerRunOK(t, root, "memory", "set", "first", "shared needle", "--tag", "alpha,beta")
	trackerRunOK(t, root, "memory", "set", "second", "shared needle", "--tag", "alpha")
	body := p3File(t, root, ".gofer/tracker/memories.jsonl")
	if !strings.Contains(body, `"tags":["alpha","beta"]`) {
		t.Fatalf("memory tags not stored: %s", body)
	}
	out := trackerRunOK(t, root, "memory", "ls", "needle", "--tag", "beta", "--json")
	if !strings.Contains(out, "first") || strings.Contains(out, "second") {
		t.Fatalf("memory tag+keyword filter: %s", out)
	}
}

func p3BdFixture(t *testing.T, root string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "tracker", "testdata", "bd-issues.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	p3Write(t, root, ".beads/issues.jsonl", string(b))
}

func TestMigrateFromBdFixture(t *testing.T) {
	root := t.TempDir()
	p3BdFixture(t, root)
	before := p3File(t, root, ".beads/issues.jsonl")
	if out := trackerRunOK(t, root, "repo", "migrate", "--from-bd"); !strings.Contains(strings.ToLower(out), "dry") {
		t.Fatalf("dry-run plan missing: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".gofer")); !os.IsNotExist(err) || p3File(t, root, ".beads/issues.jsonl") != before {
		t.Fatalf("dry-run wrote files: %v", err)
	}
	trackerRunOK(t, root, "repo", "migrate", "--from-bd", "--apply")
	first := p3File(t, root, ".gofer/tracker/issues.jsonl")
	if !strings.Contains(first, `"tags":["alpha","beta","bd:reviewing"]`) || strings.Contains(first, `"labels"`) || !strings.Contains(first, `"parent":"demo-a"`) || !strings.Contains(first, `"deps"`) || !strings.Contains(first, `"close_reason":"done"`) {
		t.Fatalf("bd field mapping incomplete: %s", first)
	}
	trackerRunOK(t, root, "repo", "migrate", "--from-bd", "--apply")
	if second := p3File(t, root, ".gofer/tracker/issues.jsonl"); second != first {
		t.Fatalf("repeat migration changed issue data: before=%q after=%q", first, second)
	}
}

func TestMigrateStripsBeadsBlock(t *testing.T) {
	root := t.TempDir()
	p3BdFixture(t, root)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		p3Write(t, root, name, "header\n<!-- BEGIN BEADS INTEGRATION v:1 -->\nUse bd.\n<!-- END BEADS INTEGRATION -->\nfooter\n")
	}
	p3Write(t, root, ".claude/settings.json", `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"bd prime --hook-json"},{"type":"command","command":"other-tool"}]}]}}`)
	p3Write(t, root, ".codex/hooks.json", `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"bd prime --hook-json"}]}]}}`)
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	cmd = exec.Command("git", "config", "core.hooksPath", ".beads/hooks")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v %s", err, out)
	}
	trackerRunOK(t, root, "repo", "migrate", "--from-bd", "--apply")
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		body := p3File(t, root, name)
		if strings.Contains(body, "BEGIN BEADS INTEGRATION") || strings.Count(body, "BEGIN GOFER TRACKER") != 1 || !strings.Contains(body, "footer") {
			t.Fatalf("managed block replacement %s: %s", name, body)
		}
	}
	for _, rel := range []string{".claude/settings.json", ".codex/hooks.json"} {
		body := p3File(t, root, rel)
		if strings.Contains(body, "bd prime --hook-json") || strings.Count(body, "gofer repo prime --hook-json") != 1 {
			t.Fatalf("hook migration %s: %s", rel, body)
		}
		if rel == ".claude/settings.json" && !strings.Contains(body, "other-tool") {
			t.Fatalf("foreign hook lost: %s", body)
		}
	}
	cmd = exec.Command("git", "config", "--local", "--get", "core.hooksPath")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err == nil || len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("core.hooksPath remains: %q err=%v", out, err)
	}
	if p3File(t, root, ".beads/issues.jsonl") == "" {
		t.Fatal(".beads archive removed")
	}
}
