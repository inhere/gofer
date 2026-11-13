package job

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/util"
)

// Pre-run check of a schedule (design 2026-11-12 §2, plan R4/R5): a command the server
// runs before a due tick becomes a job. Exit 0 submits, exit 2 skips quietly, anything
// else (or a timeout) is a failed run. With skip_unchanged an exit 0 whose output is the
// same as the last submitted run's is skipped too.

// SchedulePrecheck is the declaration stored on a schedule.
type SchedulePrecheck struct {
	// Argv is the command, run WITHOUT a shell; argv[0] is resolved on the server
	// process's PATH.
	Argv []string `json:"argv"`
	// TimeoutSec bounds the check (0 = DefaultPrecheckTimeoutSec, at most MaxPrecheckTimeoutSec).
	TimeoutSec int `json:"timeout_sec,omitempty"`
	// SkipUnchanged skips a run whose check output fingerprint equals the last
	// submitted run's.
	SkipUnchanged bool `json:"skip_unchanged,omitempty"`
}

// Pre-run check limits and exit codes.
const (
	DefaultPrecheckTimeoutSec = 60
	MaxPrecheckTimeoutSec     = 300
	// precheckExitSkip is the exit status meaning "nothing to do this time".
	precheckExitSkip = 2
	// precheckOutputTail is how much of the check's stdout end becomes
	// GOFER_PRECHECK_OUTPUT (after masking).
	precheckOutputTail = 4096
	// precheckPoolSize bounds how many checks run at once across all schedules.
	precheckPoolSize = 4
	// EnvPrecheckOutput is the job environment variable carrying the check's output.
	EnvPrecheckOutput = "GOFER_PRECHECK_OUTPUT"
	// precheckVarOutput is the template variable carrying the same text (requests
	// that name a template only).
	precheckVarOutput = "precheck_output"
)

// Validate checks the declaration.
func (p SchedulePrecheck) Validate() error {
	if len(p.Argv) == 0 || strings.TrimSpace(p.Argv[0]) == "" {
		return fmt.Errorf("%w: precheck.argv must name a command", ErrInvalidRequest)
	}
	for _, a := range p.Argv {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("%w: precheck.argv must not contain NUL", ErrInvalidRequest)
		}
	}
	if p.TimeoutSec < 0 || p.TimeoutSec > MaxPrecheckTimeoutSec {
		return fmt.Errorf("%w: precheck.timeout_sec must be between 0 and %d", ErrInvalidRequest, MaxPrecheckTimeoutSec)
	}
	return nil
}

func (p SchedulePrecheck) timeout() time.Duration {
	if p.TimeoutSec <= 0 {
		return DefaultPrecheckTimeoutSec * time.Second
	}
	return time.Duration(p.TimeoutSec) * time.Second
}

// IsSet reports whether the declaration holds a command (an empty one means "no check").
func (p *SchedulePrecheck) IsSet() bool { return p != nil && len(p.Argv) > 0 }

// SameCommand reports whether two declarations run the same argv: the fingerprint of the
// old one is only meaningful for the same command.
func (p *SchedulePrecheck) SameCommand(o *SchedulePrecheck) bool {
	if !p.IsSet() || !o.IsSet() || len(p.Argv) != len(o.Argv) {
		return false
	}
	for i := range p.Argv {
		if p.Argv[i] != o.Argv[i] {
			return false
		}
	}
	return true
}

// EncodeSchedulePrecheck validates and serialises the declaration; nil or an empty argv
// encodes to "" (no check).
func EncodeSchedulePrecheck(p *SchedulePrecheck) (string, error) {
	if !p.IsSet() {
		return "", nil
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DecodeSchedulePrecheck reads a stored declaration; "" is nil, nil.
func DecodeSchedulePrecheck(raw string) (*SchedulePrecheck, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var p SchedulePrecheck
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("decode precheck: %w", err)
	}
	if !p.IsSet() {
		return nil, nil
	}
	return &p, nil
}

// precheckResult is what one check run produced.
type precheckResult struct {
	ExitCode    int
	Duration    time.Duration
	TimedOut    bool
	Err         error  // could not start / unexpected wait error
	Tail        string // last precheckOutputTail bytes of stdout, unmasked
	Truncated   bool   // stdout was longer than Tail
	Fingerprint string // sha256 of the whole stdout without trailing whitespace
}

// tailHashWriter keeps the last `limit` bytes written and the sha256 of everything
// written except trailing whitespace (the fingerprint rule of design §2): whitespace is
// held back until a later non-whitespace byte proves it is not trailing.
type tailHashWriter struct {
	limit     int
	tail      []byte
	total     int64
	h         hash.Hash
	pendingWS []byte
}

func newTailHashWriter(limit int) *tailHashWriter {
	return &tailHashWriter{limit: limit, h: sha256.New()}
}

func (w *tailHashWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.total += int64(n)
	w.tail = append(w.tail, p...)
	if len(w.tail) > w.limit {
		w.tail = append([]byte(nil), w.tail[len(w.tail)-w.limit:]...)
	}
	// Hash everything up to the last non-whitespace byte of p, carrying trailing
	// whitespace over to the next write.
	end := len(bytes.TrimRight(p, " \t\r\n\v\f"))
	if end > 0 {
		w.h.Write(w.pendingWS)
		w.pendingWS = w.pendingWS[:0]
		w.h.Write(p[:end])
	}
	w.pendingWS = append(w.pendingWS, p[end:]...)
	return n, nil
}

func (w *tailHashWriter) fingerprint() string { return hex.EncodeToString(w.h.Sum(nil)) }

// precheckEnvDeny is the set of inherited variables a check must not see: the job
// pipeline's credential deny list, every GOFER_* variable, and every variable the
// configuration names through a `*_env` setting (server token, worker tokens, webhook
// and secret sources).
func precheckEnvDeny(cfg *config.Config) []string {
	deny := effectiveJobEnvDeny(cfg)
	deny = append(deny, cfg.EnvRefNames()...)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if len(key) >= 6 && strings.EqualFold(key[:6], "GOFER_") {
			deny = append(deny, key)
		}
	}
	return deny
}

// runPrecheck executes the declared command in dir and returns what it produced. extra
// (the schedule's parameters) is layered over the filtered inherited environment.
func runPrecheck(ctx context.Context, cfg *config.Config, dir string, p SchedulePrecheck, extra map[string]string) precheckResult {
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Argv[0], p.Argv[1:]...)
	cmd.Dir = dir
	cmd.Env = util.EnvironWithout(precheckEnvDeny(cfg), nil, extra)
	out := newTailHashWriter(precheckOutputTail)
	cmd.Stdout = out
	cmd.Stderr = nil // not kept: a check reports through its exit status and stdout
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	err := cmd.Run()
	res := precheckResult{
		Duration:    time.Since(start),
		Tail:        string(out.tail),
		Truncated:   out.total > int64(len(out.tail)),
		Fingerprint: out.fingerprint(),
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.ExitCode = -1
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		res.ExitCode = -1
		res.Err = err
	}
	return res
}

// syncSet is a mutex-guarded string set.
type syncSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func newSyncSet() *syncSet { return &syncSet{m: map[string]struct{}{}} }

// add inserts k and reports whether it was absent.
func (s *syncSet) add(k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[k]; ok {
		return false
	}
	s.m[k] = struct{}{}
	return true
}

func (s *syncSet) remove(k string) {
	s.mu.Lock()
	delete(s.m, k)
	s.mu.Unlock()
}

// precheckPool bounds concurrent checks and keeps one check per schedule in flight.
type precheckPool struct {
	slots    chan struct{}
	inflight *syncSet
}

func newPrecheckPool() *precheckPool {
	return &precheckPool{slots: make(chan struct{}, precheckPoolSize), inflight: newSyncSet()}
}

// startCheck is the check half of FireTimer: the overlap rule has already said "go". It
// hands the run to the pool and returns at once, so a slow check never holds up the
// sweep. The ok result is false when the schedule's previous check is still running.
func (d *ScheduleDispatcher) startCheck(rec jobstore.ScheduleRecord, pc SchedulePrecheck, req JobRequest) bool {
	if !d.pool.inflight.add(rec.ID) {
		d.recordSkipped(rec, ScheduleSkipCheckRunning, "")
		return false
	}
	started := d.s.GoBackground(func() {
		defer d.pool.inflight.remove(rec.ID)
		d.runCheckedRun(rec, pc, req)
	})
	if !started { // shutting down
		d.pool.inflight.remove(rec.ID)
	}
	return started
}

// runCheckedRun runs the check, then submits, skips or fails the run by its outcome.
func (d *ScheduleDispatcher) runCheckedRun(rec jobstore.ScheduleRecord, pc SchedulePrecheck, req JobRequest) {
	select {
	case d.pool.slots <- struct{}{}:
		defer func() { <-d.pool.slots }()
	case <-d.s.bg.closingCh():
		return
	}
	cfg := d.s.config()
	proj, ok := cfg.Projects[req.ProjectKey]
	if !ok {
		d.checkFailed(rec, map[string]any{"error": "unknown project"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-d.s.bg.closingCh():
			cancel()
		case <-ctx.Done():
		}
	}()
	res := runPrecheck(ctx, cfg, cfg.ExecPath(proj), pc, req.Env)
	if ctx.Err() != nil && !res.TimedOut { // server shutdown: no verdict
		return
	}
	detail := map[string]any{
		"exit_code": res.ExitCode, "duration_ms": res.Duration.Milliseconds(),
		"timed_out": res.TimedOut, "truncated": res.Truncated,
	}
	switch {
	case res.Err != nil || res.TimedOut || (res.ExitCode != 0 && res.ExitCode != precheckExitSkip):
		d.checkFailed(rec, detail)
		return
	case res.ExitCode == precheckExitSkip:
		d.recordSkipped(rec, ScheduleSkipPrecheck, "")
		return
	}
	// The stored fingerprint is read now, not at tick time: another run may have
	// submitted (and updated it) while this check was running.
	latest, found, err := d.s.meta.GetSchedule(rec.ID)
	if err != nil || !found {
		return
	}
	if pc.SkipUnchanged && latest.PrecheckFingerprint != "" && latest.PrecheckFingerprint == res.Fingerprint {
		d.recordSkipped(rec, ScheduleSkipUnchanged, "")
		return
	}
	d.submitChecked(rec, req, res)
}

// checkFailed is a failed check: schedule.run_failed{reason: precheck} with the exit
// facts (never the output), counted like any failed run.
func (d *ScheduleDispatcher) checkFailed(rec jobstore.ScheduleRecord, detail map[string]any) {
	d.s.recordScheduleRunFailed(rec, "precheck", detail)
	if scheduleCounts(rec, jobstore.ScheduleTriggerTimer) {
		d.s.scheduleRunFailed(rec.ID, "")
	}
}

// submitChecked puts the (masked) check output into the request and dispatches it; the
// fingerprint is stored only once the run was actually submitted.
func (d *ScheduleDispatcher) submitChecked(rec jobstore.ScheduleRecord, req JobRequest, res precheckResult) {
	cfg := d.s.config()
	_, secretValues, _ := resolveSecrets(cfg, req.Secrets)
	output := maskSecrets(strings.TrimRight(res.Tail, " \t\r\n"), secretValues)
	req.Env = util.EnvWith(req.Env, map[string]string{EnvPrecheckOutput: output})
	if req.Template != "" {
		req.TemplateVars = util.EnvWith(req.TemplateVars, map[string]string{precheckVarOutput: output})
	}
	permit, err := d.s.BeginUpgradeWork()
	if err != nil {
		slog.Info("schedule run dropped: server is draining", "schedule_id", rec.ID, "err", err)
		return
	}
	defer permit.Release()
	run, err := d.Dispatch(permit, rec, jobstore.ScheduleTriggerTimer, req)
	if err != nil || run.Skipped {
		return
	}
	if err := d.s.meta.SetScheduleLastJob(rec.ID, run.Result.ID); err != nil {
		slog.Warn("schedule last job", "schedule_id", rec.ID, "err", err)
	}
	if err := d.s.meta.SetSchedulePrecheckFingerprint(rec.ID, res.Fingerprint); err != nil {
		slog.Warn("schedule precheck fingerprint", "schedule_id", rec.ID, "err", err)
	}
}
