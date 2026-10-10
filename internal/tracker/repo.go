package tracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

const beginBlock = "<!-- BEGIN GOFER TRACKER v:1 -->"
const endBlock = "<!-- END GOFER TRACKER -->"

// managedBlockHead / managedBlockTail surround the commit-policy line of the
// managed block; the line itself is CommitPolicyText, the same text prime shows.
const managedBlockHead = beginBlock + "\n" +
	"## Gofer Issue Tracker\n\n" +
	"本仓库用 `gofer issue` / `gofer memory` 跟踪任务与记忆（数据在 `.gofer/tracker/`，会话开场自动注入上下文）。\n\n" +
	"```bash\n" +
	"gofer issue ready                       # 可开工的 issue（open 且未被阻塞）\n" +
	"gofer issue show <id>                   # 详情：评论 / 依赖 / 父子\n" +
	"gofer issue create \"标题\" -p 1 -l 标签   # 新建（-l 标签区分子项目）\n" +
	"gofer issue update <id> --claim         # 认领并开始\n" +
	"gofer issue comment <id> \"进展\"        # 追加评论\n" +
	"gofer issue close <id> --reason \"...\"  # 完成\n" +
	"gofer memory set <key> \"内容\" --summary \"一句话\"  # 记住经验；gofer memory ls <关键字> / show <key> 召回\n" +
	"```\n\n" +
	"- 用 `gofer issue` 跟踪全部任务，不要另建 markdown TODO；持久经验用 `gofer memory`。\n" +
	"- " + MemoryWriteHint + "\n"

const managedBlockTail = "- 查看 tracker 改了什么用 `gofer repo status --changed`（逐条列出 issue / memory 的增删改）；**不要** `git diff` / `cat` `.gofer/tracker/*.jsonl`，整行 JSON 会灌满上下文。\n" +
	endBlock + "\n"

// DefaultCommitPolicy is the commit_policy of a new tracker.
const DefaultCommitPolicy = "local-commit"

// ManagedBlockFor renders the managed block for a commit_policy value; its
// commit line is CommitPolicyText, so the block and prime always agree.
func ManagedBlockFor(policy string) (string, error) {
	text, err := CommitPolicyText(policy)
	if err != nil {
		return "", err
	}
	return managedBlockHead + "- " + text + "\n" + managedBlockTail, nil
}

// BeginBlock / EndBlock delimit the gofer-managed block in AGENTS.md / CLAUDE.md.
const (
	BeginBlock = beginBlock
	EndBlock   = endBlock
)

// ManagedBlock returns the managed block for the default commit policy.
func ManagedBlock() string {
	block, _ := ManagedBlockFor(DefaultCommitPolicy)
	return block
}

// ClaudeImportsAgents is claudeImportsAgents for other packages (the bd migration).
func ClaudeImportsAgents(root string) bool { return claudeImportsAgents(root) }

// AtomicWriteFile replaces path with data without ever leaving a half-written file.
func AtomicWriteFile(path string, data []byte) error { return atomicWrite(path, data) }

// InitOptions are the inputs of InitWith.
type InitOptions struct {
	// Prefix is the issue id prefix ("" = the repository directory name).
	Prefix string
	// NoAgentsMD skips the managed block in AGENTS.md / CLAUDE.md.
	NoAgentsMD bool
	// CommitPolicy sets commit_policy (local-commit | ask | none); "" keeps the
	// current value (a new tracker gets DefaultCommitPolicy).
	CommitPolicy string
}

// Init is InitWith without a commit policy change.
func Init(root, prefix string, noAgentsMD bool) (*Store, bool, error) {
	return InitWith(root, InitOptions{Prefix: prefix, NoAgentsMD: noAgentsMD})
}

// InitWith creates the local tracker files and writes (or refreshes) the managed
// instructions block, rendered for the repository's commit_policy. Hooks and sync
// are the caller's business. beads reports a leftover bd integration block.
func InitWith(root string, opts InitOptions) (*Store, bool, error) {
	prefix, noAgentsMD := opts.Prefix, opts.NoAgentsMD
	policy := strings.TrimSpace(opts.CommitPolicy)
	if policy != "" {
		if _, err := CommitPolicyText(policy); err != nil {
			return nil, false, fmt.Errorf("%w (want local-commit, ask or none)", err)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, false, err
	}
	if prefix == "" {
		prefix = filepath.Base(abs)
	}
	if strings.TrimSpace(prefix) == "" || strings.ContainsAny(prefix, "/\\ \t\r\n") {
		return nil, false, fmt.Errorf("invalid issue prefix %q", prefix)
	}
	dir := filepath.Join(abs, ".gofer", "tracker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	s := NewStore(dir)
	configPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		initial := policy
		if initial == "" {
			initial = DefaultCommitPolicy
		}
		body, err := yaml.Marshal(Config{Prefix: prefix, TrackerID: NewTrackerID(), CommitPolicy: initial, AutoSync: true})
		if err != nil {
			return nil, false, err
		}
		if err := os.WriteFile(configPath, body, 0o644); err != nil {
			return nil, false, err
		}
	} else if err != nil {
		return nil, false, err
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		return nil, false, err
	}
	if policy != "" && cfg.CommitPolicy != policy {
		if err := s.UpdateConfig(func(c *Config) { c.CommitPolicy = policy }); err != nil {
			return nil, false, err
		}
		cfg.CommitPolicy = policy
	}
	for _, name := range []string{"issues.jsonl", "memories.jsonl"} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if err := f.Close(); err != nil {
			return nil, false, err
		}
	}
	if err := appendOnce(filepath.Join(abs, ".gofer", ".gitignore"), "tracker/.local/\n"); err != nil {
		return nil, false, err
	}
	if noAgentsMD {
		return s, false, nil
	}
	block, err := ManagedBlockFor(cfg.CommitPolicy)
	if err != nil {
		return nil, false, fmt.Errorf(".gofer/tracker/config.yaml: %w", err)
	}
	paths := []string{}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if name == "CLAUDE.md" && claudeImportsAgents(abs) {
			continue
		}
		path := filepath.Join(abs, name)
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}
	}
	if len(paths) == 0 {
		paths = append(paths, filepath.Join(abs, "AGENTS.md"))
	}
	beads := false
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}
		if bytes.Contains(b, []byte("<!-- BEGIN BEADS INTEGRATION")) {
			beads = true
		}
		if bytes.Contains(b, []byte(beginBlock)) {
			refreshed, ok := refreshManagedBlock(b, block)
			if !ok {
				return nil, beads, fmt.Errorf("incomplete gofer tracker block in %s", path)
			}
			if !bytes.Equal(refreshed, b) {
				if err := atomicWrite(path, refreshed); err != nil {
					return nil, beads, err
				}
			}
			continue
		}
		out := append([]byte(nil), b...)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, block...)
		if err := atomicWrite(path, out); err != nil {
			return nil, beads, err
		}
	}
	return s, beads, nil
}

// refreshManagedBlock replaces an existing gofer block with block, so re-running
// `repo init` brings older instructions (and a changed commit_policy) up to date.
// ok is false when the end marker is missing.
func refreshManagedBlock(b []byte, block string) ([]byte, bool) {
	start := bytes.Index(b, []byte(beginBlock))
	rel := bytes.Index(b[start:], []byte(endBlock))
	if rel < 0 {
		return nil, false
	}
	end := start + rel + len(endBlock)
	if end < len(b) && b[end] == '\n' {
		end++
	}
	out := append([]byte(nil), b[:start]...)
	out = append(out, block...)
	return append(out, b[end:]...), true
}

// claudeImportsAgents reports whether root's CLAUDE.md pulls AGENTS.md in with
// an `@AGENTS.md` line while AGENTS.md exists; the gofer block then belongs in
// AGENTS.md only, or Claude reads it twice.
func claudeImportsAgents(root string) bool {
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "@AGENTS.md" {
			return true
		}
	}
	return false
}

func appendOnce(path, line string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, existing := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(existing) == strings.TrimSpace(line) {
			return nil
		}
	}
	out := append([]byte(nil), b...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, line...)
	return atomicWrite(path, out)
}

func (s *Store) ReadConfig() (Config, error) {
	var cfg Config
	b, err := os.ReadFile(filepath.Join(s.Dir, "config.yaml"))
	if err != nil {
		return cfg, err
	}
	err = yaml.Unmarshal(b, &cfg)
	if err == nil {
		for name, value := range map[string]*int{"issues_limit": cfg.Prime.IssuesLimit, "ready_limit": cfg.Prime.ReadyLimit, "memory_summary_limit": cfg.Prime.MemorySummaryLimit} {
			if value != nil && *value < 0 {
				return cfg, fmt.Errorf("prime.%s must be >= 0", name)
			}
		}
	}
	return cfg, err
}

// UpdateConfig rewrites config.yaml after applying change to the current values.
func (s *Store) UpdateConfig(change func(*Config)) error {
	cfg, err := s.ReadConfig()
	if err != nil {
		return err
	}
	change(&cfg)
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.Dir, "config.yaml"), b)
}

func (s *Store) SetProjectKey(key string) error {
	cfg, err := s.ReadConfig()
	if err != nil {
		return err
	}
	cfg.ProjectKey = key
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.Dir, "config.yaml"), b)
}

type RepoStatus struct {
	Tracker        string         `json:"tracker"`
	Issues         map[string]int `json:"issues"`
	Memories       int            `json:"memories"`
	CommitPolicy   string         `json:"commit_policy"`
	ManagedBlock   bool           `json:"managed_block"`
	Hooks          string         `json:"hooks"`
	Sync           string         `json:"sync"`
	ProjectKey     string         `json:"project_key,omitempty"`
	LastSyncAt     string         `json:"last_sync_at,omitempty"`
	PendingSync    int            `json:"pending_sync"`
	SyncSummary    string         `json:"sync_summary,omitempty"`
	PrimeBytes     int            `json:"prime_bytes"`
	PrimeTruncated bool           `json:"prime_truncated"`
}

type repoSyncMeta struct {
	LastSyncAt string `json:"last_sync_at"`
	Summary    string `json:"summary"`
}

func (s *Store) Status() (RepoStatus, error) {
	cfg, err := s.ReadConfig()
	if err != nil {
		return RepoStatus{}, err
	}
	issues, err := s.ReadIssues()
	if err != nil {
		return RepoStatus{}, err
	}
	memories, err := s.ReadMemories()
	if err != nil {
		return RepoStatus{}, err
	}
	status := RepoStatus{Tracker: s.Dir, Issues: map[string]int{"open": 0, "in_progress": 0, "blocked": 0, "closed": 0}, Memories: len(memories), CommitPolicy: cfg.CommitPolicy, ProjectKey: cfg.ProjectKey, Hooks: "已检测", Sync: "未同步"}
	status.PrimeBytes, status.PrimeTruncated, err = s.PrimeEstimate()
	if err != nil {
		return RepoStatus{}, err
	}
	if b, e := os.ReadFile(filepath.Join(s.Dir, ".local", "sync-status.json")); e == nil {
		var meta repoSyncMeta
		if json.Unmarshal(b, &meta) == nil {
			status.LastSyncAt = meta.LastSyncAt
			status.SyncSummary = meta.Summary
			status.Sync = "已同步"
		}
	}
	if base, e := readSyncBase(s.Dir); e == nil {
		cur := SyncSnapshot{Issues: issues, Memories: memories}
		status.PendingSync = snapshotDiffCount(base, cur)
	} else {
		status.PendingSync = len(issues) + len(memories)
	}
	for _, item := range issues {
		status.Issues[item.Status]++
	}
	root := filepath.Dir(filepath.Dir(s.Dir))
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err == nil && bytes.Contains(b, []byte(beginBlock)) && bytes.Contains(b, []byte(endBlock)) {
			status.ManagedBlock = true
		}
	}
	return status, nil
}

func snapshotDiffCount(a, b SyncSnapshot) int {
	n := 0
	am, bm := indexIssues(a.Issues), indexIssues(b.Issues)
	for k, v := range bm {
		if old, ok := am[k]; !ok || string(mustJSON(old)) != string(mustJSON(v)) {
			n++
		}
	}
	mm, bn := indexMemories(a.Memories), indexMemories(b.Memories)
	for k, v := range bn {
		if old, ok := mm[k]; !ok || string(mustJSON(old)) != string(mustJSON(v)) {
			n++
		}
	}
	for k := range am {
		if _, ok := bm[k]; !ok {
			n++
		}
	}
	for k := range mm {
		if _, ok := bn[k]; !ok {
			n++
		}
	}
	return n
}
