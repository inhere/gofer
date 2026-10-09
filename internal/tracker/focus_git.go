package tracker

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/inhere/gofer/internal/procattr"
)

// GitRunner runs `git -C dir args...` and returns its stdout (tests use fakes).
type GitRunner func(ctx context.Context, dir string, args ...string) (string, error)

// ExecGit is the real GitRunner; ctx bounds the child process.
func ExecGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	procattr.Background(cmd)
	out, err := cmd.Output()
	return string(out), err
}

// CollectFocusGit reads branch / HEAD / changes / ahead (`git status
// --porcelain=v2 --branch`, untracked files ignored), the latest tag (`git
// describe`) and the tracker's path prefix, in parallel. root is the repository
// root of the tracker ("" = unknown, git runs in trackerDir). An error means the
// directory is not usable as a git repository; a missing tag or upstream is not.
func CollectFocusGit(ctx context.Context, run GitRunner, root, trackerDir string) (*FocusGit, error) {
	dir := root
	if dir == "" {
		dir = trackerDir
	}
	var (
		wg                         sync.WaitGroup
		status, describe, prefix   string
		statusErr, descErr, preErr error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		status, statusErr = run(ctx, dir, "--no-optional-locks", "status", "--porcelain=v2", "--branch", "--untracked-files=no")
	}()
	go func() {
		defer wg.Done()
		describe, descErr = run(ctx, dir, "describe", "--tags", "--long", "--abbrev=8")
	}()
	go func() {
		defer wg.Done()
		prefix, preErr = run(ctx, trackerDir, "rev-parse", "--show-prefix")
	}()
	wg.Wait()
	if statusErr != nil {
		return nil, statusErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	trackerPrefix := ""
	if preErr == nil {
		trackerPrefix = strings.TrimSpace(prefix)
	}
	g, err := parseGitStatusV2(status, trackerPrefix)
	if err != nil {
		return nil, err
	}
	if descErr == nil {
		g.Tag, g.SinceTag, g.HasTag = parseGitDescribe(describe)
	}
	return g, nil
}

// parseGitStatusV2 parses `git status --porcelain=v2 --branch` output. Paths are
// relative to the repository top level, as is trackerPrefix (`rev-parse
// --show-prefix` of the tracker dir, "" = do not count tracker files).
func parseGitStatusV2(out, trackerPrefix string) (*FocusGit, error) {
	g := &FocusGit{}
	seenOID := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			seenOID = true
			if oid := strings.TrimPrefix(line, "# branch.oid "); oid != "(initial)" {
				g.Head = oid
				if len(g.Head) > 8 {
					g.Head = g.Head[:8]
				}
			}
		case strings.HasPrefix(line, "# branch.head "):
			if head := strings.TrimPrefix(line, "# branch.head "); head != "(detached)" {
				g.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			g.HasUpstream = true
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) > 0 {
				g.Ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "):
			g.Changed++
			if trackerPrefix != "" && strings.HasPrefix(statusV2Path(line), trackerPrefix) {
				g.TrackerChanged++
			}
		}
	}
	if !seenOID {
		return nil, errors.New("not a git status --porcelain=v2 --branch output")
	}
	return g, nil
}

// statusV2Path extracts the path of an ordinary (1), renamed (2) or unmerged (u)
// entry; the field counts before the path are fixed by the format.
func statusV2Path(line string) string {
	n := 9
	switch line[0] {
	case '2':
		n = 10
	case 'u':
		n = 11
	}
	fields := strings.SplitN(line, " ", n)
	if len(fields) < n {
		return ""
	}
	path := fields[n-1]
	if i := strings.IndexByte(path, '\t'); i >= 0 {
		path = path[:i]
	}
	return strings.Trim(path, `"`)
}

// parseGitDescribe parses `git describe --tags --long` ("v1.2.3-5-gabcdef12").
func parseGitDescribe(out string) (tag string, since int, ok bool) {
	out = strings.TrimSpace(out)
	g := strings.LastIndex(out, "-g")
	if g <= 0 {
		return "", 0, false
	}
	rest := out[:g]
	dash := strings.LastIndexByte(rest, '-')
	if dash <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest[dash+1:])
	if err != nil {
		return "", 0, false
	}
	return rest[:dash], n, true
}
