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
	"github.com/google/uuid"
)

const beginBlock = "<!-- BEGIN GOFER TRACKER v:1 -->"
const endBlock = "<!-- END GOFER TRACKER -->"

const managedBlock = beginBlock + "\n" +
	"Use `gofer issue` and `gofer memory` for repository-local tracking.\n" +
	"按功能点本地提交是默认授权，不 push；tracker 的 jsonl 变化随功能点一起提交。\n" +
	"会话开场会自动注入 tracker 上下文。\n" + endBlock + "\n"

// Init creates only the P2 local files and managed instructions. Hooks and sync belong to later phases.
func Init(root, prefix string, noAgentsMD bool) (*Store, bool, error) {
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
		body, err := yaml.Marshal(Config{Prefix: prefix, TrackerID: uuid.NewString(), CommitPolicy: "local-commit", AutoSync: true})
		if err != nil {
			return nil, false, err
		}
		if err := os.WriteFile(configPath, body, 0o644); err != nil {
			return nil, false, err
		}
	} else if err != nil {
		return nil, false, err
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
			if !bytes.Contains(b, []byte(endBlock)) {
				return nil, beads, fmt.Errorf("incomplete gofer tracker block in %s", path)
			}
			continue
		}
		out := append([]byte(nil), b...)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, managedBlock...)
		if err := atomicWrite(path, out); err != nil {
			return nil, beads, err
		}
	}
	return s, beads, nil
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
	return cfg, err
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
	Tracker      string         `json:"tracker"`
	Issues       map[string]int `json:"issues"`
	Memories     int            `json:"memories"`
	CommitPolicy string         `json:"commit_policy"`
	ManagedBlock bool           `json:"managed_block"`
	Hooks        string         `json:"hooks"`
	Sync         string         `json:"sync"`
	ProjectKey   string         `json:"project_key,omitempty"`
	LastSyncAt   string         `json:"last_sync_at,omitempty"`
	PendingSync  int            `json:"pending_sync"`
	SyncSummary  string         `json:"sync_summary,omitempty"`
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
	if b, e := os.ReadFile(filepath.Join(s.Dir, ".local", "sync-status.json")); e == nil {
		var meta repoSyncMeta
		if json.Unmarshal(b, &meta) == nil {
			status.LastSyncAt = meta.LastSyncAt
			status.SyncSummary = meta.Summary
			status.Sync = "已同步"
		}
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
