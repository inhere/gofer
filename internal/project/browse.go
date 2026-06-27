package project

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// browse.go implements the Web 控制台 v2 只读层 backend (design §6.3 E20 / §6.4
// E32 / §8). It is a read-only git/fs surface over a project's execution root
// (Config.ExecPath, D2): current git state (E20), sub-git discovery (E32) and
// whitelisted key-file reads (E32). It deliberately does NOT import internal/job
// (job→project is single-direction; the reverse would cycle, G022/D6) and carries
// its own exec.CommandContext("git",…)+timeout+cap, mirroring job/gitdiff.go.
//
// Security (design §8): git args are ALWAYS fixed literals (no user concatenation
// → no command injection) and only read-only subcommands run (rev-parse / branch
// via abbrev-ref / status / log). File reads are basename-whitelisted + SafeJoin
// (anti-traversal) + ≤256KB + text-only. No write surface exists here.

// ErrForbiddenFile is returned by ReadKeyFile when the requested basename is not
// in keyFileAllowlist (handler → 403). It guards against arbitrary file reads
// (e.g. .env exfiltration), independent of path traversal.
var ErrForbiddenFile = errors.New("file not in key-file allowlist")

// ErrBinary is returned by ReadKeyFile when the target is not text (handler →
// 415). The web preview only renders text/markdown/json.
var ErrBinary = errors.New("file is not text")

// keyFileAllowlist is the E32 basename whitelist (design §10.3, D3). Only these
// project-relative key files may be read; everything else is ErrForbiddenFile.
// Hard-coded for v1 (made configurable later if needed).
var keyFileAllowlist = map[string]bool{
	"README.md": true, "README": true, "README.txt": true,
	".gitignore": true, "AGENTS.md": true, "CLAUDE.md": true,
	"go.mod": true, "package.json": true, "LICENSE": true, "LICENSE.md": true,
}

const (
	maxKeyFileBytes = 256 * 1024      // D3 key-file size cap (over → truncated)
	repoScanDepth   = 3               // D4 sub-git scan max depth
	maxRepos        = 100             // D4 sub-git hit cap (anti-blowup)
	gitTimeout      = 5 * time.Second // bounds every git subprocess (D6/§8)
)

// repoSkipDirs are directory names DiscoverRepos never descends into: noise dirs
// plus .git internals (D4).
var repoSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, ".git": true,
}

// Commit is one entry of GitStatus.RecentCommits (E20). Ts is the committer date
// as a unix second timestamp.
type Commit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Ts      int64  `json:"ts"`
}

// GitStatus is the current git state of a project root (E20, design §6.3). When
// the root is not a git work tree (or not locally reachable) IsGitRepo is false
// and the rest is zero.
type GitStatus struct {
	IsGitRepo     bool     `json:"is_git_repo"`
	Branch        string   `json:"branch"`
	Dirty         bool     `json:"dirty"`
	RecentCommits []Commit `json:"recent_commits"`
}

// RepoInfo is one discovered git repo under a project root (E32, design §6.4).
// RelPath is "." for the root repo, else slash-separated relative to ExecPath.
type RepoInfo struct {
	RelPath string `json:"rel_path"`
	Branch  string `json:"branch"`
	Dirty   bool   `json:"dirty"`
}

// FileContent is a read whitelisted key file (E32, design §6.4). Truncated is
// true when the file exceeds maxKeyFileBytes and Content is the leading slice.
type FileContent struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// runGitRO runs a read-only git subcommand under dir with fixed args (NEVER user
// concatenation → no injection, §8), a gitTimeout deadline and an output cap of
// capBytes. Any error (non-git dir, git absent, non-zero exit, timeout) degrades
// to ("", err) — it never panics; callers ignore the error for best-effort fields.
func runGitRO(dir string, capBytes int, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	if len(out) > capBytes {
		out = out[:capBytes]
	}
	return string(out), nil
}

// ProjectGit returns the current git state of a project's execution root (E20).
// IsGitRepo is false when the root is not a git work tree (or not locally
// reachable / git absent) — never an error in that case. Branch / dirty / recent
// commits are best-effort (a degraded field stays zero, the call still succeeds).
func ProjectGit(cfg *config.Config, proj config.ProjectConfig) (GitStatus, error) {
	root := cfg.ExecPath(proj)

	// rev-parse --is-inside-work-tree prints "true" only in a real work tree; a
	// bare repo / non-repo / git-absent path → not a git repo (mirrors job pkg).
	out, err := runGitRO(root, 1024, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(out) != "true" {
		return GitStatus{IsGitRepo: false, RecentCommits: []Commit{}}, nil
	}

	branch, _ := runGitRO(root, 1024, "rev-parse", "--abbrev-ref", "HEAD")
	porcelain, _ := runGitRO(root, 64*1024, "status", "--porcelain")
	logOut, _ := runGitRO(root, 32*1024, "log", "-n", "10", "--pretty=%h\x1f%s\x1f%an\x1f%ct")

	return GitStatus{
		IsGitRepo:     true,
		Branch:        strings.TrimSpace(branch),
		Dirty:         strings.TrimSpace(porcelain) != "",
		RecentCommits: parseCommits(logOut),
	}, nil
}

// parseCommits splits the runGitRO log output (one commit per line, fields
// separated by \x1f: hash, subject, author, committer-unix-ts) into Commit
// values. Malformed / short lines are skipped. Always returns a non-nil slice so
// the JSON renders [] not null.
func parseCommits(logOut string) []Commit {
	commits := []Commit{}
	for _, line := range strings.Split(strings.TrimRight(logOut, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 4 {
			continue
		}
		ts, _ := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64)
		commits = append(commits, Commit{
			Hash:    parts[0],
			Subject: parts[1],
			Author:  parts[2],
			Ts:      ts,
		})
	}
	return commits
}

// DiscoverRepos walks a project's execution root up to repoScanDepth, skipping
// repoSkipDirs, and returns each directory that contains a .git entry with its
// branch + dirty flag (E32, D4). The hit count is capped at maxRepos. The root
// itself is included when it is a repo (RelPath "."). Always returns a non-nil
// slice. Unreadable entries are skipped (best-effort), not fatal.
func DiscoverRepos(cfg *config.Config, proj config.ProjectConfig) ([]RepoInfo, error) {
	rootAbs, err := filepath.Abs(cfg.ExecPath(proj))
	if err != nil {
		return nil, err
	}

	repos := []RepoInfo{}
	walkErr := filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, don't abort the whole scan
		}
		if len(repos) >= maxRepos {
			return filepath.SkipAll
		}
		if !d.IsDir() {
			return nil
		}

		rel, rerr := filepath.Rel(rootAbs, path)
		if rerr != nil {
			return filepath.SkipDir
		}
		// Skip noise dirs / .git internals (but never the root itself).
		if rel != "." && repoSkipDirs[d.Name()] {
			return filepath.SkipDir
		}
		// Depth guard: root is depth 0; a/b/c is depth 3. Beyond the cap, don't
		// descend (its .git would sit one level deeper than the repo dir).
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(filepath.Separator)) + 1
		}
		if depth > repoScanDepth {
			return filepath.SkipDir
		}

		if hasGitDir(path) {
			branch, _ := runGitRO(path, 1024, "rev-parse", "--abbrev-ref", "HEAD")
			porcelain, _ := runGitRO(path, 64*1024, "status", "--porcelain")
			repos = append(repos, RepoInfo{
				RelPath: filepath.ToSlash(rel),
				Branch:  strings.TrimSpace(branch),
				Dirty:   strings.TrimSpace(porcelain) != "",
			})
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return repos, nil
}

// hasGitDir reports whether dir is a git repo root, i.e. it has a .git entry —
// either a directory (normal repo) or a file (submodule / worktree gitlink).
func hasGitDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// ReadKeyFile reads a whitelisted key file under a project's execution root (E32,
// D3, §8). Layered guards, in order: basename ∈ keyFileAllowlist (else
// ErrForbiddenFile) → SafeJoin(ExecPath, rel) anti-traversal (escape → error) →
// must be a regular file (else fs.ErrNotExist) → text-only (binary → ErrBinary).
// Content over maxKeyFileBytes is truncated (Truncated=true), not rejected.
func ReadKeyFile(cfg *config.Config, proj config.ProjectConfig, rel string) (FileContent, error) {
	if !keyFileAllowlist[filepath.Base(rel)] {
		return FileContent{}, ErrForbiddenFile
	}

	abs, err := SafeJoin(cfg.ExecPath(proj), rel)
	if err != nil {
		return FileContent{}, err // traversal / absolute path → handler 404
	}

	fi, err := os.Stat(abs)
	if err != nil {
		return FileContent{}, err // os.ErrNotExist → handler 404
	}
	if !fi.Mode().IsRegular() {
		return FileContent{}, fs.ErrNotExist // dir / special file: treat as not found
	}

	f, err := os.Open(abs)
	if err != nil {
		return FileContent{}, err
	}
	defer func() { _ = f.Close() }()

	// Read one byte past the cap to detect truncation without loading the whole
	// (possibly large) file.
	buf := make([]byte, maxKeyFileBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return FileContent{}, err
	}
	data := buf[:n]

	truncated := false
	if len(data) > maxKeyFileBytes {
		data = data[:maxKeyFileBytes]
		truncated = true
	}

	if !isTextContent(data) {
		return FileContent{}, ErrBinary
	}

	return FileContent{
		Name:      filepath.Base(abs),
		Size:      fi.Size(),
		Content:   string(data),
		Truncated: truncated,
	}, nil
}

// isTextContent reports whether data looks like text. A NUL byte is the reliable
// binary signal (same heuristic git uses); we avoid a strict UTF-8 check because
// truncation may slice a multibyte rune at the boundary of legitimate text.
func isTextContent(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0
}
