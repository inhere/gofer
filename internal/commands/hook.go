package commands

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/hookrelay"
)

// hookOpts holds `gofer hook` flags.
var hookOpts = struct {
	wait    int
	poll    int
	runner  string
	project string
	agent   string
}{}

// hookLogMaxBytes truncates the hook log once it grows past this size (the
// hook runs on every turn of every session; no rotation daemon exists here).
const hookLogMaxBytes = 5 << 20

// hookMemoryTimeout bounds each server memory list of the prompt injection.
const hookMemoryTimeout = 250 * time.Millisecond

// hookCommandMemoryTimeout bounds each server memory list of the PreToolUse
// command injection, which runs before every shell call: a dead hub must cost
// next to nothing (the whole path is capped by hookrelay.DefaultCommandMemoryDeadline).
const hookCommandMemoryTimeout = 100 * time.Millisecond

// hookCommandMinDeadline is the least time the command injection still gets
// when process start-up already ate most of the budget.
const hookCommandMinDeadline = 20 * time.Millisecond

// NewHookCmd builds `gofer hook <agent>` — the executor Claude Code / Codex
// hook configs call (session relay, SESS-01 D1). It is not meant for humans:
// it reads the hook payload from stdin, reports the event to the hub and, on
// Stop with relay on, blocks for the web reply and prints the continuation
// JSON. It never exits non-zero for hub/network trouble (that would surface in
// the agent's UI); only a malformed invocation does.
func NewHookCmd() *gcli.Command {
	return &gcli.Command{
		Name: "hook",
		Desc: "Agent-CLI hook executor (called by .claude/settings.json / .codex/hooks.json / the omp extension / jcode config.toml; installs via `gofer init hooks`)",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("agent", "which CLI is calling: claude | codex | omp | jcode | generic (a self-built agent, needs --agent)", true)
			c.StrOpt(&hookOpts.agent, "agent", "", "", "generic: the gofer agent key the session is registered under (its session_resume / session_resume_interactive templates drive resume and takeover)")
			c.IntOpt(&hookOpts.wait, "wait", "", 0, "Stop: seconds to block for a web reply (default 540; set below the hook timeout)")
			c.IntOpt(&hookOpts.poll, "poll", "", 0, "Stop: long-poll window per request in seconds (default 25)")
			c.StrOpt(&hookOpts.runner, "runner", "", "${GOFER_HOOK_RUNNER}", "runner label to register the session under (default: worker id in worker mode, else server)")
			c.StrOpt(&hookOpts.project, "project", "p", "${GOFER_PROJECT}", "project key override (default: server matches cwd)")
		},
		Func: runHook,
	}
}

func runHook(c *gcli.Command, _ []string) error {
	started := time.Now()
	agent := strings.ToLower(c.Arg("agent").String())
	if hookInsideJob() {
		logHook(fmt.Sprintf("%s bypass relay: %s is set", agent, hookBypassReason()))
		return nil
	}
	var p hookrelay.Payload
	var err error
	if agent == hookrelay.DialectGeneric {
		p, err = hookrelay.ParseGenericPayload(hookOpts.agent, os.Stdin)
		agent = p.Agent
	} else if agent == hookrelay.AgentJcode {
		// jcode hands the event over in JCODE_HOOK_* env vars, stdin is empty.
		p, err = hookrelay.ParseJcodePayload(os.Getenv, os.Stdin)
	} else {
		p, err = hookrelay.ParsePayload(agent, os.Stdin)
	}
	if err != nil {
		return err
	}
	if p.Event == "" {
		return nil // an event gofer has no use for (jcode pre_tool, ...)
	}
	if hookrelay.IgnoredCwd(p.Cwd, "", nil) {
		// An agent-internal session (codex memories, ...): not the person's work,
		// so no register / heartbeat / work item. Silent and exit 0.
		return nil
	}
	if p.Event == "PreToolUse" {
		return runPreToolUseHook(p, started)
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
	progressSec := config.DefaultSessionProgressIntervalSec
	cfg, _, cfgErr := config.Load(config.InputCfgFile)
	if cfgErr == nil {
		progressSec = cfg.EffectiveSessionProgressIntervalSec()
	}
	runDir := filepath.Dir(config.RuntimeFilePath("run", "hook.log"))
	opts := hookrelay.Options{
		Runner:           resolveHookRunner(hookOpts.runner),
		ProjectKey:       hookOpts.project,
		TmuxPane:         os.Getenv("TMUX_PANE"),
		Wait:             time.Duration(hookOpts.wait) * time.Second,
		PollSec:          hookOpts.poll,
		ProgressInterval: time.Duration(progressSec) * time.Second,
		ProgressStateDir: filepath.Join(runDir, "session-progress"),
		UsageStateDir:    filepath.Join(runDir, "hook-usage"),
		CurrentFile:      currentSessionFile(p.Cwd),
		Log:              logf,
	}
	if p.Event == "UserPromptSubmit" {
		// Keyword-triggered memories (design 2026-10-09 §2.9): the server lists use
		// the same short deadline as the session prime, so a dead hub costs ≤ 250ms.
		lister := client.NewWithTimeout(cli.BaseURL(), cli.Token(), hookMemoryTimeout)
		opts.PromptMemories = hookrelay.NewMemoryLoader(lister, hookProjectFor(cfg, cfgErr))
		opts.MemoryStateDir = filepath.Join(runDir, "prompt-memory")
	}
	if p.Event == "Stop" {
		stopWatching := abortReportOnSignal(func() { hookrelay.ReportInterrupt(cli, p, opts) }, os.Exit)
		defer stopWatching()
	}
	res, err := hookrelay.Run(cli, p, opts)
	if err != nil {
		return err
	}
	// A notice is meant for the PERSON at this terminal (the session was taken over
	// on the web, §9.1 B): stderr is where the agent CLI shows them text, and it is
	// the channel the hook otherwise never uses (the decision channel is stdout).
	if res.Notice != "" {
		fmt.Fprintln(os.Stderr, res.Notice)
	}
	printHookContext(p.Event, res.Context)
	if res.Blocked {
		os.Stdout.Write(hookrelay.BlockJSON(res.Reason))
		os.Stdout.Write([]byte{'\n'})
	}
	return nil
}

// printHookContext prints ctx as hookSpecificOutput.additionalContext (claude,
// codex and generic all read it on SessionStart / UserPromptSubmit / PreToolUse).
func printHookContext(event, ctx string) {
	if ctx == "" {
		return
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": event, "additionalContext": ctx}})
	os.Stdout.Write(out)
	os.Stdout.Write([]byte{'\n'})
}

// hookProjectFor resolves the server project of a cwd for the memory loaders:
// the --project flag / GOFER_PROJECT, else the config's project mapping.
func hookProjectFor(cfg *config.Config, cfgErr error) func(cwd string) string {
	return func(cwd string) string {
		if key := strings.TrimSpace(hookOpts.project); key != "" {
			return key
		}
		if cfgErr != nil || cfg == nil {
			return ""
		}
		key, _ := cfg.ProjectForPath(cwd)
		return key
	}
}

// runPreToolUseHook is the PreToolUse path (design 2026-10-09 §2.9, P5): inject
// the memories whose when.commands prefix the shell command about to run. It
// never talks to the session hub (no register / heartbeat), works without a
// server config (repository memories only), stays within
// hookrelay.DefaultCommandMemoryDeadline counted from process start, and prints
// nothing on any failure — the tool call must never be held back.
func runPreToolUseHook(p hookrelay.Payload, started time.Time) error {
	logf, closeLog := openHookLog()
	defer closeLog()
	cfg, _, cfgErr := config.Load(config.InputCfgFile)
	var lister hookrelay.ScopedMemoryLister
	if cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token); err == nil {
		lister = client.NewWithTimeout(cli.BaseURL(), cli.Token(), hookCommandMemoryTimeout)
	}
	deadline := hookrelay.DefaultCommandMemoryDeadline - time.Since(started)
	if deadline < hookCommandMinDeadline {
		deadline = hookCommandMinDeadline
	}
	runDir := filepath.Dir(config.RuntimeFilePath("run", "hook.log"))
	res, err := hookrelay.Run(nil, p, hookrelay.Options{
		CommandMemories: hookrelay.NewCommandMemoryLoader(lister, hookProjectFor(cfg, cfgErr)),
		CommandDeadline: deadline,
		MemoryStateDir:  filepath.Join(runDir, "prompt-memory"),
		Log:             logf,
	})
	if err != nil {
		return nil
	}
	printHookContext(p.Event, res.Context)
	return nil
}

// hookAbortGrace bounds how long the dying hook may spend telling the hub: the
// agent CLI may follow its signal with a hard kill, and a hub that stalls must not
// keep the process alive either.
const hookAbortGrace = 3 * time.Second

// abortReportOnSignal arms the Esc handling of the blocking Stop hook: the first
// SIGINT/SIGTERM/SIGHUP runs report (bounded by hookAbortGrace) and exits 130.
// The returned func disarms it (the hook finished on its own).
func abortReportOnSignal(report func(), exit func(int)) (disarm func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			fin := make(chan struct{})
			go func() { report(); close(fin) }()
			select {
			case <-fin:
			case <-time.After(hookAbortGrace):
			}
			exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sigs)
		close(done)
	}
}

func hookInsideJob() bool {
	return hookBypassReason() != ""
}

func hookBypassReason() string {
	if strings.TrimSpace(os.Getenv("GOFER_JOB_ID")) != "" {
		return "GOFER_JOB_ID"
	}
	if strings.TrimSpace(os.Getenv("GOFER_MESSENGER")) == "1" {
		return "GOFER_MESSENGER"
	}
	return ""
}

// resolveHookRunner picks the runner label for registration: the flag/env
// wins; in worker mode the worker.yaml worker_id; in client mode nothing (see
// below); else "server".
//
// Client mode (GOFER_RUN_MODE=client) must NOT claim the server: a client node
// runs the session SOMEWHERE ELSE (typically inside a container that has no
// gofer of its own), so the honest label is the one the environment provides —
// GOFER_HOOK_RUNNER, the worker the container runs — and otherwise empty. The
// server reads runner="" as "this session did not say where it runs" and refuses
// to deliver to it with a reason (design §9.1 v0.5) instead of dispatching into
// the void.
func resolveHookRunner(flag string) string {
	if v := strings.TrimSpace(flag); v != "" {
		return v
	}
	switch config.RunMode() {
	case config.RunModeWorker:
		if path, err := config.UserWorkerConfigPath(); err == nil {
			if wc, err := loadWorkerConfig(path); err == nil && wc != nil && wc.WorkerID != "" {
				return wc.WorkerID
			}
		}
	case config.RunModeClient:
		return ""
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
