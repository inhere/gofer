package job

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// uncommittedSnapshot records content, not only porcelain status: a file already
// dirty before the job counts only if its contents change during this run.
type uncommittedSnapshot map[string]string

const uncommittedTimeout = 30 * time.Second

func captureUncommitted(cwd string) uncommittedSnapshot {
	if cwd == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), uncommittedTimeout)
	defer cancel()
	if !isGitWorkTree(ctx, cwd) {
		return nil
	}

	nested := nestedGitRoots(ctx, cwd)
	result := make(uncommittedSnapshot)
	for _, root := range append([]string{cwd}, nested...) {
		rel, err := filepath.Rel(cwd, root)
		if err != nil {
			continue
		}
		prefix := ""
		if rel != "." {
			prefix = filepath.ToSlash(rel) + "/"
		}
		// git status already applies the exclude-standard ignore rules to
		// untracked files; --exclude-standard itself is an ls-files flag.
		status, err := gitStatusPorcelain(ctx, root)
		if err != nil {
			return nil
		}
		for _, name := range porcelainPaths(status) {
			key := prefix + filepath.ToSlash(name)
			if prefix == "" && inNestedRepo(key, nested, cwd) {
				continue
			}
			hash := strings.TrimSpace(string(runGit(ctx, root, 256, "hash-object", "--", name)))
			if ctx.Err() != nil {
				return nil
			}
			if hash == "" {
				hash = "<deleted>"
			}
			result[key] = hash
		}
	}
	return result
}

func gitStatusPorcelain(ctx context.Context, root string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v2", "-z", "--untracked-files=all")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	// The count must cover the whole status output, even when the path list we
	// eventually store is capped at 200. A truncated porcelain record is invalid.
	return cmd.Output()
}

func porcelainPaths(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	paths := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		line := parts[i]
		if line == "" {
			continue
		}
		var fields int
		switch {
		case strings.HasPrefix(line, "1 "):
			fields = 9
		case strings.HasPrefix(line, "2 "):
			fields = 10
			// Porcelain v2 -z puts the rename origin in the next NUL field.
			i++
		case strings.HasPrefix(line, "u "):
			fields = 11
		case strings.HasPrefix(line, "? "):
			fields = 2
		default:
			continue
		}
		parts := strings.SplitN(line, " ", fields)
		if len(parts) == fields && parts[fields-1] != "" {
			paths = append(paths, parts[fields-1])
		}
	}
	return paths
}

func nestedGitRoots(ctx context.Context, cwd string) []string {
	roots := []string{}
	_ = filepath.WalkDir(cwd, func(name string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(cwd, name)
		if err != nil {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		if depth > 2 || entry.Name() == "node_modules" || entry.Name() == "tmp" || entry.Name() == "vendor" || entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if gitIgnored(ctx, cwd, rel) {
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(name, ".git")); err == nil {
			roots = append(roots, name)
			return filepath.SkipDir
		}
		return nil
	})
	slices.Sort(roots)
	return roots
}

func gitIgnored(ctx context.Context, cwd, rel string) bool {
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "-q", "--", rel)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd.Run() == nil
}

func inNestedRepo(key string, roots []string, cwd string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(cwd, root)
		if err == nil && strings.HasPrefix(key, filepath.ToSlash(rel)+"/") {
			return true
		}
	}
	return false
}

func diffUncommitted(before, after uncommittedSnapshot, ignore []string) []string {
	var files []string
	for name, hash := range after {
		if old, existed := before[name]; existed && old == hash {
			continue
		}
		ignored := false
		for _, pattern := range ignore {
			if globMatch(pattern, name) {
				ignored = true
				break
			}
		}
		if !ignored {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	return files
}

func globMatch(pattern, name string) bool {
	patternParts := strings.Split(pattern, "/")
	nameParts := strings.Split(name, "/")
	var match func(int, int) bool
	match = func(i, j int) bool {
		if i == len(patternParts) {
			return j == len(nameParts)
		}
		if patternParts[i] == "**" {
			if match(i+1, j) {
				return true
			}
			return j < len(nameParts) && match(i, j+1)
		}
		if j >= len(nameParts) {
			return false
		}
		ok, _ := path.Match(patternParts[i], nameParts[j])
		return ok && match(i+1, j+1)
	}
	return match(0, 0)
}

func uncommittedEnabled(agent, policy string) bool {
	return agent != "exec" && policy != "off"
}

func uncommittedDecision(policy string, files []string, session string, attempt, max int, resumed bool) string {
	if len(files) == 0 || policy == "off" {
		return ""
	}
	switch policy {
	case "review":
		return "review"
	case "resume":
		if resumed || attempt > 0 || session == "" || max <= attempt {
			return "review"
		}
		return "resume"
	default:
		return "warn"
	}
}

func uncommittedResumePrompt(files []string) string {
	return uncommittedResumePromptCount(len(files), files)
}

func uncommittedResumePromptCount(count int, files []string) string {
	first := files
	if len(first) > 20 {
		first = first[:20]
	}
	return fmt.Sprintf("上一轮结束时有 %d 个文件改动未提交：%s。请按功能点检查并本地提交（conventional commit），不要 push，不要提交无关文件；不应提交的请说明原因。", count, strings.Join(first, "、"))
}

func (s *Service) resumeUncommitted(snap JobResult) bool {
	res, err := s.resumeJob(snap.ID, uncommittedResumePromptCount(snap.UncommittedCount, snap.UncommittedFiles), snap.Runner, snap.CallerID, snap.AutoResumeAttempt+1, nil, ResumeOptions{})
	if err != nil {
		return false
	}
	snap.AutoResumedBy = res.ID
	_ = s.persist(snap)
	s.recordEvent(snap.ID, "job.auto_resumed", map[string]any{"job_id": res.ID, "reason": "uncommitted"})
	return true
}

func (s *Service) uncommittedSettings(projectKey string) (string, []string) {
	policy := "warn"
	if cfg := s.config(); cfg != nil {
		if project, ok := cfg.Projects[projectKey]; ok {
			if project.OnUncommitted != "" {
				policy = project.OnUncommitted
			}
			return policy, project.UncommittedIgnore
		}
	}
	return policy, nil
}

// SetUncommittedDecisionOnly is set during worker assembly, before jobs start.
// The worker still detects with the pushed policy; the hub owns transitions.
func (s *Service) SetUncommittedDecisionOnly(enabled bool) {
	s.uncommittedDecisionOnly = enabled
}

func (s *Service) captureUncommittedOutcome(entry *jobEntry) {
	entry.mu.Lock()
	result := entry.result
	baseline := entry.uncommittedBaseline
	entry.mu.Unlock()
	policy, ignore := s.uncommittedSettings(result.ProjectKey)
	if !uncommittedEnabled(result.Agent, policy) && !(result.ResumedFrom != "" && policy != "off") {
		return
	}
	if baseline == nil {
		return
	}
	cwd := result.Cwd
	if entry.wt != nil {
		cwd = entry.wt.Path
	}
	after := captureUncommitted(cwd)
	if after == nil {
		return
	}
	files := diffUncommitted(baseline, after, ignore)
	if result.AutoResumeAttempt > 0 && policy == "resume" {
		// A continuation must clear the prior round's dirty files even if it never
		// touches them. Its own baseline would otherwise hide exactly those files.
		files = diffUncommitted(nil, after, ignore)
	}
	if len(files) == 0 {
		return
	}
	first := files
	if len(first) > 200 {
		first = first[:200]
	}
	entry.mu.Lock()
	entry.result.UncommittedCount = len(files)
	entry.result.UncommittedFiles = slices.Clone(first)
	entry.mu.Unlock()
	eventFiles := files
	if len(eventFiles) > 20 {
		eventFiles = eventFiles[:20]
	}
	s.recordEvent(result.ID, EventJobUncommitted, map[string]any{"count": len(files), "files": eventFiles})
}
