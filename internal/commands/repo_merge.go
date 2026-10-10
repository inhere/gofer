package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"
	"github.com/inhere/gofer/internal/tracker"
)

// newRepoMergeDriverCmd is `gofer repo merge-driver`: the git merge driver for
// .gofer/tracker/*.jsonl (logic in tracker.RunMergeDriver) and its installer.
func newRepoMergeDriverCmd() *gcli.Command {
	var install bool
	return &gcli.Command{
		Name: "merge-driver",
		Desc: "Git merge driver for .gofer/tracker/*.jsonl (merges records by id); --install sets it up in this clone",
		Help: "git runs it as `gofer repo merge-driver %O %A %B %P` (base, ours, theirs, path); the result\n" +
			"replaces <ours>. Records merge by issue id / memory key: one-sided adds, edits and deletes are\n" +
			"taken; a record edited on both sides merges field by field (a field both changed takes the side\n" +
			"with the newer updated_at), comments / notes / tags / deps are unioned; delete vs edit keeps the\n" +
			"edit. Decisions between two real edits are printed to stderr. A file it cannot read safely falls\n" +
			"back to git's text merge (conflict markers, non-zero exit).\n\n" +
			"--install (also run by `gofer repo init`) appends `" + tracker.MergeAttributesLine + "`\n" +
			"to .gitattributes (commit it) and sets merge." + tracker.MergeDriverName + ".{name,driver} in the clone's\n" +
			"git config, which every worktree of the clone shares. Each new clone runs --install once.",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			c.BoolOpt(&install, "install", "", false, "configure the driver for this repository (.gitattributes + git config) instead of merging")
			c.AddArg("base", "merge base version (%O)", false)
			c.AddArg("ours", "our version (%A); receives the result", false)
			c.AddArg("theirs", "their version (%B)", false)
			c.AddArg("path", "path of the file in the repository (%P)", false)
		},
		Func: func(c *gcli.Command, _ []string) error {
			if install {
				s, err := tracker.Discover(".", "")
				if err != nil {
					return err
				}
				printNotes(c, installMergeDriverNotes(filepath.Dir(filepath.Dir(s.Dir))))
				return nil
			}
			base, ours, theirs := c.Arg("base").String(), c.Arg("ours").String(), c.Arg("theirs").String()
			if base == "" || ours == "" || theirs == "" {
				return fmt.Errorf("usage: gofer repo merge-driver <base> <ours> <theirs> [<path>] (or --install)")
			}
			if code := tracker.RunMergeDriver(base, ours, theirs, c.Arg("path").String(), os.Stderr); code != 0 {
				return errorx.Failf(code, "tracker merge left a conflict in %s", ours)
			}
			return nil
		},
	}
}

// installMergeDriverNotes installs the merge driver for the repository at root
// and describes the outcome; a failure is a note, never fatal for the caller.
func installMergeDriverNotes(root string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := tracker.InstallMergeDriver(ctx, tracker.ExecGit, root)
	switch {
	case err != nil:
		return []string{fmt.Sprintf("merge driver: not installed: %v (retry with `gofer repo merge-driver --install`)", err)}
	case res.Skipped != "":
		return []string{fmt.Sprintf("merge driver: skipped (%s)", res.Skipped)}
	case !res.AttributesAdded && !res.ConfigChanged:
		return []string{"merge driver: already installed"}
	}
	var notes []string
	if res.AttributesAdded {
		notes = append(notes, fmt.Sprintf("merge driver: added `%s` to %s (commit it)", tracker.MergeAttributesLine, res.Attributes))
	}
	if res.ConfigChanged {
		notes = append(notes, fmt.Sprintf("merge driver: git config merge.%s.driver = %q (this clone and its worktrees)", tracker.MergeDriverName, tracker.MergeDriverCommand))
	}
	return notes
}
