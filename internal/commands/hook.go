package commands

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/hookrelay"
)

// hookOpts holds `gofer hook` flags.
var hookOpts = struct {
	wait    int
	poll    int
	runner  string
	project string
}{}

// hookLogMaxBytes truncates the hook log once it grows past this size (the
// hook runs on every turn of every session; no rotation daemon exists here).
const hookLogMaxBytes = 5 << 20

// NewHookCmd builds `gofer hook <agent>` — the executor Claude Code / Codex
// hook configs call (session relay, SESS-01 D1). It is not meant for humans:
// it reads the hook payload from stdin, reports the event to the hub and, on
// Stop with relay on, blocks for the web reply and prints the continuation
// JSON. It never exits non-zero for hub/network trouble (that would surface in
// the agent's UI); only a malformed invocation does.
func NewHookCmd() *gcli.Command {
	return &gcli.Command{
		Name: "hook",
		Desc: "Agent-CLI hook executor (called by .claude/settings.json / .codex/hooks.json; installs via `gofer init hooks`)",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("agent", "which CLI is calling: claude | codex", true)
			c.IntOpt(&hookOpts.wait, "wait", "", 0, "Stop: seconds to block for a web reply (default 540; set below the hook timeout)")
			c.IntOpt(&hookOpts.poll, "poll", "", 0, "Stop: long-poll window per request in seconds (default 25)")
			c.StrOpt(&hookOpts.runner, "runner", "", "${GOFER_HOOK_RUNNER}", "runner label to register the session under (default: worker id in worker mode, else server)")
			c.StrOpt(&hookOpts.project, "project", "p", "${GOFER_PROJECT}", "project key override (default: server matches cwd)")
		},
		Func: runHook,
	}
}

func runHook(c *gcli.Command, _ []string) error {
	agent := strings.ToLower(c.Arg("agent").String())
	p, err := hookrelay.ParsePayload(agent, os.Stdin)
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		// No usable server config: nothing to relay to. Quiet exit (the agent
		// keeps working normally); leave a trace in the hook log.
		logHook(fmt.Sprintf("%s %s no client: %v", agent, p.Event, err))
		return nil
	}
	logf, closeLog := openHookLog()
	defer closeLog()
	opts := hookrelay.Options{
		Runner:      resolveHookRunner(hookOpts.runner),
		ProjectKey:  hookOpts.project,
		TmuxPane:    os.Getenv("TMUX_PANE"),
		Wait:        time.Duration(hookOpts.wait) * time.Second,
		PollSec:     hookOpts.poll,
		CurrentFile: currentSessionFile(p.Cwd),
		Log:         logf,
	}
	res, err := hookrelay.Run(cli, p, opts)
	if err != nil {
		return err
	}
	if res.Blocked {
		os.Stdout.Write(hookrelay.BlockJSON(res.Reason))
		os.Stdout.Write([]byte{'\n'})
	}
	return nil
}

// resolveHookRunner picks the runner label for registration: the flag/env
// wins; in worker mode the worker.yaml worker_id; else "server".
func resolveHookRunner(flag string) string {
	if v := strings.TrimSpace(flag); v != "" {
		return v
	}
	if config.RunMode() == config.RunModeWorker {
		if path, err := config.UserWorkerConfigPath(); err == nil {
			if wc, err := loadWorkerConfig(path); err == nil && wc != nil && wc.WorkerID != "" {
				return wc.WorkerID
			}
		}
	}
	return "server"
}

// currentSessionFile is where SessionStart records the session id for this
// cwd: <config-dir>/run/sessions/<sha1(cwd)[:16]>. `gofer session relay` reads
// it as an offline fallback when the hub cannot resolve the cwd.
func currentSessionFile(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	sum := sha1.Sum([]byte(filepath.Clean(cwd)))
	return config.RuntimeFilePath(filepath.Join("run", "sessions"), hex.EncodeToString(sum[:])[:16])
}

// openHookLog opens <config-dir>/run/hook.log for appending, truncating it
// when it outgrew hookLogMaxBytes. Failures degrade to a discard writer.
func openHookLog() (*os.File, func()) {
	path := config.RuntimeFilePath("run", "hook.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, func() {}
	}
	if st, err := os.Stat(path); err == nil && st.Size() > hookLogMaxBytes {
		_ = os.Truncate(path, 0)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, func() {}
	}
	return f, func() { _ = f.Close() }
}

func logHook(line string) {
	f, closeLog := openHookLog()
	if f == nil {
		return
	}
	defer closeLog()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), line)
}
