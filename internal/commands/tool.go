package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// tool.go implements the `gofer tool` group and its first two members, both from
// XFER-01 (design §〇/§一). G033 puts every new SMALL UTILITY command here instead
// of inventing another top-level group:
//
//	tool cp SRC DST       — push a local file to, or pull one from, a worker (or the
//	                        server host); the remote end is `<runner>:<project>/<path>`.
//	tool xfer ls|show|rm  — inspect and clean the server's staging area.
//
// The bytes always ride HTTP through internal/client; this file only parses the
// operands, prints progress and decides the exit code. Both ends of a `cp` are
// project-root-relative, i.e. the same boundary as a job's --cwd, and the runner
// re-validates them on its own machine.

const (
	// defaultCpTimeout bounds one transfer end-to-end: a large file legitimately
	// takes minutes, but a wedged worker must not hang the CLI forever.
	defaultCpTimeout = 600
	// cpPollInterval is the status poll cadence — live enough to look alive, slow
	// enough not to hammer the control plane.
	cpPollInterval = 500 * time.Millisecond
	// cpPartSuffix tags the in-flight download. The real destination only appears
	// once the bytes are verified, so a failed pull never leaves a file that looks
	// like a real result.
	cpPartSuffix = ".gofer-part"
	// toolExitErr is the process exit code of a failed `tool` command. It is
	// CODED (errorx.ErrorCoder, mirroring agent.go's probeExitErr / config.go's
	// configExitErr) because gcli derives a non-zero exit only from coded errors —
	// without it a scripting caller cannot tell a failed transfer from a good one.
	toolExitErr = 1
)

// toolFail is the coded failure every `tool` command returns. The reason text is
// carried through unchanged: the server's own words ("exists", "path escapes
// project", "worker offline", "too large") ARE the diagnosis, so nothing here
// rewords or hides them.
func toolFail(format string, a ...any) error {
	return errorx.Failf(toolExitErr, format, a...)
}

// toolCpOpts / toolXferLsOpts hold the subcommands' flags.
var toolCpOpts = struct {
	force   bool
	timeout int
}{}

var toolXferLsOpts = struct {
	state  string
	runner string
}{}

// NewToolCmd builds the `tool` command group (Category is assigned by NewApp's
// addGroup, like every other group).
func NewToolCmd() *gcli.Command {
	cp := &gcli.Command{
		Name:    "cp",
		Aliases: []string{"copy"},
		Desc:    "Copy one file between this host and a worker (or the server host)",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&toolCpOpts.force, "force", "f", false, "push: overwrite the file that already exists at the remote path")
			c.IntOpt(&toolCpOpts.timeout, "timeout", "", defaultCpTimeout, "seconds to wait for the transfer to finish")
			c.AddArg("src", "source: a local path, or <runner>:<project>/<path>", true)
			c.AddArg("dst", "destination: a local path, or <runner>:<project>/<path>", true)
		},
		Func: runToolCp,
	}
	ls := &gcli.Command{
		Name:    "ls",
		Aliases: []string{"list"},
		Desc:    "List the server's staging area",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.StrOpt(&toolXferLsOpts.state, "state", "", "", "filter by state: staged|dispatched|done|failed|expired")
			c.StrOpt(&toolXferLsOpts.runner, "runner", "", "", "filter by runner (a worker id, or server/local for the server host)")
		},
		Func: runToolXferLs,
	}
	show := &gcli.Command{
		Name: "show",
		Desc: "Print one transfer",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "transfer id", true)
		},
		Func: runToolXferShow,
	}
	rm := &gcli.Command{
		Name:    "rm",
		Aliases: []string{"remove", "delete"},
		Desc:    "Delete a transfer and its staged payload",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "transfer id", true)
		},
		Func: runToolXferRm,
	}
	return &gcli.Command{
		Name: "tool",
		Desc: "Small utilities: copy a file to/from a worker and manage the transfer staging area",
		Subs: []*gcli.Command{
			cp,
			{Name: "xfer", Desc: "Manage staged file transfers (XFER-01)", Subs: []*gcli.Command{ls, show, rm}},
		},
	}
}

// remoteSpec is one `tool cp` operand written `<runner>:<project>/<path>`: which
// machine to read/write on, in which project, and where inside that project.
type remoteSpec struct {
	runner  string // canonical runner key: a worker id, or "local" for the server host
	project string
	path    string
}

// cpPlan is a validated `tool cp` invocation. The local end is kept verbatim —
// it is a path on THIS host, so no separator is rewritten.
type cpPlan struct {
	push   bool // local -> remote (false: remote -> local)
	local  string
	remote remoteSpec
}

// parseRemoteSpec parses one cp operand. `<runner>:<project>/<path>` is REMOTE:
// the project is the first path segment after the colon and everything after it
// is the project-relative path, so later colons belong to the path. The runner is
// canonicalised on the way in (G043: the `server` alias becomes the wire's
// `local`). Anything else is a LOCAL path.
//
// The colon only separates when the prefix can be a runner at all: a Windows
// drive letter (`C:\fw.bin`) and a prefix carrying a separator (`a/b:c`) stay
// local, so a path with a colon is never misread as a remote target.
func parseRemoteSpec(s string) (remoteSpec, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return remoteSpec{}, false, errors.New("empty path")
	}
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return remoteSpec{}, false, nil
	}
	runner := s[:i]
	if len(runner) < 2 || strings.ContainsAny(runner, `/\`) {
		return remoteSpec{}, false, nil
	}
	rest := strings.ReplaceAll(s[i+1:], `\`, "/")
	seg := strings.SplitN(rest, "/", 2)
	if len(seg) < 2 || seg[0] == "" {
		return remoteSpec{}, false, fmt.Errorf("%q: a remote path is <runner>:<project>/<path> (the project is the first path segment)", s)
	}
	if seg[1] == "" {
		return remoteSpec{}, false, fmt.Errorf("%q: the remote path is empty", s)
	}
	return remoteSpec{runner: config.NormalizeRunnerName(runner), project: seg[0], path: seg[1]}, true, nil
}

// parseCpPair reads both operands: exactly ONE side is remote. v1 deliberately
// has no runner-to-runner transfer (design §一.1) — a copy between two workers
// goes through this host, where the user can see both ends.
func parseCpPair(src, dst string) (cpPlan, error) {
	srcSpec, srcRemote, err := parseRemoteSpec(src)
	if err != nil {
		return cpPlan{}, fmt.Errorf("SRC: %w", err)
	}
	dstSpec, dstRemote, err := parseRemoteSpec(dst)
	if err != nil {
		return cpPlan{}, fmt.Errorf("DST: %w", err)
	}
	switch {
	case srcRemote && dstRemote:
		return cpPlan{}, errors.New("both SRC and DST are remote: v1 transfers between this host and one runner — pass through a local file")
	case !srcRemote && !dstRemote:
		return cpPlan{}, errors.New("neither SRC nor DST is remote: write one side as <runner>:<project>/<path>")
	case dstRemote:
		return cpPlan{push: true, local: src, remote: dstSpec}, nil
	default:
		return cpPlan{push: false, local: dst, remote: srcSpec}, nil
	}
}

// runToolCp runs `gofer tool cp SRC DST`.
func runToolCp(c *gcli.Command, args []string) error {
	src := toolArg(c, args, 0, "src")
	dst := toolArg(c, args, 1, "dst")
	if src == "" || dst == "" {
		return toolFail("usage: gofer tool cp SRC DST — exactly one side is <runner>:<project>/<path>")
	}
	plan, err := parseCpPair(src, dst)
	if err != nil {
		return toolFail("%v", err)
	}
	timeout := toolCpOpts.timeout
	if timeout <= 0 {
		timeout = defaultCpTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	return runCpTransfer(ctx, c, cli, plan, toolCpOpts.force)
}

// runCpTransfer performs a parsed plan against an already-built client. It is the
// seam tests drive (they hand it a client pointed at a stub server) so the real
// push/pull code path — parsing, progress, polling, verify-then-rename — is what
// gets exercised.
func runCpTransfer(ctx context.Context, c *gcli.Command, cli *client.Client, plan cpPlan, force bool) error {
	if plan.push {
		return runCpPush(ctx, c, cli, plan, force)
	}
	return runCpPull(ctx, c, cli, plan)
}

// runCpPush stages the local file and waits for the runner to write it into its
// own project root.
func runCpPush(ctx context.Context, c *gcli.Command, cli *client.Client, plan cpPlan, force bool) error {
	st, err := os.Stat(plan.local)
	if err != nil {
		return toolFail("%v", err)
	}
	if st.IsDir() {
		return toolFail("%s is a directory: v1 transfers one file (pack it first: tar czf / Compress-Archive)", plan.local)
	}
	c.Printf("push %s (%s) -> %s\n", plan.local, humanBytes(st.Size()), remoteLabel(plan.remote))
	rec, err := cli.XferPut(ctx, plan.remote.runner, plan.remote.project, plan.remote.path, plan.local, force, transferProgress(c))
	if err != nil {
		return toolFail("%v", err)
	}
	c.Print("\n") // the percentage line was drawn in place
	rec, err = waitXfer(ctx, c, cli, rec.ID)
	if err != nil {
		return err
	}
	// The label comes from the plan, not the record: the plan's runner is already
	// canonical, which is the spelling `tool xfer ls --runner` filters on.
	c.Printf("done: %s (%s%s)\n", remoteLabel(plan.remote), humanBytes(rec.Size), shaSuffix(rec.SHA256))
	return nil
}

// runCpPull asks the runner for a file, waits until it is staged, then downloads
// it. The bytes land in `<DST>.gofer-part` and are renamed only after BOTH the
// download's digest and the transfer's recorded digest check out: a failed or
// corrupted pull leaves no file where a real result is expected.
func runCpPull(ctx context.Context, c *gcli.Command, cli *client.Client, plan cpPlan) error {
	c.Printf("pull %s -> %s\n", remoteLabel(plan.remote), plan.local)
	rec, err := cli.XferGet(ctx, plan.remote.runner, plan.remote.project, plan.remote.path)
	if err != nil {
		return toolFail("%v", err)
	}
	rec, err = waitXfer(ctx, c, cli, rec.ID)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(plan.local); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return toolFail("%v", err)
		}
	}
	part := plan.local + cpPartSuffix
	size, digest, err := cli.XferDownload(ctx, rec.ID, part)
	if err != nil {
		_ = os.Remove(part)
		return toolFail("%v", err)
	}
	if rec.SHA256 != "" && !strings.EqualFold(rec.SHA256, digest) {
		_ = os.Remove(part)
		return toolFail("sha256 mismatch: transfer %s recorded %s, the download hashed %s", rec.ID, rec.SHA256, digest)
	}
	if err := os.Rename(part, plan.local); err != nil {
		_ = os.Remove(part)
		return toolFail("%v", err)
	}
	c.Printf("done: %s (%s%s)\n", plan.local, humanBytes(size), shaSuffix(digest))
	return nil
}

// waitXfer polls a transfer until it is terminal. Every distinct state is printed:
// a large transfer spends most of its life dispatched, and seeing that is how a
// user tells "the worker is still reading" from "nothing is happening".
//
// A failed/expired transfer returns the record's OWN error text verbatim —
// "exists", "path escapes project", "worker offline", "too large". That phrase is
// the entire diagnosis, so it is never reworded or swallowed.
func waitXfer(ctx context.Context, c *gcli.Command, cli *client.Client, id string) (client.XferRecord, error) {
	last := ""
	for {
		rec, err := cli.XferStatus(id)
		if err != nil {
			return rec, toolFail("%v", err)
		}
		if rec.State != last {
			c.Printf("transfer %s: %s\n", rec.ID, rec.State)
			last = rec.State
		}
		switch rec.State {
		case client.XferStateDone:
			return rec, nil
		case client.XferStateFailed, client.XferStateExpired:
			if rec.Error != "" {
				// Verbatim: the record's own reason is the whole diagnosis.
				return rec, toolFail("%s", rec.Error)
			}
			return rec, toolFail("transfer %s ended in state %s with no reason recorded", rec.ID, rec.State)
		}
		select {
		case <-ctx.Done():
			return rec, toolFail("timed out waiting for transfer %s (state=%s): %v", rec.ID, rec.State, ctx.Err())
		case <-time.After(cpPollInterval):
		}
	}
}

// transferProgress renders upload progress as one line redrawn in place — a
// large file must not print a line per 1MB chunk.
func transferProgress(c *gcli.Command) func(sent, total int64) {
	return func(sent, total int64) {
		if total <= 0 {
			c.Printf("\r%s sent", humanBytes(sent))
			return
		}
		c.Printf("\r%3d%% (%s/%s)", sent*100/total, humanBytes(sent), humanBytes(total))
	}
}

// shaSuffix renders the digest for a one-line summary, or nothing at all when the
// server reported none (printing "sha256 " with no value would read as a bug).
func shaSuffix(digest string) string {
	if digest == "" {
		return ""
	}
	return ", sha256 " + digest
}

// remoteLabel names a target the way the command line writes it.
func remoteLabel(spec remoteSpec) string {
	return spec.runner + ":" + spec.project + "/" + spec.path
}

// runToolXferLs runs `gofer tool xfer ls`.
func runToolXferLs(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	return runXferLs(c, cli, toolXferLsOpts.state, toolXferLsOpts.runner)
}

// runXferLs lists the staging area. The runner filter is canonicalised exactly
// like a cp operand, because that is the spelling the server stores.
func runXferLs(c *gcli.Command, cli *client.Client, state, runner string) error {
	rows, err := cli.XferList(state, config.NormalizeRunnerName(runner))
	if err != nil {
		return toolFail("%v", err)
	}
	if len(rows) == 0 {
		c.Println("no transfers in the staging area")
		return nil
	}
	c.Printf("%-22s %-10s %-4s %-16s %-34s %10s  %s\n", "ID", "STATE", "OP", "RUNNER", "PROJECT:PATH", "SIZE", "CREATED")
	for _, r := range rows {
		c.Printf("%-22s %-10s %-4s %-16s %-34s %10s  %s\n",
			r.ID, r.State, r.Op, r.Runner, r.Project+":"+r.Path, humanBytes(r.Size), formatStarted(r.CreatedAt))
	}
	return nil
}

// runToolXferShow runs `gofer tool xfer show <id>`.
func runToolXferShow(c *gcli.Command, args []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	return runXferShow(c, cli, toolArg(c, args, 0, "id"))
}

// runXferShow prints every field a transfer carries. The absent ones (no error,
// never finished, no expiry) are omitted rather than shown as zero, so the
// presence of a value is itself information.
func runXferShow(c *gcli.Command, cli *client.Client, id string) error {
	if id == "" {
		return toolFail("xfer show requires an <id> argument")
	}
	rec, err := cli.XferStatus(id)
	if err != nil {
		return toolFail("%v", err)
	}
	c.Printf("id:          %s\n", rec.ID)
	c.Printf("op:          %s\n", rec.Op)
	c.Printf("state:       %s\n", rec.State)
	c.Printf("runner:      %s\n", rec.Runner)
	c.Printf("project:     %s\n", rec.Project)
	c.Printf("path:        %s\n", rec.Path)
	c.Printf("size:        %s\n", humanBytes(rec.Size))
	if rec.SHA256 != "" {
		c.Printf("sha256:      %s\n", rec.SHA256)
	}
	if rec.CallerID != "" {
		c.Printf("caller_id:   %s\n", rec.CallerID)
	}
	c.Printf("created_at:  %s\n", formatStarted(rec.CreatedAt))
	if rec.FinishedAt > 0 {
		c.Printf("finished_at: %s\n", formatStarted(rec.FinishedAt))
	}
	if rec.ExpiresAt > 0 {
		c.Printf("expires_at:  %s\n", formatStarted(rec.ExpiresAt))
	}
	if rec.Error != "" {
		c.Printf("error:       %s\n", rec.Error)
	}
	return nil
}

// runToolXferRm runs `gofer tool xfer rm <id>`.
func runToolXferRm(c *gcli.Command, args []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	return runXferRemove(c, cli, toolArg(c, args, 0, "id"))
}

// runXferRemove deletes a transfer and its staged payload.
func runXferRemove(c *gcli.Command, cli *client.Client, id string) error {
	if id == "" {
		return toolFail("xfer rm requires an <id> argument")
	}
	if err := cli.XferRemove(id); err != nil {
		return toolFail("%v", err)
	}
	c.Printf("removed transfer %s\n", id)
	return nil
}

// toolArg reads a positional operand: gcli binds it to the named arg, while a
// direct call (tests) passes the same value in the args slice.
func toolArg(c *gcli.Command, args []string, i int, name string) string {
	if i < len(args) {
		if v := strings.TrimSpace(args[i]); v != "" {
			return v
		}
	}
	return strings.TrimSpace(argString(c, name))
}

// humanBytes renders a byte count for a one-line summary or a table cell.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2fGB", float64(n)/(1024*1024*1024))
	}
}
