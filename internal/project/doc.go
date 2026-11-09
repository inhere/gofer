package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
)

// ErrProjectNotLocal is returned by ReadDoc when the project does not run on the
// server's own machine (allowed_runners excludes local), so the server cannot read
// its files (handler → 409 project_not_local).
var ErrProjectNotLocal = errors.New("project_not_local")

// ErrForbiddenDoc is returned by ReadDoc when the request is not an allowed doc
// read: empty / absolute / traversing / hidden-segment / ':' or '\' paths, or an
// extension outside the markdown whitelist (handler → 403).
var ErrForbiddenDoc = errors.New("forbidden doc")

// DocContent is the ReadDoc result. Path is the hit candidate relative to the
// project root, slash-separated (never an absolute path).
type DocContent struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// errDocNotFound is the single not-found error: it carries no path of any kind.
var errDocNotFound = fmt.Errorf("doc not found: %w", os.ErrNotExist)

// docExts is the extension whitelist (compared case-insensitively).
var docExts = map[string]bool{".md": true, ".markdown": true}

// validDocPath applies the rules shared by rel and base: no NUL, ':' (NTFS
// alternate data streams / drive letters) or '\' (Windows separators / UNC), not a
// POSIX absolute path, no ".." segment, and no segment starting with "." (hidden
// dirs / files such as .git, .env.md, .worktrees). Empty segments are ignored.
// (A base of exactly "." means the project root and is handled by the caller.)
func validDocPath(p string) bool {
	if strings.ContainsAny(p, "\x00:\\") || strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") { // also covers ".." and "."
			return false
		}
	}
	return true
}

// ReadDoc reads a markdown document under a project's execution root for the web
// doc reader. Order: AllowsLocalRunner (else ErrProjectNotLocal) → path rules
// (else ErrForbiddenDoc) → extension whitelist (else ErrForbiddenDoc) → candidates
// base/rel then rel, each resolved with SafeJoinReal (symlink-aware; a failure or
// escape counts as not found) and required to be a regular file → read with the
// maxKeyFileBytes cap (truncated, not rejected) → NUL means ErrBinary. Every
// not-found case returns the same path-free error wrapping os.ErrNotExist.
func ReadDoc(cfg *config.Config, proj config.ProjectConfig, base, rel string) (DocContent, error) {
	if !AllowsLocalRunner(proj.AllowedRunners) {
		return DocContent{}, ErrProjectNotLocal
	}
	if rel == "" || !validDocPath(rel) || (base != "." && !validDocPath(base)) {
		return DocContent{}, ErrForbiddenDoc
	}
	cleanRel := path.Clean(rel)
	if !docExts[strings.ToLower(path.Ext(cleanRel))] || cleanRel == "." || strings.HasSuffix(rel, "/") {
		return DocContent{}, ErrForbiddenDoc
	}
	cleanBase := path.Clean("/" + base)[1:] // "" or "." → "" (project root)

	candidates := make([]string, 0, 2)
	if cleanBase != "" {
		candidates = append(candidates, path.Join(cleanBase, cleanRel))
	}
	candidates = append(candidates, cleanRel)

	root := cfg.ExecPath(proj)
	for _, cand := range candidates {
		dc, err := readDocCandidate(root, cand)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return dc, err
	}
	return DocContent{}, errDocNotFound
}

// readDocCandidate resolves and reads one candidate; any resolve / stat / open
// problem is reported as errDocNotFound so no path detail can leak.
func readDocCandidate(root, cand string) (DocContent, error) {
	abs, err := SafeJoinReal(root, filepath.FromSlash(cand))
	if err != nil {
		return DocContent{}, errDocNotFound
	}
	// SafeJoinReal allows symlinks that stay inside the project, so the requested name
	// alone proves nothing: docs/x.md may point at .env or .git/config. The resolved
	// real path must pass the same segment and extension rules as the request.
	if !docRealPathAllowed(root, abs) {
		return DocContent{}, errDocNotFound
	}
	// Stat before open so a FIFO / device can never block the open.
	if st, err := os.Stat(abs); err != nil || !st.Mode().IsRegular() {
		return DocContent{}, errDocNotFound
	}
	f, err := os.Open(abs)
	if err != nil {
		return DocContent{}, errDocNotFound
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return DocContent{}, errDocNotFound
	}

	buf := make([]byte, maxKeyFileBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return DocContent{}, errDocNotFound
	}
	data := buf[:n]

	truncated := false
	if len(data) > maxKeyFileBytes {
		data = data[:maxKeyFileBytes]
		truncated = true
	}
	if !isTextContent(data) {
		return DocContent{}, ErrBinary
	}
	return DocContent{Path: cand, Size: fi.Size(), Content: string(data), Truncated: truncated}, nil
}

// docRealPathAllowed applies validDocPath and the extension whitelist to abs (a
// SafeJoinReal result, symlink-free) taken relative to the real project root.
func docRealPathAllowed(root, abs string) bool {
	rel, err := filepath.Rel(util.RealPath(root), abs)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return validDocPath(rel) && docExts[strings.ToLower(path.Ext(rel))]
}
