package skill

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The Source values recorded in the index. They are what Update switches on to
// decide how to re-fetch, so they are part of the persisted contract, not an
// implementation detail.
const (
	sourceDir = "dir"
	sourceZip = "zip"
	sourceURL = "url"
	sourceGit = "git"
)

// httpTimeout bounds a remote fetch. A skill is prompt material (tens of KiB), so
// a dead endpoint must not hang `gofer agent skill import` — the CLI is a
// synchronous admin command.
const httpTimeout = 60 * time.Second

// httpClient is the fetcher for http(s) sources (var, not const, so a test can
// point it at an httptest server).
var httpClient = &http.Client{Timeout: httpTimeout}

// maxFetch caps a remote download. The extraction caps are the real gate, but a
// stream that keeps growing must be refused before it fills the disk: a zip of a
// skill that passes those caps can only exceed MaxTotalBytes by the overhead of
// members that are already compressed, so 4x headroom never rejects a skill the
// extractor would have accepted.
func (l Limits) maxFetch() int64 {
	const ceiling = 1 << 40 // 1TiB: past this the number stops meaning anything
	if l.MaxTotalBytes <= 0 || l.MaxTotalBytes > ceiling/4 {
		return ceiling
	}
	return l.MaxTotalBytes * 4
}

// fetchSource materialises one source spec inside work (a temp dir the caller owns)
// and reports the local path to read, the Source value to record, and the SourceRef
// Update replays verbatim.
//
// A remote source is always pulled into work first: import never reads from the
// network again after this call, and a fetch that fails halfway leaves only the
// temp dir behind. The recorded SourceRef is the spec as given (not a resolved
// URL/commit) precisely so Update can repeat exactly this resolution — the sha256
// file index in the skill record is what detects that the upstream moved.
func fetchSource(spec, work string, lim Limits) (local, kind, ref string, err error) {
	lower := strings.ToLower(spec)
	switch {
	case strings.HasPrefix(lower, "git+http://"), strings.HasPrefix(lower, "git+https://"):
		root, gerr := gitClone(spec, filepath.Join(work, "clone"))
		if gerr != nil {
			return "", "", "", gerr
		}
		return root, sourceGit, spec, nil
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		zipPath, derr := downloadZip(spec, filepath.Join(work, "download.zip"), lim)
		if derr != nil {
			return "", "", "", derr
		}
		return zipPath, sourceURL, spec, nil
	}

	abs, err := filepath.Abs(spec)
	if err != nil {
		return "", "", "", fmt.Errorf("skill: resolve %q: %w", spec, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", "", "", fmt.Errorf("skill: source %q: %w", spec, err)
	}
	if fi.IsDir() {
		return abs, sourceDir, abs, nil
	}
	if !fi.Mode().IsRegular() {
		return "", "", "", fmt.Errorf("%w: %s is neither a directory nor a .zip file", ErrInvalid, spec)
	}
	if !hasZipExt(abs) {
		return "", "", "", fmt.Errorf("%w: %s is not a .zip file", ErrInvalid, spec)
	}
	return abs, sourceZip, abs, nil
}

// gitClone shallow-clones a `git+https://host/repo.git[#subdir]` spec into dst and
// returns the directory that holds the skill.
//
// The system git is used on purpose — gofer has no git implementation of its own,
// and this is the same binary the import's user already has. When it is missing the
// error says which binary is missing and what to do instead, because "exec: git:
// executable file not found" from a deep call stack is not an actionable message.
func gitClone(spec, dst string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf(
			"skill: git is not on PATH, so %q cannot be fetched; install git or import a local directory/.zip",
			spec)
	}
	url, sub := splitGitSpec(spec)
	cmd := exec.Command("git", "clone", "--depth", "1", url, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// git writes the reason ("repository not found", auth failure) to stderr;
		// surfacing it is the difference between a fixable error and a mystery.
		return "", fmt.Errorf("skill: git clone %s: %w: %s", url, err, strings.TrimSpace(string(out)))
	}
	if sub == "" {
		return dst, nil
	}
	root, err := safeJoin(dst, sub)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("skill: %q has no #%s", spec, sub)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%w: #%s in %s is not a directory", ErrInvalid, sub, url)
	}
	return root, nil
}

// splitGitSpec separates the clone URL from the optional skill directory inside the
// repo: "git+https://host/repo.git#skills/foo" → ("https://host/repo.git",
// "skills/foo").
func splitGitSpec(spec string) (url, subdir string) {
	spec = strings.TrimPrefix(spec, "git+")
	i := strings.Index(spec, "#")
	if i < 0 {
		return spec, ""
	}
	sub := strings.ReplaceAll(spec[i+1:], "\\", "/")
	if j := strings.IndexAny(sub, "?#"); j >= 0 { // a subdir must not smuggle a query
		sub = sub[:j]
	}
	return spec[:i], strings.Trim(sub, "/")
}

// downloadZip fetches an http(s) .zip into dst. Only .zip URLs are accepted: the
// extractor cannot guess at an arbitrary content type, and refusing anything else
// keeps "the URL was actually an HTML error page" from becoming a confusing zip
// parse error.
func downloadZip(rawURL, dst string, lim Limits) (string, error) {
	if !hasZipExt(rawURL) {
		return "", fmt.Errorf("%w: %s is not a .zip URL (only .zip sources are fetched)", ErrInvalid, rawURL)
	}
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("skill: fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("skill: fetch %s: unexpected status %s", rawURL, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("skill: create %s: %w", dst, err)
	}
	defer f.Close()
	limit := lim.maxFetch()
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("skill: fetch %s: %w", rawURL, err)
	}
	if n > limit {
		return "", fmt.Errorf("%w: %s is larger than the fetch cap (%d bytes)", ErrTooLarge, rawURL, limit)
	}
	return dst, nil
}

// defaultNameFor is the name used when SKILL.md carries no frontmatter name: the
// source's own basename, minus the archive/repo decoration (.zip, .git). A
// frontmatter name always wins — this is the fallback for a hand-written SKILL.md
// that documents itself without a header.
func defaultNameFor(spec, kind, local string) string {
	switch kind {
	case sourceDir:
		// A directory may legitimately be named "my.skill", so no extension is
		// stripped here.
		return filepath.Base(local)
	case sourceZip:
		return strings.TrimSuffix(filepath.Base(local), filepath.Ext(local))
	case sourceURL:
		u := spec
		if i := strings.IndexAny(u, "?#"); i >= 0 {
			u = u[:i]
		}
		return strings.TrimSuffix(path.Base(strings.ReplaceAll(u, "\\", "/")), ".zip")
	case sourceGit:
		url, sub := splitGitSpec(spec)
		if sub != "" {
			return path.Base(sub)
		}
		return strings.TrimSuffix(path.Base(strings.ReplaceAll(url, "\\", "/")), ".git")
	}
	return ""
}
