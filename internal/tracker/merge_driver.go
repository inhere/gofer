package tracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/procattr"
)

// Git merge driver wiring for the tracker JSONL files (gofer-7rqx).
const (
	// MergeDriverName is the driver name in .gitattributes and git config.
	MergeDriverName = "gofer-tracker"
	// MergeDriverDesc is merge.<name>.name (shown by git).
	MergeDriverDesc = "gofer tracker JSONL merge (by record id)"
	// MergeDriverCommand is merge.<name>.driver. It calls `gofer` from PATH: the
	// git config is shared by every worktree and often by a host and a container
	// mounting the same checkout, so an absolute binary path would be wrong for
	// one of them.
	MergeDriverCommand = "gofer repo merge-driver %O %A %B %P"
	// MergeAttributesLine routes the tracker files to the driver. The pattern is
	// relative to the .gitattributes beside .gofer/ and does not reach .local/.
	MergeAttributesLine = ".gofer/tracker/*.jsonl merge=" + MergeDriverName
)

// RunMergeDriver is `gofer repo merge-driver <base> <ours> <theirs> [<path>]`:
// git's %O %A %B %P. The result replaces the ours file. It returns the exit code
// git expects: 0 = merged, non-zero = conflict left for a person.
//
// A tracker file merges by record id (MergeJSONL); the decisions between two real
// changes are printed to stderr. A file the merge cannot read safely (unknown
// name, conflict markers, a field from a newer gofer) falls back to git's own
// text merge (`git merge-file`), so the outcome is exactly what git would have
// done without the driver: conflict markers and a non-zero exit when it collides.
func RunMergeDriver(base, ours, theirs, name string, stderr io.Writer) int {
	label := name
	if label == "" {
		label = filepath.Base(ours)
	}
	data := make([][]byte, 3)
	for i, p := range []string{base, ours, theirs} {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(stderr, "gofer merge-driver: %s: %v\n", label, err)
			return 1
		}
		data[i] = b
	}
	kind := MergeKindForPath(name)
	if kind == "" && name == "" {
		kind = SniffMergeKind(data[1], data[2], data[0])
	}
	if kind == "" {
		fmt.Fprintf(stderr, "gofer merge-driver: %s: not a tracker file this gofer knows; using a text merge\n", label)
		return textMerge(base, ours, theirs, label, stderr)
	}
	merged, conflicts, err := MergeJSONL(kind, data[0], data[1], data[2])
	if err != nil {
		fmt.Fprintf(stderr, "gofer merge-driver: %s: %v; using a text merge\n", label, err)
		return textMerge(base, ours, theirs, label, stderr)
	}
	if err := os.WriteFile(ours, merged, 0o644); err != nil {
		fmt.Fprintf(stderr, "gofer merge-driver: %s: %v\n", label, err)
		return 1
	}
	for _, c := range conflicts {
		fmt.Fprintf(stderr, "gofer merge-driver: %s: %s\n", label, c)
	}
	return 0
}

// textMerge runs git's line merge in place on ours (conflict markers on collision).
// merge-file needs no repository, but git still reads the one around its working
// directory and dies on a broken one, so it runs beside the ours file.
func textMerge(base, ours, theirs, label string, stderr io.Writer) int {
	paths := []string{ours, base, theirs}
	for i, p := range paths {
		if abs, err := filepath.Abs(p); err == nil {
			paths[i] = abs
		}
	}
	cmd := exec.Command("git", "merge-file", "-L", "ours", "-L", "base", "-L", "theirs", paths[0], paths[1], paths[2])
	cmd.Dir = filepath.Dir(paths[0])
	procattr.Background(cmd)
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		fmt.Fprintf(stderr, "gofer merge-driver: %s: git merge-file: %v\n", label, err)
	}
	return 1
}

// MergeDriverSetup reports what InstallMergeDriver did.
type MergeDriverSetup struct {
	// Attributes is the .gitattributes path; AttributesAdded is true when the
	// driver line was appended (the file must then be committed).
	Attributes      string `json:"attributes,omitempty"`
	AttributesAdded bool   `json:"attributes_added"`
	// ConfigChanged is true when merge.gofer-tracker.* was (re)written in the
	// clone's git config (shared by all of its worktrees).
	ConfigChanged bool `json:"config_changed"`
	// Skipped explains why nothing was installed ("" = installed).
	Skipped string `json:"skipped,omitempty"`
}

// InstallMergeDriver routes root's .gofer/tracker/*.jsonl to the gofer merge
// driver: the .gitattributes line beside .gofer/ and the merge.gofer-tracker
// entries in the clone's git config. Idempotent. A root outside a git work tree
// is skipped, not an error.
func InstallMergeDriver(ctx context.Context, run GitRunner, root string) (MergeDriverSetup, error) {
	var res MergeDriverSetup
	if out, err := run(ctx, root, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		res.Skipped = "not inside a git work tree"
		return res, nil
	}
	res.Attributes = filepath.Join(root, ".gitattributes")
	before, err := os.ReadFile(res.Attributes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	if err := appendOnce(res.Attributes, MergeAttributesLine+"\n"); err != nil {
		return res, err
	}
	after, err := os.ReadFile(res.Attributes)
	if err != nil {
		return res, err
	}
	res.AttributesAdded = string(after) != string(before)
	for _, kv := range [][2]string{
		{"merge." + MergeDriverName + ".name", MergeDriverDesc},
		{"merge." + MergeDriverName + ".driver", MergeDriverCommand},
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
