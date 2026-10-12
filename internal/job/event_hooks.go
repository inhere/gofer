package job

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/proctree"
	"github.com/inhere/gofer/internal/secret"
	"github.com/inhere/gofer/internal/util"
)

// Event hook executor (AUTO-04, docs/design/2026-10-11-event-hooks-design.md §四).
// A hook is a read-only side channel: its exit code decides only whether THIS
// delivery is done or retried; nothing it prints flows back into gofer.

const (
	// eventHookParallel caps the hook processes one sweep runs at the same time.
	eventHookParallel = 4
	// eventHookStderrTail is how much of the hook's stderr (its END) is kept for
	// last_error and the failure log.
	eventHookStderrTail = 1024
	// eventHookWaitDelay bounds Wait on the stderr copy after the hook exits, so an
	// orphaned descendant holding the pipe cannot wedge the sweep (same reasoning as
	// the local runner's stdioWaitDelay).
	eventHookWaitDelay = 2 * time.Second
)

// Environment variables gofer sets for a hook process (before the hook's own env).
const (
	EnvHookEventType  = "GOFER_EVENT_TYPE"
	EnvHookEventSeq   = "GOFER_EVENT_SEQ"
	EnvHookDeliveryID = "GOFER_DELIVERY_ID"
	EnvHookJobID      = "GOFER_JOB_ID"
	EnvHookName       = "GOFER_HOOK_NAME"
)

// runHookDeliveries runs the claimed hook rows of one sweep, at most
// eventHookParallel at a time, and returns when every started run has finished.
// Rows not started before ctx ends stay leased and come back after the lease.
func (s *Service) runHookDeliveries(ctx context.Context, cfg *config.Config, rows []jobstore.Delivery) {
	sem := make(chan struct{}, eventHookParallel)
	var wg sync.WaitGroup
	for _, d := range rows {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(d jobstore.Delivery) {
			defer wg.Done()
			defer func() { <-sem }()
			s.runHookDelivery(ctx, cfg, d)
		}(d)
	}
	wg.Wait()
}

// runHookDelivery runs one hook row and records its outcome with the webhook
// backoff table: exit 0 => delivered; non-zero / timeout / start failure => retry,
// or failed once the hook's max_attempts is reached. A hook that was removed or
// disabled after the row was enqueued fails the row without running anything.
func (s *Service) runHookDelivery(ctx context.Context, cfg *config.Config, d jobstore.Delivery) {
	name, _ := config.EventHookName(d.Target)
	hook, found := cfg.EventHook(name)
	if !found || !hook.IsEnabled() {
		reason := "hook removed"
		if found {
			reason = "hook disabled"
		}
		if err := s.meta.MarkFailed(d.ID, d.Attempts, reason, s.nowFn().Unix()); err != nil {
			slog.Warn("DeliverDue: mark hook failed", "delivery_id", d.ID, "err", err)
		}
		slog.Warn("event_hook.failed", "event", "event_hook.failed", "name", name, "delivery_id", d.ID, "err", reason)
		return
	}
	body, eventType, ok := s.buildDeliveryBody(d)
	if !ok {
		_ = s.meta.MarkFailed(d.ID, d.Attempts+1, "source event missing", s.nowFn().Unix())
		return
	}

	run := runEventHookProcess(ctx, hook, hook.EffectiveDir(), eventHookEnv(cfg, hook, d, eventType), body)
	if run.err != nil && ctx.Err() != nil {
		// The sweep itself was cancelled (serve shutdown), not the hook's fault: leave
		// the row leased so it runs again after the lease, without spending an attempt.
		slog.Warn("event_hook.failed", "event", "event_hook.failed", "name", hook.Name, "event_type", eventType,
			"delivery_id", d.ID, "err", "interrupted by shutdown")
		return
	}
	now := s.nowFn().Unix()
	if run.err == nil {
		if err := s.meta.MarkDelivered(d.ID, now); err != nil {
			slog.Warn("DeliverDue: mark hook delivered", "delivery_id", d.ID, "err", err)
		}
		slog.Info("event_hook.ran", "event", "event_hook.ran", "name", hook.Name, "event_type", eventType,
			"delivery_id", d.ID, "exit_code", run.exitCode, "duration_ms", run.duration.Milliseconds())
		return
	}

	lastErr := redactHookText(hookErrorText(run), hook.Env)
	attempts := d.Attempts + 1
	slog.Warn("event_hook.failed", "event", "event_hook.failed", "name", hook.Name, "event_type", eventType,
		"delivery_id", d.ID, "exit_code", run.exitCode, "duration_ms", run.duration.Milliseconds(),
		"attempt", attempts, "err", lastErr)
	if attempts >= hook.EffectiveMaxAttempts() {
		if err := s.meta.MarkFailed(d.ID, attempts, lastErr, now); err != nil {
			slog.Warn("DeliverDue: mark hook failed", "delivery_id", d.ID, "err", err)
		}
		return
	}
	nextAt := now + int64(nextBackoff(attempts)/time.Second)
	if err := s.meta.MarkRetry(d.ID, attempts, nextAt, lastErr, now); err != nil {
		slog.Warn("DeliverDue: mark hook retry", "delivery_id", d.ID, "err", err)
	}
}

// eventHookEnv is the hook process environment: the serve environment minus the
// SEC-01 credential denylist (the same list a job gets), plus the event identity,
// plus the hook's own env last (it may override anything above).
func eventHookEnv(cfg *config.Config, hook config.EventHookConfig, d jobstore.Delivery, eventType string) []string {
	seq := ""
	if d.EventSeq > 0 {
		seq = strconv.FormatInt(d.EventSeq, 10)
	}
	jobID := d.JobID
	if strings.Contains(jobID, ":") {
		jobID = "" // a scope id (plan:<id>, xfer:<id>, …) is not a job
	}
	ids := map[string]string{
		EnvHookEventType:  eventType,
		EnvHookEventSeq:   seq,
		EnvHookDeliveryID: strconv.FormatInt(d.ID, 10),
		EnvHookJobID:      jobID,
		EnvHookName:       hook.Name,
	}
	return util.EnvironWithout(effectiveJobEnvDeny(cfg), nil, util.MergeEnv(ids, hook.Env))
}

// hookRun is the outcome of one hook process.
type hookRun struct {
	exitCode int
	duration time.Duration
	stderr   string // the last eventHookStderrTail bytes, not yet redacted
	err      error  // nil = exit 0
}

// runEventHookProcess runs the hook command once: argv form (no shell), stdin = the
// event body, stdout to the null device (a read-only side channel has no use for it
// and nothing can block on it), stderr tail kept, and the whole process tree killed
// when the hook's timeout (or ctx) ends it.
func runEventHookProcess(ctx context.Context, hook config.EventHookConfig, dir string, env []string, stdin []byte) hookRun {
	return runHookCommand(ctx, hook.Name, hook.Command, hook.EffectiveTimeout(), dir, env, stdin, nil)
}

// runHookCommand runs a hook argv once (event and decision hooks alike): no shell,
// stdin given, stdout to w (nil = the null device), the stderr tail kept, and the
// whole process tree killed when timeout (or ctx) ends it.
func runHookCommand(ctx context.Context, name string, argv []string, timeout time.Duration, dir string, env []string, stdin []byte, stdout io.Writer) hookRun {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	procattr.Background(cmd)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = stdout
	stderr := &tailWriter{max: eventHookStderrTail}
	cmd.Stderr = stderr
	cmd.WaitDelay = eventHookWaitDelay
	tree := proctree.New()
	tree.Configure(cmd)
	defer tree.Release()

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return hookRun{exitCode: -1, err: fmt.Errorf("start: %w", err)}
	}
	if err := tree.Attach(cmd); err != nil {
		slog.Warn("hook: cannot contain the process tree", "name", name, "pid", cmd.Process.Pid, "err", err)
	}
	waitErr := cmd.Wait()
	if runCtx.Err() != nil {
		tree.Kill()
	}
	res := hookRun{exitCode: -1, duration: time.Since(start), stderr: stderr.String()}
	if cmd.ProcessState != nil {
		res.exitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() != nil:
		res.err = ctx.Err()
	case runCtx.Err() != nil:
		res.err = fmt.Errorf("timeout after %s", timeout)
	case waitErr == nil:
	case errors.Is(waitErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success():
		// Exited 0; only an orphaned descendant kept stderr open past WaitDelay.
	default:
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.err = fmt.Errorf("exit status %d", exitErr.ExitCode())
		} else {
			res.err = waitErr
		}
	}
	return res
}

// hookErrorText is the last_error of a failed run: the reason plus the stderr tail.
func hookErrorText(run hookRun) string {
	msg := run.err.Error()
	if tail := strings.TrimSpace(run.stderr); tail != "" {
		msg += ": " + tail
	}
	return msg
}

// redactHookText scrubs text that leaves the hook (last_error, the serve log): the
// hook's own env values first (they are the secrets most likely to be echoed), then
// the generic credential patterns.
func redactHookText(s string, env map[string]string) string {
	values := make([]string, 0, len(env))
	for _, v := range env {
		if len(v) >= 4 {
			values = append(values, v)
		}
	}
	// Longest first, so a value that contains another is replaced whole.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		s = strings.ReplaceAll(s, v, secret.Placeholder)
	}
	s, _ = secret.RedactString(s)
	return s
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	// The cut may land inside a multi-byte rune; drop the broken prefix.
	return strings.ToValidUTF8(string(t.buf), "")
}
