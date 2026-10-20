package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"
	"github.com/inhere/gofer/internal/changelog"
	"github.com/inhere/gofer/internal/tracker"
)

// newRepoChangelogMergeCmd is `gofer repo changelog-merge`: the git merge driver
// for a Keep a Changelog style CHANGELOG.md (logic in package changelog) and its
// installer.
func newRepoChangelogMergeCmd() *gcli.Command {
	var install bool
	var file, unreleased string
	return &gcli.Command{
		Name: "changelog-merge",
		Desc: "Git merge driver for a Keep a Changelog CHANGELOG.md (merges the unreleased section); --install sets it up in this clone",
		Help: "git runs it as `gofer repo changelog-merge %O %A %B %P` (base, ours, theirs, path); the result\n" +
			"replaces <ours>. Only the unreleased section is auto-merged: its `### ` subsections merge per\n" +
			"entry — ours keeps the entry order, theirs' new entries (multi-line and nested ones included)\n" +
			"are appended in theirs' order, an entry deleted on the other untouched side disappears, an\n" +
			"entry changed differently on both sides becomes a conflict marker. A subsection only one side\n" +
			"added is kept whole. The header and the released versions go through `git merge-file`, so a\n" +
			"region both sides changed comes back with git's standard conflict markers and a non-zero exit.\n" +
			"When one side released (renamed the unreleased heading to a version and opened a new\n" +
			"unreleased section) while the other added entries, the new entries land in the new section.\n\n" +
			"--install appends `" + changelog.AttributesLine("<file>") + "` to .gitattributes (commit it) and\n" +
			"sets merge." + changelog.DriverName + ".{name,driver} in the clone's git config, which every worktree of\n" +
			"the clone shares. Each new clone runs --install once.",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			c.BoolOpt(&install, "install", "", false, "configure the driver for this repository (.gitattributes + git config) instead of merging")
			c.StrOpt(&file, "file", "", "CHANGELOG.md", "changelog path relative to the repository root (--install)")
			c.StrOpt(&unreleased, "unreleased", "", "", "comma-separated section titles treated as unreleased (default: 未发布,Unreleased)")
			c.AddArg("base", "merge base version (%O)", false)
			c.AddArg("ours", "our version (%A); receives the result", false)
			c.AddArg("theirs", "their version (%B)", false)
			c.AddArg("path", "path of the file in the repository (%P)", false)
		},
		Func: func(c *gcli.Command, _ []string) error {
			if install {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				name := strings.TrimSpace(file)
				if name == "" {
					name = "CHANGELOG.md"
				}
				root, err := tracker.ExecGit(ctx, ".", "rev-parse", "--show-toplevel")
				if err != nil {
					return fmt.Errorf("not inside a git work tree: %w", err)
				}
				res, err := changelog.Install(ctx, tracker.ExecGit, strings.TrimSpace(root), name)
				if err != nil {
					return err
				}
				printNotes(c, changelogInstallNotes(res))
				return nil
			}
			base, ours, theirs := c.Arg("base").String(), c.Arg("ours").String(), c.Arg("theirs").String()
			if base == "" || ours == "" || theirs == "" {
				return fmt.Errorf("usage: gofer repo changelog-merge <base> <ours> <theirs> [<path>] (or --install)")
			}
			code := changelog.RunDriver(base, ours, theirs, c.Arg("path").String(), changelogOptions(unreleased), os.Stderr)
			if code != 0 {
				return errorx.Failf(code, "changelog merge left a conflict in %s", ours)
			}
			return nil
		},
	}
}

// changelogOptions turns the --unreleased flag into merge options.
func changelogOptions(titles string) changelog.Options {
	var out changelog.Options
	for _, title := range strings.Split(titles, ",") {
		if title = strings.TrimSpace(title); title != "" {
			out.UnreleasedTitles = append(out.UnreleasedTitles, title)
		}
	}
	return out
}

// changelogInstallNotes describes what installing the changelog merge driver did.
func changelogInstallNotes(res changelog.Setup) []string {
	switch {
	case res.Skipped != "":
		return []string{fmt.Sprintf("changelog merge driver: skipped (%s)", res.Skipped)}
	case !res.AttributesAdded && !res.ConfigChanged:
		return []string{"changelog merge driver: already installed"}
	}
	var notes []string
	if res.AttributesAdded {
		notes = append(notes, fmt.Sprintf("changelog merge driver: added `%s` to %s (commit it)", changelog.AttributesLine(res.File), res.Attributes))
	}
	if res.ConfigChanged {
		notes = append(notes, fmt.Sprintf("changelog merge driver: git config merge.%s.driver = %q (this clone and its worktrees)", changelog.DriverName, changelog.DriverCommand))
	}
	return notes
}
