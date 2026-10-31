package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/hookrelay"
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
		// Only prime-tagged memories remain full text; keep this fixture large enough
		// to exercise the 8 KiB cap. Untagged summaries are covered separately.
		return []tracker.Memory{{Key: "old", Content: strings.Repeat("o", 5000), Tags: []string{"prime"}, UpdatedAt: "2026-01-01T00:00:00Z"}, {Key: "latest", Content: strings.Repeat("n", 5000), Tags: []string{"prime"}, UpdatedAt: "2026-01-02T00:00:00Z"}}, nil
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
		if len([]byte(out)) > 8192 || !strings.Contains(out, policy.want) || !strings.Contains(out, "latest（规则）") || !strings.Contains(out, "请精简规则") {
			t.Fatalf("policy=%s prime bytes=%d lacks policy/latest/truncation: %q", policy.name, len([]byte(out)), out)
		}
		order := []string{"## 提交策略", "## 规则", "## 进行中", "## ready", "## 记忆索引"}
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
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	// Claude Code attributes hookSpecificOutput by hookEventName; without it the
	// context is not injected (bd prime --hook-json emits the same shape).
	if err := json.Unmarshal([]byte(out), &payload); err != nil ||
		payload.HookSpecificOutput.HookEventName != "SessionStart" ||
		!strings.Contains(payload.HookSpecificOutput.AdditionalContext, "提交策略") {
		t.Fatalf("invalid Claude SessionStart JSON: %q err=%v", out, err)
	}
}

func TestRepoStatusSuggestsProjectKeyWhenEmpty(t *testing.T) {
	t.Setenv("GOFER_SERVER_ADDR", "http://127.0.0.1:1")
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	out := trackerRunOK(t, root, "repo", "status")
	if !strings.Contains(out, "project_key: 未填写") || !strings.Contains(out, "config.yaml") {
		t.Fatalf("status should explain how to fill project_key: %s", out)
	}
}

func TestPrimeUsesTrackerProjectKeyWithoutClientProjectsConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/plans":
			_, _ = w.Write([]byte(`{"plans":[{"plan_id":"p1","status":"open","project":"proj","updated_at":2}],"total":1}`))
		case "/v1/plans/p1/handoff":
			_, _ = w.Write([]byte(`{"plan_id":"p1","version":1,"body":"from project key","by":"tester","at":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("GOFER_SERVER_ADDR", server.URL)

	root := t.TempDir()
	s, _, err := tracker.Init(root, "prime-project-key", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectKey("proj"); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	p3Write(t, root, "config.yaml", "server: {}\n")
	t.Chdir(root)
	widenPrimeServerBudget(t)
	body, err := primeWithServerHandoffs(s, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## 进行中 plan") || !strings.Contains(body, "from project key") {
		t.Fatalf("prime did not use tracker project_key without client projects config: %q", body)
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
	// bd-style -l/--label is an alias of --tag (create/update/ls).
	if out := trackerRunOK(t, root, "issue", "ls", "--label", "alpha", "--json"); !strings.Contains(out, created.ID) || !strings.Contains(out, `"other"`) {
		t.Fatalf("--label alias for ls: %s", out)
	}
	if out := trackerRunOK(t, root, "issue", "ls", "-l", "gamma", "--json"); !strings.Contains(out, created.ID) || strings.Contains(out, `"other"`) {
		t.Fatalf("-l alias for ls: %s", out)
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

// TestPrimeWorkHintNeedsRelayOrWork: `repo prime` carries the `gofer work report`
// line only once the session relay hooks are installed (here: project level);
// a plain `repo init` (command-memory hook only, no server) leaves it out.
func TestPrimeWorkHintNeedsRelayOrWork(t *testing.T) {
	t.Setenv("GOFER_SERVER_ADDR", "")
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init")
	if out := trackerRunOK(t, root, "repo", "prime"); strings.Contains(out, "gofer work report") {
		t.Fatalf("work hint without relay hooks or work items:\n%s", out)
	}
	path, err := hookrelay.ConfigFileFor(hookrelay.AgentClaude, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hookrelay.Install(hookrelay.AgentClaude, path, false, false); err != nil {
		t.Fatal(err)
	}
	if out := trackerRunOK(t, root, "repo", "prime"); strings.Count(out, "gofer work report") != 1 {
		t.Fatalf("relay hooks installed: want the work hint once:\n%s", out)
	}
}
