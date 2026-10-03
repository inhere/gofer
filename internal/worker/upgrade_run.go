package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/wsproto"
)

// Flags the replacement process is started with (appended to the original args by the
// old process and stripped again before the next upgrade). They are hidden plumbing,
// not an operator interface.
const (
	FlagUpgradeFrom  = "upgrade-from"  // pid of the process being replaced
	FlagUpgradeID    = "upgrade-id"    // Upgrade.RequestID, echoed in Register.UpgradeID
	FlagUpgradeReady = "upgrade-ready" // marker file the new process writes once registered
)

// versionCheckTimeout bounds the `<candidate> --version` test run.
const versionCheckTimeout = 15 * time.Second

// downloadTimeout bounds the candidate download.
const downloadTimeout = 10 * time.Minute

// UpgradeDeps are the process-level facts the default UpgradeFunc needs. The command
// layer fills the paths (it owns the run-dir layout, G021); the function fields are
// test seams and default to the real implementations when nil.
type UpgradeDeps struct {
	// ExePath is the running binary (default: os.Executable).
	ExePath string
	// Args / Environ are what the new process is started with (default: os.Args[1:] /
	// os.Environ()). Upgrade flags already present in Args are stripped.
	Args    []string
	Environ []string
	// ReadyPath names the readiness marker file for an upgrade id.
	ReadyPath func(upgradeID string) string
	// LogPath receives the new process's stdout/stderr.
	LogPath string

	Fetch        func(ctx context.Context, urlPath, dst string, size int64) error
	CheckVersion func(ctx context.Context, bin string) (string, error)
	Spawn        func(spec SpawnSpec) (ChildProc, error)
	// Poll is the drain / readiness polling interval (default 200ms).
	Poll time.Duration
}

// SpawnSpec describes the replacement process to start.
type SpawnSpec struct {
	Exe     string
	Args    []string
	Env     []string
	LogPath string
}

// ChildProc is the started replacement process.
type ChildProc interface {
	Pid() int
	// Exited is closed when the process has exited.
	Exited() <-chan struct{}
	Kill() error
}

// Handover is set on a process that was started by an upgrade (the flags above): it
// registers with Register.UpgradeID and, once registered, takes over the pidfile and
// writes the readiness marker the old process is polling for.
type Handover struct {
	UpgradeID string
	FromPID   int
	ReadyPath string
	// OnReady runs once, after registration and before the marker is written (the
	// command layer repoints the daemon pidfile here).
	OnReady func()
}

// upgradeRun is one upgrade attempt of this process. All process effects go through
// seams so the drain / switch / rollback logic is testable without real processes.
type upgradeRun struct {
	deps     UpgradeDeps
	inflight func() int
	hold     func(bool) // suppress (true) / allow (false) reconnecting to the hub
	exit     func()     // graceful process exit after a successful handover
	pid      int
	// drainLimit / readyLimit override the frame's budgets (tests only).
	drainLimit time.Duration
	readyLimit time.Duration
}

func (cl *Client) newUpgradeRun(deps UpgradeDeps) *upgradeRun {
	return &upgradeRun{
		deps:     deps,
		inflight: func() int { return len(cl.inflightIDs()) },
		hold:     func(v bool) { cl.holdReconnect.Store(v) },
		exit:     cl.requestExit,
		pid:      os.Getpid(),
	}
}

func (u *upgradeRun) poll() time.Duration {
	if u.deps.Poll > 0 {
		return u.deps.Poll
	}
	return 200 * time.Millisecond
}

// run is the UpgradeFunc: download → verify → test-run → accepted → drain → switch →
// start the new process → wait for its readiness marker → exit (or roll back).
func (u *upgradeRun) run(ctx context.Context, req wsproto.Upgrade, accepted func(string)) error {
	exe := u.deps.ExePath
	if exe == "" {
		exe = runningBinaryPath()
	}
	if exe == "" {
		return errors.New("upgrade: cannot locate the running executable")
	}
	tmp := upgradeTempPath(exe)
	switched := false
	defer func() {
		if !switched {
			_ = os.Remove(tmp)
		}
	}()

	_ = os.Remove(tmp)
	fetch := u.deps.Fetch
	if fetch == nil {
		return errors.New("upgrade: no download source wired")
	}
	if err := fetch(ctx, req.URLPath, tmp, req.Size); err != nil {
		return fmt.Errorf("download upgrade binary: %w", err)
	}
	if err := VerifyUpgradeFile(tmp, req.Size, req.SHA256); err != nil {
		return err
	}
	if st, err := os.Stat(exe); err == nil {
		_ = os.Chmod(tmp, st.Mode().Perm()|0o100)
	} else {
		_ = os.Chmod(tmp, 0o755)
	}
	check := u.deps.CheckVersion
	if check == nil {
		check = runVersionCheck
	}
	version, err := check(ctx, tmp)
	if err != nil {
		return fmt.Errorf("candidate binary does not run: %w", err)
	}
	if want := firstField(req.Version); want != "" && !strings.Contains(version, want) {
		return fmt.Errorf("candidate binary reports version %q, expected %q", version, want)
	}
	accepted(version)

	if !req.Force {
		limit := secs(req.DrainTimeoutSec, wsproto.DefaultUpgradeDrainSec)
		if u.drainLimit > 0 {
			limit = u.drainLimit
		}
		if err := u.drain(ctx, limit); err != nil {
			return err
		}
	}

	ready := u.deps.ReadyPath
	if ready == nil {
		return errors.New("upgrade: no readiness marker path wired")
	}
	markerPath := ready(req.RequestID)
	_ = os.Remove(markerPath)

	rollback, err := SwitchBinary(exe, tmp)
	if err != nil {
		return err
	}
	switched = true
	u.hold(true)
	fail := func(cause error) error {
		u.hold(false)
		_ = os.Remove(markerPath)
		if rerr := rollback(); rerr != nil {
			cause = fmt.Errorf("%w (and restoring the old binary failed: %v)", cause, rerr)
		}
		return &RolledBackError{Err: cause}
	}

	spawn := u.deps.Spawn
	if spawn == nil {
		spawn = spawnDetached
	}
	env := u.deps.Environ
	if env == nil {
		env = os.Environ()
	}
	args := u.deps.Args
	if args == nil && len(os.Args) > 1 {
		args = os.Args[1:]
	}
	args = append(StripUpgradeArgs(args),
		"--"+FlagUpgradeFrom, strconv.Itoa(u.pid),
		"--"+FlagUpgradeID, req.RequestID,
		"--"+FlagUpgradeReady, markerPath)
	child, err := spawn(SpawnSpec{Exe: exe, Args: args, Env: env, LogPath: u.deps.LogPath})
	if err != nil {
		return fail(fmt.Errorf("start the new worker process: %w", err))
	}
	slog.Info("worker.upgrade_handover", "event", "worker.upgrade_handover", "component", "worker",
		"upgrade_id", req.RequestID, "new_pid", child.Pid(), "version", version)

	readyLimit := secs(req.ReadyTimeoutSec, wsproto.DefaultUpgradeReadySec)
	if u.readyLimit > 0 {
		readyLimit = u.readyLimit
	}
	if err := u.waitReady(ctx, child, markerPath, req.RequestID, readyLimit); err != nil {
		_ = child.Kill()
		select {
		case <-child.Exited():
		case <-time.After(5 * time.Second):
		}
		return fail(err)
	}
	slog.Info("worker.upgrade_done", "event", "worker.upgrade_done", "component", "worker",
		"upgrade_id", req.RequestID, "new_pid", child.Pid())
	_ = os.Remove(markerPath)
	u.exit()
	return nil
}

func secs(v, def int) time.Duration {
	if v <= 0 {
		v = def
	}
	return time.Duration(v) * time.Second
}

// drain waits until this process tracks no job, up to limit.
func (u *upgradeRun) drain(ctx context.Context, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		n := u.inflight()
		if n == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("drain timed out after %s with %d job(s) still running; upgrade abandoned, worker takes jobs again", limit, n)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(u.poll()):
		}
	}
}

// waitReady polls for the readiness marker the new process writes once registered.
// It fails when the child exits first or the budget runs out.
func (u *upgradeRun) waitReady(ctx context.Context, child ChildProc, path, upgradeID string, limit time.Duration) error {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(u.poll())
	defer tick.Stop()
	for {
		if readyMarkerMatches(path, upgradeID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-child.Exited():
			if readyMarkerMatches(path, upgradeID) {
				return nil
			}
			return errors.New("the new worker process exited before registering")
		case <-deadline.C:
			return fmt.Errorf("the new worker process did not register within %s", limit)
		case <-tick.C:
		}
	}
}

type readyMarker struct {
	UpgradeID string `json:"upgrade_id"`
	PID       int    `json:"pid"`
	At        int64  `json:"at"`
}

// WriteReadyMarker records that the process registered after an upgrade handover.
func WriteReadyMarker(path, upgradeID string) error {
	data, err := json.Marshal(readyMarker{UpgradeID: upgradeID, PID: os.Getpid(), At: time.Now().Unix()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readyMarkerMatches(path, upgradeID string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m readyMarker
	return json.Unmarshal(data, &m) == nil && m.UpgradeID == upgradeID
}

// StripUpgradeArgs removes the handover flags (both "--flag value" and "--flag=value"
// spellings) so a process that was itself started by an upgrade can start the next one.
func StripUpgradeArgs(args []string) []string {
	names := map[string]bool{FlagUpgradeFrom: true, FlagUpgradeID: true, FlagUpgradeReady: true}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		trim := strings.TrimLeft(a, "-")
		if len(trim) == len(a) || len(a)-len(trim) > 2 {
			out = append(out, a)
			continue
		}
		name, _, hasValue := strings.Cut(trim, "=")
		if !names[name] {
			out = append(out, a)
			continue
		}
		if !hasValue {
			i++ // skip the value
		}
	}
	return out
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)
var versionRE = regexp.MustCompile(`(?i)version:?\s*(\S+)`)

// runVersionCheck executes `<bin> --version` with a deadline and returns the version
// it prints (the whole trimmed line when the output has no "Version:" label).
func runVersionCheck(ctx context.Context, bin string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, versionCheckTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w (%s)", bin, err, strings.TrimSpace(string(out)))
	}
	return parseVersionOutput(string(out))
}

func parseVersionOutput(raw string) (string, error) {
	text := strings.TrimSpace(ansiRE.ReplaceAllString(raw, ""))
	if text == "" {
		return "", errors.New("--version printed nothing")
	}
	if m := versionRE.FindStringSubmatch(text); m != nil {
		return m[1], nil
	}
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line), nil
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

type execChild struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

func (c *execChild) Pid() int                { return c.cmd.Process.Pid }
func (c *execChild) Exited() <-chan struct{} { return c.exited }
func (c *execChild) Kill() error             { return daemon.KillDetached(c.cmd.Process) }

// spawnDetached starts the replacement process detached from this one (new session /
// no console), appending its output to spec.LogPath.
func spawnDetached(spec SpawnSpec) (ChildProc, error) {
	if spec.LogPath == "" {
		return nil, errors.New("no log path for the new process")
	}
	cmd, err := daemon.StartDetached(spec.Exe, spec.Args, spec.Env, spec.LogPath)
	if err != nil {
		return nil, err
	}
	ch := &execChild{cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait() // reap; also closes Exited so a crashing candidate fails fast
		close(ch.exited)
	}()
	return ch, nil
}

// fetchFromHub downloads urlPath (server-relative) from the hub this worker is
// connected to, authenticating with the worker's own token, into dst. At most size
// bytes are accepted.
func (cl *Client) fetchFromHub(ctx context.Context, urlPath, dst string, size int64) error {
	base := cl.hubBase()
	if base == "" {
		return errors.New("not connected to a hub")
	}
	cctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimRight(base, "/")+urlPath, nil)
	if err != nil {
		return err
	}
	cl.xferAuth(req)
	resp, err := xferHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("server answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > size {
		return fmt.Errorf("download exceeds the announced size %d", size)
	}
	return nil
}

// markHandoverReady runs once, right after this process registered with the hub as the
// replacement of another: it repoints the pidfile (OnReady) and writes the readiness
// marker the old process is polling for.
func (cl *Client) markHandoverReady() {
	h := cl.handover
	if h == nil {
		return
	}
	cl.handoverOnce.Do(func() {
		if h.OnReady != nil {
			h.OnReady()
		}
		if h.ReadyPath == "" {
			return
		}
		if err := WriteReadyMarker(h.ReadyPath, h.UpgradeID); err != nil {
			slog.Error("worker.upgrade_ready_marker_failed", "event", "worker.upgrade_ready_marker_failed", "component", "worker",
				"upgrade_id", h.UpgradeID, "path", h.ReadyPath, "error", err)
			return
		}
		slog.Info("worker.upgrade_ready", "event", "worker.upgrade_ready", "component", "worker",
			"upgrade_id", h.UpgradeID, "from_pid", h.FromPID)
	})
}

// requestExit asks this process to shut down gracefully (set by Serve).
func (cl *Client) requestExit() {
	if f := cl.exitFn; f != nil {
		f()
	}
}

// SetExit installs the graceful-shutdown trigger an upgrade handover calls once the
// new process is up. worker.Serve wires it to its signal context.
func (cl *Client) SetExit(f func()) { cl.exitFn = f }

// handoverID is the upgrade id a replacement process registers with ("" otherwise).
func (cl *Client) handoverID() string {
	if cl.handover == nil {
		return ""
	}
	return cl.handover.UpgradeID
}
