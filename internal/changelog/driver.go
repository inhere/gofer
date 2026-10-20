package changelog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Git merge driver wiring for a Keep a Changelog style CHANGELOG.md (gofer-yf4p).
const (
	// DriverName is the driver name in .gitattributes and git config.
	DriverName = "gofer-changelog"
	// DriverDesc is merge.<name>.name (shown by git).
	DriverDesc = "gofer CHANGELOG merge (unreleased section per subsection)"
	// DriverCommand is merge.<name>.driver. Like the tracker driver it calls
	// `gofer` from PATH — the git config is shared by every worktree and often by a
	// host and a container mounting the same checkout — and falls back to git's own
	// text merge when the driver cannot run (no gofer on PATH, a gofer too old to
	// know the subcommand), so a "conflict" is never left unmarked.
	DriverCommand = "gofer repo changelog-merge %O %A %B %P" + DriverFallback
	// DriverFallback follows the driver invocation in DriverCommand (git runs the
	// driver through the shell, Git for Windows included).
	DriverFallback = "; rc=$?; [ $rc -le 1 ] && exit $rc; git merge-file %A %O %B"
)

// AttributesLine is the .gitattributes line routing file to the driver.
func AttributesLine(file string) string { return file + " merge=" + DriverName }

// GitRunner runs `git -C dir args...` and returns its stdout (tests use fakes).
// It has the same shape as tracker.GitRunner, so tracker.ExecGit fits.
type GitRunner func(ctx context.Context, dir string, args ...string) (string, error)

// RunDriver is `gofer repo changelog-merge <base> <ours> <theirs> [<path>]`:
// git's %O %A %B %P. The result replaces the ours file. It returns the exit code
// git expects: 0 = merged, 1 = conflict left for a person. A file the merge cannot
// handle (unreadable, git unavailable) falls back to git's text merge on the whole
// file, exactly what git would have done without the driver.
func RunDriver(base, ours, theirs, name string, opts Options, stderr io.Writer) int {
	label := name
	if label == "" {
		label = filepath.Base(ours)
	}
	data := make([][]byte, 3)
	for i, p := range []string{base, ours, theirs} {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(stderr, "gofer changelog-merge: %s: %v\n", label, err)
			return 1
		}
		data[i] = b
	}
	res, err := Merge(data[0], data[1], data[2], opts)
	if err != nil {
		fmt.Fprintf(stderr, "gofer changelog-merge: %s: %v; using a text merge\n", label, err)
		return textMerge(data[0], data[1], data[2], ours, label, stderr)
	}
	if err := os.WriteFile(ours, res.Merged, 0o644); err != nil {
		fmt.Fprintf(stderr, "gofer changelog-merge: %s: %v\n", label, err)
		return 1
	}
	for _, note := range res.Notes {
		fmt.Fprintf(stderr, "gofer changelog-merge: %s: %s\n", label, note)
	}
	if res.Conflict {
		fmt.Fprintf(stderr, "gofer changelog-merge: %s: conflict markers left in the file\n", label)
		return 1
	}
	return 0
}

// textMerge replaces oursPath with git's line merge of the three whole files.
func textMerge(base, ours, theirs []byte, oursPath, label string, stderr io.Writer) int {
	res, conflict, err := MergeFileText(base, ours, theirs)
	if err != nil {
		fmt.Fprintf(stderr, "gofer changelog-merge: %s: %v\n", label, err)
		return 1
	}
	if err := os.WriteFile(oursPath, res, 0o644); err != nil {
		fmt.Fprintf(stderr, "gofer changelog-merge: %s: %v\n", label, err)
		return 1
	}
	if conflict {
		return 1
	}
	return 0
}

// Setup reports what Install did.
type Setup struct {
	// Attributes is the .gitattributes path; AttributesAdded is true when the
	// driver line was appended (the file must then be committed).
	Attributes      string `json:"attributes,omitempty"`
	AttributesAdded bool   `json:"attributes_added"`
	// ConfigChanged is true when merge.<DriverName>.* was (re)written in the
	// clone's git config (shared by all of its worktrees).
	ConfigChanged bool `json:"config_changed"`
	// File is the changelog path the .gitattributes line routes.
	File string `json:"file"`
	// Skipped explains why nothing was installed ("" = installed).
	Skipped string `json:"skipped,omitempty"`
}

// Install routes file (a path relative to the repository root) to the gofer
// changelog driver: the .gitattributes line and the merge.<DriverName> entries in
// the clone's git config. Idempotent. A root outside a git work tree is skipped,
// not an error.
func Install(ctx context.Context, run GitRunner, root, file string) (Setup, error) {
	res := Setup{File: file}
	if out, err := run(ctx, root, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		res.Skipped = "not inside a git work tree"
		return res, nil
	}
	res.Attributes = filepath.Join(root, ".gitattributes")
	before, err := os.ReadFile(res.Attributes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	if err := appendOnce(res.Attributes, AttributesLine(file)+"\n"); err != nil {
		return res, err
	}
	after, err := os.ReadFile(res.Attributes)
	if err != nil {
		return res, err
	}
	res.AttributesAdded = string(after) != string(before)
	for _, kv := range [][2]string{
		{"merge." + DriverName + ".name", DriverDesc},
		{"merge." + DriverName + ".driver", DriverCommand},
	} {
		cur, _ := run(ctx, root, "config", "--get", kv[0])
		if strings.TrimSpace(cur) == kv[1] {
			continue
		}
		if _, err := run(ctx, root, "config", kv[0], kv[1]); err != nil {
			return res, fmt.Errorf("git config %s: %w", kv[0], err)
		}
		res.ConfigChanged = true
	}
	return res, nil
}

// appendOnce appends line to path unless it is already there (line-wise).
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
	return os.WriteFile(path, out, 0o644)
}
