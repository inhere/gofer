package hookrelay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/tracker"
)

var promptMemNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func kwMemory(key, kind, content string, keywords ...string) tracker.Memory {
	return tracker.Memory{Key: key, Content: content, UpdatedAt: "2026-10-08T00:00:00Z",
		MemoryMeta: tracker.MemoryMeta{Kind: kind, When: &tracker.MemoryWhen{Keywords: keywords}}}
}

func promptMemOpts(t *testing.T, cands []PromptMemory) Options {
	t.Helper()
	return Options{
		PromptMemories: func(string) ([]PromptMemory, error) { return cands, nil },
		MemoryStateDir: t.TempDir(),
		now:            func() time.Time { return promptMemNow },
	}
}

func promptPayload(agent, dialect, sid, prompt string) Payload {
	return Payload{Agent: agent, Dialect: dialect, Event: "UserPromptSubmit", SessionID: sid, Cwd: "/w/repo", Prompt: prompt}
}

func TestPromptMemoryMatchesKeywordsAndOrders(t *testing.T) {
	expired := kwMemory("old-handoff", tracker.MemoryKindHandoff, "stale handoff", "发版")
	expired.ExpiresAt = "2026-10-01T00:00:00Z"
	codexOnly := kwMemory("codex-only", "", "codex text", "发版")
	codexOnly.Tags = []string{"agent:codex"}
	cands := []PromptMemory{
		{Scope: MemoryScopeRepo, Memory: kwMemory("a-note", tracker.MemoryKindNote, "note body", "RELEASE")},
		{Scope: MemoryScopeGlobal, Memory: kwMemory("b-rule", tracker.MemoryKindRule, "rule body", "发版")},
		{Scope: MemoryScopeRepo, Memory: kwMemory("c-other", "", "other body", "tunnel")},
		{Scope: MemoryScopeRepo, Memory: expired},
		{Scope: MemoryScopeRepo, Memory: codexOnly},
		{Scope: MemoryScopeRepo, Memory: tracker.Memory{Key: "no-when", Content: "发版 without trigger"}},
	}
	api := newFake()
	res, err := Run(api, promptPayload(AgentClaude, "", "pm-1", "今天准备发版, release notes?"), promptMemOpts(t, cands))
	if err != nil {
		t.Fatal(err)
	}
	ctx := res.Context
	ri := strings.Index(ctx, "[gofer 记忆 · 因“发版”命中] b-rule（全局记忆）\nrule body")
	ni := strings.Index(ctx, "[gofer 记忆 · 因“RELEASE”命中] a-note\nnote body")
	if ri < 0 || ni < 0 || ri > ni {
		t.Fatalf("want rule then note, got %q", ctx)
	}
	for _, bad := range []string{"other body", "stale handoff", "codex text", "no-when"} {
		if strings.Contains(ctx, bad) {
			t.Fatalf("context must not contain %q: %q", bad, ctx)
		}
	}
}

func TestPromptMemoryOncePerSession(t *testing.T) {
	cands := []PromptMemory{{Scope: MemoryScopeRepo, Memory: kwMemory("release-flow", "", "step 1", "发版")}}
	opts := promptMemOpts(t, cands)
	api := newFake()
	first, _ := Run(api, promptPayload(AgentClaude, "", "pm-once", "发版"), opts)
	if !strings.Contains(first.Context, "release-flow") {
		t.Fatalf("first prompt should inject: %q", first.Context)
	}
	// A new hook process (Run builds a fresh runner) reads the persisted state.
	again, _ := Run(api, promptPayload(AgentClaude, "", "pm-once", "再发版一次"), opts)
	if again.Context != "" {
		t.Fatalf("second prompt of the same session must not re-inject: %q", again.Context)
	}
	other, _ := Run(api, promptPayload(AgentClaude, "", "pm-other", "发版"), opts)
	if !strings.Contains(other.Context, "release-flow") {
		t.Fatalf("another session injects again: %q", other.Context)
	}
}

func TestPromptMemoryBudgetFallsBackToSummary(t *testing.T) {
	big := strings.Repeat("x", 900)
	a := kwMemory("a", "", big, "deploy")
	b := kwMemory("b", "", big, "deploy")
	c := kwMemory("c", "", big, "deploy")
	c.Summary = "c 的摘要"
	g := kwMemory("g", "", big, "deploy")
	cands := []PromptMemory{{Memory: a}, {Memory: b}, {Memory: c}, {Scope: MemoryScopeProject, ScopeKey: "proj", Memory: g}}
	res, _ := Run(newFake(), promptPayload(AgentCodex, "", "pm-budget", "deploy now"), promptMemOpts(t, cands))
	ctx := res.Context
	if strings.Count(ctx, big) != 2 {
		t.Fatalf("want 2 full memories within 2KB, got %d: %q", strings.Count(ctx, big), ctx)
	}
	if !strings.Contains(ctx, "摘要：c 的摘要") || !strings.Contains(ctx, "`gofer memory show c` 看全文") {
		t.Fatalf("over-budget memory needs summary + show hint: %q", ctx)
	}
	if !strings.Contains(ctx, "`gofer memory show --project proj g` 看全文") {
		t.Fatalf("scoped show hint missing: %q", ctx)
	}
	if len(ctx) > DefaultPromptMemoryBudget+600 {
		t.Fatalf("context too large: %d", len(ctx))
	}
}

func TestPromptMemoryIgnoresHarnessAndInjectedPrompts(t *testing.T) {
	cands := []PromptMemory{{Memory: kwMemory("release-flow", "", "step 1", "发版")}}
	opts := promptMemOpts(t, cands)
	for _, p := range []Payload{
		promptPayload(AgentClaude, "", "pm-h1", "<system-reminder>发版</system-reminder>"),
		promptPayload(AgentClaude, "", "pm-h2", ReplyPrefix+"去发版"),
		promptPayload(AgentClaude, "", "pm-h3", JobDoneTag+" 发版 job"),
		func() Payload {
			p := promptPayload("myagent", DialectGeneric, "pm-h4", "发版")
			p.Injected = true
			return p
		}(),
	} {
		res, _ := Run(newFake(), p, opts)
		if res.Context != "" {
			t.Fatalf("%s: harness prompt must not inject: %q", p.SessionID, res.Context)
		}
	}
}

func TestPromptMemoryDialects(t *testing.T) {
	cands := []PromptMemory{{Memory: kwMemory("release-flow", "", "step 1", "发版")}}
	cases := []struct {
		agent, dialect string
		want           bool
	}{
		{AgentClaude, "", true},
		{AgentCodex, "", true},
		{"myagent", DialectGeneric, true},
		{AgentOmp, "", false},
		{AgentJcode, "", false},
	}
	for _, tc := range cases {
		res, _ := Run(newFake(), promptPayload(tc.agent, tc.dialect, "pm-d-"+tc.agent, "发版"), promptMemOpts(t, cands))
		if got := strings.Contains(res.Context, "release-flow"); got != tc.want {
			t.Fatalf("%s/%s: injected=%v want %v (%q)", tc.agent, tc.dialect, got, tc.want, res.Context)
		}
	}
}

func TestPromptMemoryAppendsToCatchUpContext(t *testing.T) {
	opts := promptMemOpts(t, []PromptMemory{{Memory: kwMemory("release-flow", "", "step 1", "发版")}}).withDefaults()
	r := &runner{api: newFake(), p: promptPayload(AgentClaude, "", "pm-merge", "发版"), opts: opts, log: func(string, ...any) {}}
	res := r.injectPromptMemories(Result{Context: JobDoneTag + " job-1"})
	if !strings.HasPrefix(res.Context, JobDoneTag+" job-1\n\n"+MemoryInjectHeader+"\n[gofer 记忆 · 因“发版”命中] release-flow") {
		t.Fatalf("memory context must follow the catch-up notice: %q", res.Context)
	}
}

func TestPromptMemoryLoaderErrorKeepsPartialAndHeartbeatFailureStillInjects(t *testing.T) {
	api := newFake()
	api.failHeartbeat = errors.New("hub down")
	opts := promptMemOpts(t, nil)
	opts.PromptMemories = func(string) ([]PromptMemory, error) {
		return []PromptMemory{{Memory: kwMemory("local", "", "local body", "发版")}}, errors.New("global memories: dial tcp: refused")
	}
	res, _ := Run(api, promptPayload(AgentClaude, "", "pm-err", "发版"), opts)
	if !strings.Contains(res.Context, "local body") {
		t.Fatalf("local memories survive a server failure: %q", res.Context)
	}
}

func TestPromptMemoryStatePruned(t *testing.T) {
	opts := promptMemOpts(t, []PromptMemory{{Memory: kwMemory("k", "", "body", "发版")}})
	old := filepath.Join(opts.MemoryStateDir, "old.json")
	fresh := filepath.Join(opts.MemoryStateDir, "fresh.json")
	for _, f := range []string{old, fresh} {
		if err := os.WriteFile(f, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Chtimes(old, promptMemNow.Add(-8*24*time.Hour), promptMemNow.Add(-8*24*time.Hour))
	_ = os.Chtimes(fresh, promptMemNow.Add(-time.Hour), promptMemNow.Add(-time.Hour))
	if res, _ := Run(newFake(), promptPayload(AgentClaude, "", "pm-prune", "发版"), opts); res.Context == "" {
		t.Fatal("expected injection")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old state should be pruned: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh state must stay: %v", err)
	}
}

// fakeLister scripts the server memory lists of NewMemoryLoader.
type fakeLister struct {
	global, project []client.ScopedMemory
	err             error
	calls           []client.ScopedMemoryListOpts
}

func (f *fakeLister) ListScopedMemories(opts client.ScopedMemoryListOpts) ([]client.ScopedMemory, error) {
	f.calls = append(f.calls, opts)
	if f.err != nil {
		return nil, f.err
	}
	if opts.Scope == MemoryScopeProject {
		return f.project, nil
	}
	return f.global, nil
}

func writeTracker(t *testing.T, config string, memories ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".gofer", "tracker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "memories.jsonl"), []byte(strings.Join(memories, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "web", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	return sub
}

const repoMemLine = `{"key":"repo-release","content":"repo body","updated_at":"2026-10-08T00:00:00Z","by":"t","when":{"keywords":["发版"]}}`

func TestMemoryLoaderReadsRepoAndScoped(t *testing.T) {
	cwd := writeTracker(t, "prefix: x\nproject_key: proj\n", repoMemLine)
	lister := &fakeLister{
		global:  []client.ScopedMemory{{Scope: "global", Key: "g", Content: "g body"}, {Scope: "global", Key: "gone", Deleted: true}},
		project: []client.ScopedMemory{{Scope: "project", ScopeKey: "proj", Key: "p", Content: "p body"}},
	}
	got, err := NewMemoryLoader(lister, func(string) string { return "ignored" })(cwd)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, m := range got {
		ids = append(ids, m.id())
	}
	if strings.Join(ids, ",") != "repo/repo-release,global/g,project:proj/p" {
		t.Fatalf("candidates=%v", ids)
	}
}

func TestMemoryLoaderServerUnreachable(t *testing.T) {
	cwd := writeTracker(t, "", repoMemLine)
	lister := &fakeLister{err: errors.New("dial tcp: connection refused")}
	got, err := NewMemoryLoader(lister, func(string) string { return "proj" })(cwd)
	if err == nil || len(got) != 1 || got[0].Memory.Key != "repo-release" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if len(lister.calls) != 1 {
		t.Fatalf("a failed global list must skip the project list: %v", lister.calls)
	}
	// A real client against a closed port: bounded and non-fatal.
	dead := client.NewWithTimeout("127.0.0.1:1", "", 200*time.Millisecond)
	start := time.Now()
	got, err = NewMemoryLoader(dead, nil)(cwd)
	if err == nil || len(got) != 1 || time.Since(start) > 2*time.Second {
		t.Fatalf("dead server: got=%v err=%v in %s", got, err, time.Since(start))
	}
}

func TestMemoryLoaderSwitches(t *testing.T) {
	off := writeTracker(t, "prefix: x\nprime:\n  inject_on_prompt: false\n", repoMemLine)
	lister := &fakeLister{global: []client.ScopedMemory{{Key: "g", Content: "g"}}}
	if got, err := NewMemoryLoader(lister, nil)(off); err != nil || len(got) != 0 || len(lister.calls) != 0 {
		t.Fatalf("inject_on_prompt=false: got=%v err=%v calls=%v", got, err, lister.calls)
	}
	noScoped := writeTracker(t, "prefix: x\nprime:\n  scoped_memory: false\n", repoMemLine)
	if got, _ := NewMemoryLoader(lister, nil)(noScoped); len(got) != 1 || len(lister.calls) != 0 {
		t.Fatalf("scoped_memory=false keeps only repo memories: got=%v calls=%v", got, lister.calls)
	}
	// No tracker at all: the server memories still apply.
	if got, _ := NewMemoryLoader(lister, nil)(t.TempDir()); len(got) != 1 || got[0].Scope != MemoryScopeGlobal {
		t.Fatalf("no tracker: got=%v", got)
	}
}
