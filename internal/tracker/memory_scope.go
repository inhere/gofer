package tracker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Confidence of a `memory scope` suggestion (gofer-opv5): `source` and key / tag
// evidence are high (they name the subproject), a mention in the text is medium.
const (
	ScopeConfidenceHigh   = "high"
	ScopeConfidenceMedium = "medium"
)

// ScopeSuggestion is one proposed `when.paths` for a memory that has none: the
// subproject it looks like it belongs to, the evidence behind it, and how strong that
// evidence is. Nothing is written until ApplyMemoryScopePaths runs.
type ScopeSuggestion struct {
	Key        string   `json:"key"`
	Paths      []string `json:"when_paths"`
	Basis      string   `json:"basis"`
	Confidence string   `json:"confidence"`
}

// ScopeConfidenceRank orders the confidence levels (higher = stronger evidence).
func ScopeConfidenceRank(confidence string) int {
	switch confidence {
	case ScopeConfidenceHigh:
		return 2
	case ScopeConfidenceMedium:
		return 1
	}
	return 0
}

// ScopeConfidenceLabel is the label of a confidence level shown by `memory scope`.
func ScopeConfidenceLabel(confidence string) string {
	switch confidence {
	case ScopeConfidenceHigh:
		return "高"
	case ScopeConfidenceMedium:
		return "中"
	}
	return "低"
}

// Subrepos lists the nested repositories directly under the tracker's repository root:
// the names (slash form, no trailing slash) of the directories holding a `.git` entry of
// their own, sorted. They are the candidate scopes `memory scope` suggests.
func (s *Store) Subrepos() []string {
	root := s.RepoRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, entry.Name(), ".git")); err != nil {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// SuggestMemoryScopes proposes a `when.paths` for every memory of this tracker that has
// none (gofer-opv5): a workspace root whose subprojects are separate checkouts shares one
// tracker, and an unscoped memory reaches the jobs of all of them. cwdOf resolves a
// memory's `source` (job:<id> / session:<id>) to the working directory it was written
// from; nil means that evidence is unavailable (the caller cannot reach the server).
//
// The evidence is tried in this order and the first one that names exactly one subproject
// wins:
//  1. source: the cwd of the job / session the memory came from is inside a subrepo (high);
//  2. one of the key's prefixes or one of its tags names a subrepo directory (high);
//  3. the summary / content mentions exactly one subrepo directory (medium).
//
// Evidence naming no subproject or several of them suggests nothing: that memory stays
// workspace-wide.
func (s *Store) SuggestMemoryScopes(cwdOf func(source string) (string, bool)) ([]ScopeSuggestion, error) {
	root := s.RepoRoot()
	dirs := s.Subrepos()
	if root == "" || len(dirs) == 0 {
		return nil, nil
	}
	items, err := s.ReadMemories()
	if err != nil {
		return nil, err
	}
	out := make([]ScopeSuggestion, 0)
	for _, item := range items {
		if item.When != nil && len(item.When.Paths) > 0 {
			continue // already scoped: never touched
		}
		if sug, ok := suggestMemoryScope(item, root, dirs, cwdOf); ok {
			out = append(out, sug)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func suggestMemoryScope(item Memory, root string, dirs []string, cwdOf func(string) (string, bool)) (ScopeSuggestion, bool) {
	if cwdOf != nil {
		if source := strings.TrimSpace(item.Source); isSourceRef(source) {
			if cwd, ok := cwdOf(source); ok {
				if dir, ok := subrepoOfCwd(root, cwd, dirs); ok {
					return scopeSuggestion(item.Key, dir, fmt.Sprintf("来源 %s 的 cwd 在 %s/ 内", source, dir), ScopeConfidenceHigh), true
				}
			}
		}
	}
	if dir, ok := scopeNameMatch(item, dirs); ok {
		return scopeSuggestion(item.Key, dir, fmt.Sprintf("key 前缀 / 标签与子仓名 %s 匹配", dir), ScopeConfidenceHigh), true
	}
	if mentioned := mentionedSubrepos(item, dirs); len(mentioned) == 1 {
		dir := mentioned[0]
		return scopeSuggestion(item.Key, dir, fmt.Sprintf("摘要 / 正文提到子仓名 %s", dir), ScopeConfidenceMedium), true
	}
	return ScopeSuggestion{}, false
}

func scopeSuggestion(key, dir, basis, confidence string) ScopeSuggestion {
	return ScopeSuggestion{Key: key, Paths: []string{dir + "/**"}, Basis: basis, Confidence: confidence}
}

// isSourceRef reports whether source is a job / session reference (the only sources a
// working directory can be looked up for).
func isSourceRef(source string) bool {
	for _, prefix := range []string{"job:", "session:"} {
		if strings.HasPrefix(source, prefix) && strings.TrimSpace(source[len(prefix):]) != "" {
			return true
		}
	}
	return false
}

// subrepoOfCwd reports which of dirs cwd sits in.
func subrepoOfCwd(root, cwd string, dirs []string) (string, bool) {
	if strings.TrimSpace(cwd) == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	first, _, _ := strings.Cut(rel, "/")
	for _, dir := range dirs {
		if first == dir {
			return dir, true
		}
	}
	return "", false
}

// scopeNameMatch reports the subrepo a key prefix or a tag names.
func scopeNameMatch(item Memory, dirs []string) (string, bool) {
	names := append([]string(nil), item.Tags...)
	for i, r := range item.Key {
		if r == '-' || r == '_' {
			names = append(names, item.Key[:i])
		}
	}
	names = append(names, item.Key)
	for _, dir := range dirs {
		for _, token := range scopeTokens(dir) {
			if token == "" {
				continue
			}
			for _, name := range names {
				if normalizeScopeToken(name) == token {
					return dir, true
				}
			}
		}
	}
	return "", false
}

// scopeTokens are the normalized forms a key prefix or tag may use for the subrepo
// directory dir: the directory name itself, and — when at least three characters are
// left — the name without its last `-` segment, so `hmi-cli/` is also named by `hmi-*`
// (dashes and underscores are dropped on both sides).
func scopeTokens(dir string) []string {
	tokens := []string{normalizeScopeToken(dir)}
	if i := strings.LastIndex(dir, "-"); i > 0 {
		if rest := normalizeScopeToken(dir[:i]); len(rest) >= 3 {
			tokens = append(tokens, rest)
		}
	}
	return tokens
}

func normalizeScopeToken(name string) string {
	name = strings.NewReplacer("-", "", "_", "").Replace(strings.TrimSpace(name))
	return strings.ToLower(name)
}

// mentionedSubrepos lists the subrepo directories named in the memory's summary / content.
func mentionedSubrepos(item Memory, dirs []string) []string {
	text := strings.ToLower(DisplayMemorySummary(item.MemoryMeta, item.Content) + "\n" + item.Content)
	found := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if strings.Contains(text, strings.ToLower(dir)) {
			found = append(found, dir)
		}
	}
	return found
}

// ApplyMemoryScopePaths writes paths into the memories of suggestions: `when.paths` (and
// `updated_at`, because the tracker merge takes the newer record, so a change that leaves
// the stamp alone can be reverted by the next merge) and nothing else. A key that
// disappeared, or that gained a scope since the suggestion was computed, is skipped. It
// returns the rewritten memories, sorted by key by the underlying write.
func (s *Store) ApplyMemoryScopePaths(suggestions []ScopeSuggestion) ([]Memory, error) {
	wanted := make(map[string][]string, len(suggestions))
	for _, sug := range suggestions {
		if len(sug.Paths) > 0 {
			wanted[sug.Key] = sug.Paths
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	var changed []Memory
	err := s.UpdateMemories(func(items []Memory) ([]Memory, error) {
		now := Now()
		for i := range items {
			paths, ok := wanted[items[i].Key]
			if !ok || (items[i].When != nil && len(items[i].When.Paths) > 0) {
				continue
			}
			when := MemoryWhen{}
			if items[i].When != nil {
				when = *items[i].When
			}
			when.Paths = append([]string(nil), paths...)
			items[i].When = &when
			items[i].UpdatedAt = now
			changed = append(changed, items[i])
		}
		return items, nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}
