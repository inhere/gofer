package tracker

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// scopeTestStore builds a workspace tracker root holding three checkouts of their own
// plus one plain directory, and returns it with the root.
func scopeTestStore(t *testing.T) (string, *Store) {
	t.Helper()
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".gofer", "tracker"))
	for _, dir := range []string{"codesys-cli", "hmi-cli", "deploy-hub"} {
		mustMkdir(t, filepath.Join(root, dir, ".git"))
	}
	mustMkdir(t, filepath.Join(root, "plain-dir"))
	return root, NewStore(filepath.Join(root, ".gofer", "tracker"))
}

func writeTestMemories(t *testing.T, s *Store, items ...Memory) {
	t.Helper()
	for i := range items {
		items[i].UpdatedAt = Now()
	}
	assert.NoErr(t, s.UpdateMemories(func([]Memory) ([]Memory, error) { return items, nil }))
}

func TestSuggestMemoryScopes(t *testing.T) {
	root, s := scopeTestStore(t)
	writeTestMemories(t, s,
		// 1. source: the job's cwd is inside deploy-hub.
		Memory{Key: "dep-1", Content: "发布脚本在 scripts/", MemoryMeta: MemoryMeta{Source: "job:20261015-161635-058b0d41", Summary: "发布"}},
		// 2a. key prefix identical to a subrepo directory.
		Memory{Key: "codesys-cli-login-rule", Content: "登录流程固定", Tags: []string{"codesys"}},
		// 2b. key prefix equal after dropping `-` / `_` (`hmicli-` ↔ `hmi-cli/`).
		Memory{Key: "hmicli-1", Content: "生成脚本要跑两遍"},
		// 2c. a tag names the subrepo.
		Memory{Key: "tag-1", Content: "交付方式", Tags: []string{"deploy-hub"}},
		// 3. exactly one subrepo named in the text.
		Memory{Key: "misc-1", Content: "编译 hmi-cli 时先跑生成脚本"},
		// 4. no evidence / several subrepos / already scoped: no suggestion.
		Memory{Key: "general-1", Content: "提交前跑测试"},
		Memory{Key: "multi-1", Content: "codesys-cli 与 deploy-hub 共用同一套脚本"},
		Memory{Key: "scoped-1", Content: "x", MemoryMeta: MemoryMeta{When: &MemoryWhen{Paths: []string{"web/**"}}}},
		// A source we cannot resolve falls through to the other evidence.
		Memory{Key: "dep-2", Content: "没有线索", MemoryMeta: MemoryMeta{Source: "session:s-1"}},
		// A source whose cwd is the workspace root is not a subproject either.
		Memory{Key: "dep-3", Content: "没有线索", MemoryMeta: MemoryMeta{Source: "job:at-root"}},
	)
	sugs, err := s.SuggestMemoryScopes(func(source string) (string, bool) {
		switch source {
		case "job:20261015-161635-058b0d41":
			return filepath.Join(root, "deploy-hub", "src"), true
		case "job:at-root":
			return root, true
		}
		return "", false
	})
	assert.NoErr(t, err)

	got := make(map[string]ScopeSuggestion, len(sugs))
	for _, sug := range sugs {
		got[sug.Key] = sug
	}
	assert.Eq(t, 5, len(sugs))
	assert.Eq(t, "deploy-hub/**", got["dep-1"].Paths[0])
	assert.Eq(t, ScopeConfidenceHigh, got["dep-1"].Confidence)
	assert.True(t, strings.Contains(got["dep-1"].Basis, "job:20261015-161635-058b0d41"))
	assert.Eq(t, "codesys-cli/**", got["codesys-cli-login-rule"].Paths[0])
	assert.Eq(t, ScopeConfidenceHigh, got["codesys-cli-login-rule"].Confidence)
	assert.Eq(t, "hmi-cli/**", got["hmicli-1"].Paths[0])
	assert.Eq(t, "deploy-hub/**", got["tag-1"].Paths[0])
	assert.Eq(t, "hmi-cli/**", got["misc-1"].Paths[0])
	assert.Eq(t, ScopeConfidenceMedium, got["misc-1"].Confidence)
	for _, key := range []string{"general-1", "multi-1", "scoped-1", "dep-2", "dep-3"} {
		_, ok := got[key]
		assert.False(t, ok, key)
	}
	// Nothing is written by a scan.
	mem, err := s.Memory("codesys-cli-login-rule")
	assert.NoErr(t, err)
	assert.True(t, mem.When == nil)
}

func TestSuggestMemoryScopesWithoutSubrepos(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".gofer", "tracker"))
	mustMkdir(t, filepath.Join(root, "plain-dir"))
	s := NewStore(filepath.Join(root, ".gofer", "tracker"))
	writeTestMemories(t, s, Memory{Key: "k1", Content: "codesys-cli 的事"})
	sugs, err := s.SuggestMemoryScopes(nil)
	assert.NoErr(t, err)
	assert.Len(t, sugs, 0)
}

func TestApplyMemoryScopePaths(t *testing.T) {
	_, s := scopeTestStore(t)
	writeTestMemories(t, s,
		Memory{Key: "k1", Content: "正文", Tags: []string{"web"}, MemoryMeta: MemoryMeta{Kind: MemoryKindRule, Summary: "一句话", Source: "issue:x-1", When: &MemoryWhen{Keywords: []string{"发版"}}}},
		Memory{Key: "k2", Content: "正文", MemoryMeta: MemoryMeta{When: &MemoryWhen{Paths: []string{"web/**"}}}},
	)
	changed, err := s.ApplyMemoryScopePaths([]ScopeSuggestion{
		{Key: "k1", Paths: []string{"deploy-hub/**"}},
		{Key: "gone", Paths: []string{"hmi-cli/**"}}, // no such memory: skipped
		{Key: "k2", Paths: []string{"hmi-cli/**"}},   // scoped meanwhile: skipped
	})
	assert.NoErr(t, err)
	assert.Len(t, changed, 1)

	got, err := s.Memory("k1")
	assert.NoErr(t, err)
	assert.Eq(t, "deploy-hub/**", got.When.Paths[0])
	// Only when.paths (and updated_at, which the merge driver needs) changed.
	assert.Eq(t, "发版", got.When.Keywords[0])
	assert.Eq(t, "web", got.Tags[0])
	assert.Eq(t, MemoryKindRule, got.Kind)
	assert.Eq(t, "一句话", got.Summary)
	assert.Eq(t, "issue:x-1", got.Source)
	assert.Eq(t, "正文", got.Content)
	assert.True(t, strings.TrimSpace(got.UpdatedAt) != "")

	kept, err := s.Memory("k2")
	assert.NoErr(t, err)
	assert.Eq(t, "web/**", kept.When.Paths[0])
}
