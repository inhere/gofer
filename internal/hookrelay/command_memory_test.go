package hookrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/tracker"
)

func cmdMemory(key, kind, content string, commands ...string) tracker.Memory {
	return tracker.Memory{Key: key, Content: content, UpdatedAt: "2026-10-08T00:00:00Z",
		MemoryMeta: tracker.MemoryMeta{Kind: kind, When: &tracker.MemoryWhen{Commands: commands}}}
}

func cmdPayload(agent, dialect, sid, tool, command string) Payload {
	return Payload{Agent: agent, Dialect: dialect, Event: "PreToolUse", SessionID: sid, Cwd: "/w/repo", ToolName: tool, ToolCommand: command}
}

// cmdOpts wires the same candidates into both the prompt and the command
// loader, sharing one state dir (as `gofer hook` does).
func cmdOpts(t *testing.T, cands []PromptMemory) Options {
	t.Helper()
	load := func(string) ([]PromptMemory, error) { return cands, nil }
	return Options{
		PromptMemories:  load,
		CommandMemories: load,
		MemoryStateDir:  t.TempDir(),
		now:             func() time.Time { return promptMemNow },
	}
}

func TestNormalizeCommand(t *testing.T) {
	cases := map[string]string{
		"  git push origin main ":                          "git push origin main",
		"env GOFLAGS=-mod=mod git push":                    "git push",
		"FOO=1 BAR='a b' git push":                         "git push",
		"cd /tmp/x && git push":                            "git push",
		`cd "/tmp/a b"; FOO=1 git push`:                    "git push",
		"cd web && env X=1 cd sub && gofer worker upgrade": "gofer worker upgrade",
		`bash -lc 'git push'`:                              "git push",
		`/bin/sh -c "cd /w && git push"`:                   "git push",
		"echo hi && git push":                              "echo hi && git push",
		"env":                                              "",
	}
	for in, want := range cases {
		if got := NormalizeCommand(in); got != want {
			t.Errorf("NormalizeCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolInputCommandShapes(t *testing.T) {
	for raw, want := range map[string]string{
		`{"command":"git push"}`:                "git push",
		`{"command":["bash","-lc","git push"]}`: "bash -lc git push",
		`{"cmd":"gofer worker upgrade"}`:        "gofer worker upgrade",
		`{"file_path":"/x"}`:                    "",
		`"not an object"`:                       "",
	} {
		if got := toolInputCommand(json.RawMessage(raw)); got != want {
			t.Errorf("toolInputCommand(%s) = %q, want %q", raw, got, want)
		}
	}
	p, err := ParsePayload(AgentClaude, strings.NewReader(`{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"cd x && git push"}}`))
	if err != nil || p.ToolCommand != "cd x && git push" || p.ToolName != "Bash" {
		t.Fatalf("payload=%+v err=%v", p, err)
	}
}

func TestCommandMemoryMatchesAndOrders(t *testing.T) {
	cands := []PromptMemory{
		{Scope: MemoryScopeRepo, Memory: cmdMemory("a-note", tracker.MemoryKindNote, "note body", "git push")},
		{Scope: MemoryScopeGlobal, Memory: cmdMemory("b-rule", tracker.MemoryKindRule, "rule body", "git")},
		{Scope: MemoryScopeRepo, Memory: cmdMemory("c-other", "", "other body", "gofer worker upgrade")},
		{Scope: MemoryScopeRepo, Memory: kwMemory("kw-only", "", "keyword body", "git push")},
	}
	res, err := Run(nil, cmdPayload(AgentClaude, "", "cm-1", "Bash", "cd /w/repo && GIT_TRACE=1 git push origin main"), cmdOpts(t, cands))
	if err != nil {
		t.Fatal(err)
	}
	ri := strings.Index(res.Context, "[gofer 记忆 · 执行 “git” 前] b-rule（全局记忆）\nrule body")
	ni := strings.Index(res.Context, "[gofer 记忆 · 执行 “git push” 前] a-note\nnote body")
	if ri < 0 || ni < 0 || ri > ni {
		t.Fatalf("want rule then note, got %q", res.Context)
	}
	for _, bad := range []string{"other body", "keyword body"} {
		if strings.Contains(res.Context, bad) {
			t.Fatalf("context must not contain %q: %q", bad, res.Context)
		}
	}
}

func TestCommandMemoryIgnoresNonShellAndUnsupported(t *testing.T) {
	cands := []PromptMemory{{Scope: MemoryScopeRepo, Memory: cmdMemory("push", "", "body", "git push")}}
	opts := cmdOpts(t, cands)
	for _, p := range []Payload{
		cmdPayload(AgentClaude, "", "cm-ns", "Edit", "git push"),
		cmdPayload(AgentClaude, "", "cm-ns", "Read", "git push"),
		cmdPayload(AgentClaude, "", "cm-ns", "Bash", ""),
		cmdPayload(AgentOmp, "", "cm-ns", "Bash", "git push"),
		cmdPayload(AgentJcode, "", "cm-ns", "Bash", "git push"),
		cmdPayload(AgentClaude, "", "", "Bash", "git push"),
	} {
		if res, _ := Run(nil, p, opts); res.Context != "" {
			t.Fatalf("%+v must inject nothing: %q", p, res.Context)
		}
	}
	// codex / generic shell tool names work.
	if res, _ := Run(nil, cmdPayload(AgentCodex, "", "cm-codex", "shell", "git push"), opts); !strings.Contains(res.Context, "body") {
		t.Fatalf("codex shell: %q", res.Context)
	}
	if res, _ := Run(nil, cmdPayload("mybot", DialectGeneric, "cm-gen", "exec_command", "git push"), opts); !strings.Contains(res.Context, "body") {
		t.Fatalf("generic exec_command: %q", res.Context)
	}
	// No loader (switch off at the command layer) → nothing.
	off := opts
	off.CommandMemories = nil
	if res, _ := Run(nil, cmdPayload(AgentClaude, "", "cm-off", "Bash", "git push"), off); res.Context != "" {
		t.Fatalf("nil loader: %q", res.Context)
	}
}

func TestCommandMemorySharesOncePerSessionWithPrompt(t *testing.T) {
	m := cmdMemory("release", tracker.MemoryKindRule, "release steps", "git push")
	m.When.Keywords = []string{"发版"}
	opts := cmdOpts(t, []PromptMemory{{Scope: MemoryScopeRepo, Memory: m}})

	// Prompt first → the command later in the same session stays quiet.
	if res, _ := Run(newFake(), promptPayload(AgentClaude, "", "cs-1", "准备发版"), opts); !strings.Contains(res.Context, "release steps") {
		t.Fatalf("prompt should inject: %q", res.Context)
	}
	if res, _ := Run(nil, cmdPayload(AgentClaude, "", "cs-1", "Bash", "git push"), opts); res.Context != "" {
		t.Fatalf("already injected on prompt: %q", res.Context)
	}
	// Command first → the prompt later stays quiet, and the command only once.
	if res, _ := Run(nil, cmdPayload(AgentClaude, "", "cs-2", "Bash", "git push"), opts); !strings.Contains(res.Context, "release steps") {
		t.Fatalf("command should inject: %q", res.Context)
	}
	if res, _ := Run(nil, cmdPayload(AgentClaude, "", "cs-2", "Bash", "git push --tags"), opts); res.Context != "" {
		t.Fatalf("second command must not re-inject: %q", res.Context)
	}
	if res, _ := Run(newFake(), promptPayload(AgentClaude, "", "cs-2", "发版"), opts); res.Context != "" {
		t.Fatalf("already injected on command: %q", res.Context)
	}
}

func TestCommandMemoryBudget(t *testing.T) {
	big := strings.Repeat("长", 600) // 1800 bytes
	a := cmdMemory("a-big", "", big, "git push")
	b := cmdMemory("b-big", "", big, "git push")
	b.Summary = "b summary"
	opts := cmdOpts(t, []PromptMemory{{Scope: MemoryScopeRepo, Memory: a}, {Scope: MemoryScopeRepo, Memory: b}})
	res, _ := Run(nil, cmdPayload(AgentClaude, "", "cb-1", "Bash", "git push"), opts)
	if !strings.Contains(res.Context, big) || !strings.Contains(res.Context, "摘要：b summary") ||
		!strings.Contains(res.Context, "`gofer memory show b-big` 看全文") {
		t.Fatalf("budget rendering: %q", res.Context)
	}
	if len(res.Context) > DefaultPromptMemoryBudget+200 {
		t.Fatalf("context too large: %d", len(res.Context))
	}
}

func TestCommandMemoryDeadline(t *testing.T) {
	cands := []PromptMemory{{Scope: MemoryScopeRepo, Memory: cmdMemory("slow", "", "slow body", "git push")}}
	opts := cmdOpts(t, cands)
	release := make(chan struct{})
	defer close(release)
	opts.CommandMemories = func(string) ([]PromptMemory, error) {
		<-release
		return cands, nil
	}
	opts.CommandDeadline = 30 * time.Millisecond
	start := time.Now()
	res, err := Run(nil, cmdPayload(AgentClaude, "", "cd-1", "Bash", "git push"), opts)
	if err != nil || res.Context != "" {
		t.Fatalf("deadline: res=%q err=%v", res.Context, err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("deadline not honoured: %s", el)
	}
	// Nothing was recorded: the next command (fast loader) still injects.
	opts.CommandMemories = func(string) ([]PromptMemory, error) { return cands, nil }
	if res, _ := Run(nil, cmdPayload(AgentClaude, "", "cd-1", "Bash", "git push"), opts); !strings.Contains(res.Context, "slow body") {
		t.Fatalf("timed-out run must not mark the memory injected: %q", res.Context)
	}
}

func TestCommandMemoryLoaderSwitch(t *testing.T) {
	line := `{"key":"push","content":"push body","updated_at":"2026-10-08T00:00:00Z","by":"t","when":{"commands":["git push"]}}`
	off := writeTracker(t, "prefix: x\nprime:\n  inject_on_command: false\n", line)
	if got, err := NewCommandMemoryLoader(nil, nil)(off); err != nil || len(got) != 0 {
		t.Fatalf("inject_on_command=false: got=%v err=%v", got, err)
	}
	// The prompt switch is independent.
	if got, _ := NewMemoryLoader(nil, nil)(off); len(got) != 1 {
		t.Fatalf("prompt loader unaffected: %v", got)
	}
	on := writeTracker(t, "prefix: x\n", line)
	if got, _ := NewCommandMemoryLoader(nil, nil)(on); len(got) != 1 || got[0].Memory.Key != "push" {
		t.Fatalf("default on: %v", got)
	}
}

func TestInstallCommandMemoryIdempotent(t *testing.T) {
	for _, agent := range []string{AgentClaude, AgentCodex} {
		root := t.TempDir()
		path, _ := ConfigFileFor(agent, root)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		orig := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"other guard"}]}]}}`
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if changed, err := InstallCommandMemory(agent, root); err != nil || !changed {
			t.Fatalf("%s install: changed=%v err=%v", agent, changed, err)
		}
		if changed, err := InstallCommandMemory(agent, root); err != nil || changed {
			t.Fatalf("%s repeat install must be a no-op: changed=%v err=%v", agent, changed, err)
		}
		raw, _ := os.ReadFile(path)
		if strings.Count(string(raw), "gofer hook "+agent) != 1 || !strings.Contains(string(raw), "other guard") {
			t.Fatalf("%s install result:\n%s", agent, raw)
		}
		// A full relay install keeps exactly one gofer PreToolUse entry.
		if _, err := Install(agent, path, false, false); err != nil {
			t.Fatal(err)
		}
		if changed, _ := InstallCommandMemory(agent, root); changed {
			t.Fatalf("%s: relay install already carries the entry", agent)
		}
		var doc map[string]any
		raw, _ = os.ReadFile(path)
		_ = json.Unmarshal(raw, &doc)
		pre := doc["hooks"].(map[string]any)["PreToolUse"].([]any)
		if len(pre) != 2 { // foreign guard + gofer
			t.Fatalf("%s PreToolUse entries=%d:\n%s", agent, len(pre), raw)
		}
		// Prime-only remove leaves the relay install's entry alone...
		if changed, _ := RemoveCommandMemory(agent, root); changed {
			t.Fatalf("%s: must not strip the relay install's PreToolUse", agent)
		}
		// ...but takes out a memory-only entry.
		if _, err := Install(agent, path, true, false); err != nil {
			t.Fatal(err)
		}
		if changed, _ := InstallCommandMemory(agent, root); !changed {
			t.Fatalf("%s reinstall after relay removal", agent)
		}
		if changed, err := RemoveCommandMemory(agent, root); err != nil || !changed {
			t.Fatalf("%s memory-only remove: changed=%v err=%v", agent, changed, err)
		}
		raw, _ = os.ReadFile(path)
		if strings.Contains(string(raw), "gofer hook") || !strings.Contains(string(raw), "other guard") {
			t.Fatalf("%s remove result:\n%s", agent, raw)
		}
	}
	if changed, err := InstallCommandMemory(AgentOmp, t.TempDir()); err != nil || changed {
		t.Fatalf("omp has no JSON hooks: changed=%v err=%v", changed, err)
	}
}
