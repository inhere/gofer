package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/tracker"
	"github.com/inhere/gofer/internal/updatecheck"
	"github.com/inhere/gofer/internal/util"
	"github.com/inhere/gofer/internal/workerupgrade"
)

// One-click update (gofer-29gi, docs/design/2026-10-13-update-apply-design.md): the
// server downloads the latest release, hands its own binary to the managed-service
// upgrade, and after the restart the new server upgrades the chosen workers.

// Apply phases. downloading, waiting_idle and restarting belong to the old server; the
// new server moves restarting on to upgrading_workers and done (or failed). cancelled
// is a wait a person stopped before the server was restarted.
const (
	applyDownloading = "downloading"
	// applyWaitingIdle (gofer-dzv3) is the wait between "the binary is ready" and "the
	// server may restart": the machine has to go idle first, and new jobs are parked
	// (not refused) meanwhile.
	applyWaitingIdle = "waiting_idle"
	applyRestarting  = "restarting"
	applyWorkers     = "upgrading_workers"
	applyDone        = "done"
	applyFailed      = "failed"
	applyCancelled   = "cancelled"
)

const (
	// applyTimeout bounds the old server's download phase.
	applyTimeout = 30 * time.Minute
	// applyRestartTimeout bounds the handover to `serve upgrade` (it returns once the
	// upgrade was accepted, so this only has to cover the drain).
	applyRestartTimeout = 10 * time.Minute
	// ApplyDrainTimeout is the drain window the one-click update hands `serve upgrade`
	// (`--drain-timeout`). It is deliberately shorter than the command's own default: the
	// update waited for the machine to be idle a moment earlier, so a longer drain would
	// only delay a restart that cannot become safer. Exported because the serve-side
	// command builder passes it, and the two must stay the same number.
	ApplyDrainTimeout = 2 * time.Minute
	// applyWorkerWait is how long the new server waits for a worker to reconnect.
	applyWorkerWait = 3 * time.Minute
	applyStateFile  = "apply.json"
	// applyUpgradeLogFile is the output of the `serve upgrade` the update runs; it sits
	// in the apply dir, and a failure is explained out of it (see applyFailureMessage).
	applyUpgradeLogFile = "serve-upgrade.log"
	// applyWaitingListLimit caps the jobs the progress reports while it waits for idle.
	applyWaitingListLimit = 20
)

// Wait-for-idle bounds and pacing (gofer-dzv3). The console offers 30 minutes, 1, 2 and
// 4 hours; the default is two. The two below are vars because a test lowers them to
// exercise the timeout without sitting through the production window.
const (
	applyWaitIdleDefaultSec = 7200
	applyWaitIdleMaxSec     = 86400
)

var (
	// applyWaitIdleMinSec is the shortest wait a caller may ask for.
	applyWaitIdleMinSec = 60
	// applyWaitIdlePoll is how often the wait re-reads the restart census.
	applyWaitIdlePoll = 2 * time.Second
)

// applyWaitingIdlePublished is a test seam immediately after the waiting phase is
// persisted. The pause must already be active at this point: an arriving job must
// never observe the public phase before it observes the pause.
var applyWaitingIdlePublished = func() {}

// ManagedUpgradeFunc replaces the running managed server with binary (gofer serve
// upgrade). It returns once the upgrade was accepted; the process exits soon after.
type ManagedUpgradeFunc func(ctx context.Context, binary string) error

// ApplyWorker is one worker of an update apply.
type ApplyWorker struct {
	ID   string `json:"id"`
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
	// State: pending, accepted (handed over, see the worker's upgrade record),
	// succeeded / failed (the record's outcome), skipped.
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	UpgradeID string `json:"upgrade_id,omitempty"`
}

// applyWorkerFile is ApplyWorker as persisted. Binary is the downloaded binary for a
// worker of another platform; "" = the server's own (new) executable.
type applyWorkerFile struct {
	ApplyWorker
	Binary string `json:"binary,omitempty"`
}

// ApplyWaitingJob is one job the update is still waiting for before it restarts, as
// the progress reports it (gofer-dzv3): the id to click, a title to recognise it by,
// and what it is doing.
type ApplyWaitingJob struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status"`
	Runner    string `json:"runner,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
}

// ApplyState is the progress of the latest update apply.
type ApplyState struct {
	ID        string        `json:"id"`
	From      string        `json:"from"`
	Version   string        `json:"version"`
	Phase     string        `json:"phase"`
	Error     string        `json:"error,omitempty"`
	Workers   []ApplyWorker `json:"workers"`
	StartedAt int64         `json:"started_at"`
	UpdatedAt int64         `json:"updated_at"`
	// gofer-dzv3: while the apply waits for idle it also reports what it waits for —
	// the jobs that would block a restart (Waiting), how many jobs it has parked
	// (Paused) and when it gives up (WaitDeadline).
	WaitDeadline   int64             `json:"wait_deadline,omitempty"`
	WaitTimeoutSec int               `json:"wait_timeout_sec,omitempty"`
	Waiting        []ApplyWaitingJob `json:"waiting,omitempty"`
	Paused         int               `json:"paused,omitempty"`
}

// SetUpdateApply enables the one-click update: dir keeps the downloads and the apply
// state; managed is nil for a plain `gofer serve`, which cannot replace itself.
func (s *Server) SetUpdateApply(dir string, managed ManagedUpgradeFunc) {
	s.applyMu.Lock()
	s.applyDir, s.managedUpgrade = dir, managed
	s.applyMu.Unlock()
}

// updateResponse is GET /v1/update and POST /v1/update/check.
type updateResponse struct {
	updatecheck.Status
	// Managed reports whether this server can update itself (a registered service).
	Managed bool        `json:"managed"`
	Apply   *ApplyState `json:"apply,omitempty"`
}

func (s *Server) updateView(st updatecheck.Status) updateResponse {
	s.applyMu.Lock()
	managed := s.managedUpgrade != nil
	s.applyMu.Unlock()
	return updateResponse{Status: st, Managed: managed, Apply: s.applyView()}
}

// applyView is the persisted apply state with each accepted worker's outcome taken
// from its upgrade record, plus — while the apply waits for idle — what it is waiting
// for (gofer-dzv3).
func (s *Server) applyView() *ApplyState {
	st, ok := s.loadApply()
	if !ok {
		return nil
	}
	view := st.public()
	for i, w := range view.Workers {
		if w.State != "accepted" || w.UpgradeID == "" {
			continue
		}
		if rec := s.latestUpgrade(w.ID); rec != nil && rec.UpgradeID == w.UpgradeID {
			switch rec.State {
			case workerupgrade.StateSucceeded, workerupgrade.StateFailed:
				view.Workers[i].State, view.Workers[i].Error = rec.State, rec.Error
			}
		}
	}
	if view.Phase == applyWaitingIdle {
		s.fillWaitingView(&view)
	}
	return &view
}

// fillWaitingView names the jobs an apply is waiting for: the census the restart reads
// (so the list and the decision can never disagree) plus the number of jobs it parked.
// Both are read live: the persisted state only carries the deadline.
func (s *Server) fillWaitingView(view *ApplyState) {
	view.Waiting, view.Paused = s.waitingSnapshot()
}

// waitingSnapshot is the live view fillWaitingView and the server restart both report:
// what a restart would still have to wait for, and how many submits it parked meanwhile.
func (s *Server) waitingSnapshot() ([]ApplyWaitingJob, int) {
	if s.jobs == nil {
		return nil, 0
	}
	paused := 0
	if n, err := s.jobs.CountUpdatePaused(); err == nil {
		paused = n
	}
	blockers, err := s.jobs.ListUpgradeRestartBlockers(nil, applyWaitingListLimit)
	if err != nil {
		return nil, paused
	}
	waiting := make([]ApplyWaitingJob, 0, len(blockers))
	for _, b := range blockers {
		waiting = append(waiting, ApplyWaitingJob{
			ID: b.ID, Title: b.Title, Status: b.Status, Runner: b.Runner, StartedAt: b.StartedAt,
		})
	}
	return waiting, paused
}

type applyFile struct {
	ApplyState
	Workers []applyWorkerFile `json:"workers"`
	// WaitStartedAt is when this apply entered waiting_idle (gofer-dzv3). It is the
	// durable half of the wait: a server that starts up, sees a waiting_idle apply and
	// no waiter of its own knows the update that parked jobs is gone — this server
	// releases them and fails that apply (see ResumeUpdateApply).
	WaitStartedAt int64 `json:"wait_started_at,omitempty"`
}

func (f applyFile) public() ApplyState {
	st := f.ApplyState
	st.Workers = make([]ApplyWorker, 0, len(f.Workers))
	for _, w := range f.Workers {
		st.Workers = append(st.Workers, w.ApplyWorker)
	}
	return st
}

func (s *Server) applyStatePath() string {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.applyDir == "" {
		return ""
	}
	return filepath.Join(s.applyDir, applyStateFile)
}

func (s *Server) loadApply() (applyFile, bool) {
	var f applyFile
	p := s.applyStatePath()
	if p == "" {
		return f, false
	}
	data, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(data, &f) != nil || f.ID == "" {
		return f, false
	}
	return f, true
}

func (s *Server) saveApply(f *applyFile) {
	p := s.applyStatePath()
	if p == "" {
		return
	}
	f.UpdatedAt = time.Now().Unix()
	data, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(p), 0o755)
	}
	if err == nil {
		err = tracker.AtomicWriteFile(p, data)
	}
	if err != nil {
		slog.Error("update.apply_state_write_failed", "error", err)
	}
}

// updateApplyRequest is the body of POST /v1/update/apply.
type updateApplyRequest struct {
	// Workers to upgrade after the server restarted (empty = only the server).
	Workers []string `json:"workers"`
	// WaitIdleSec is how long the update may wait for the machine to go idle before it
	// gives up (gofer-dzv3). 0 = applyWaitIdleDefaultSec, clamped to
	// [applyWaitIdleMinSec, applyWaitIdleMaxSec].
	WaitIdleSec int `json:"wait_idle_sec"`
}

// waitIdleSec resolves the requested wait into the one that is applied.
func waitIdleSec(requested int) int {
	switch {
	case requested <= 0:
		return applyWaitIdleDefaultSec
	case requested < applyWaitIdleMinSec:
		return applyWaitIdleMinSec
	case requested > applyWaitIdleMaxSec:
		return applyWaitIdleMaxSec
	}
	return requested
}

// handleUpdateApply is POST /v1/update/apply: start a one-click update. It answers
// 202 with the apply state; progress is GET /v1/update's apply.
//
// Its user side is the route's InHandler(update.apply[, worker.upgrade]): the handler is
// what reads the body and asks (mayApplyUpdate / mayUpgradeWorkersAlong, authz_admin_rules.go).
func (s *Server) handleUpdateApply(c *rux.Context) {
	if !adminUserOnly(c, "apply a server update") {
		return
	}
	if !s.mayApplyUpdate(c) {
		return
	}
	if s.updater == nil {
		writeError(c, http.StatusServiceUnavailable, "update check unavailable", "this server has no release checker")
		return
	}
	s.applyMu.Lock()
	managed, dir, busy := s.managedUpgrade, s.applyDir, s.applyBusy
	s.applyMu.Unlock()
	if managed == nil || dir == "" {
		writeError(c, http.StatusConflict, "not a managed server",
			"only a server registered with `gofer serve register` can update itself; replace the binary and restart it by hand")
		return
	}
	if busy {
		writeError(c, http.StatusConflict, "update in progress", "an update is already being applied")
		return
	}
	st := s.updater.Status()
	if st.Latest == "" || st.Error != "" {
		writeError(c, http.StatusConflict, "no release checked", "check for updates first")
		return
	}
	if !st.Newer {
		writeError(c, http.StatusConflict, "already up to date", "this server runs "+st.Current+", the latest release is "+st.Latest)
		return
	}
	var req updateApplyRequest
	if c.Req.Body != nil && c.Req.ContentLength != 0 {
		if err := json.NewDecoder(c.Req.Body).Decode(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	// The route's second declared permission, decided by the body: a request that names
	// workers upgrades them with this server's own binary, which needs worker.upgrade too.
	// Asked before anything is downloaded, written or restarted, so a refused request
	// changes nothing.
	if !s.mayUpgradeWorkersAlong(c, len(req.Workers)) {
		return
	}
	known := s.workerConfigs()
	f := applyFile{ApplyState: ApplyState{
		ID: newUpgradeID(), From: st.Current, Version: st.Latest, Phase: applyDownloading, StartedAt: time.Now().Unix(),
		WaitTimeoutSec: waitIdleSec(req.WaitIdleSec),
	}}
	seen := map[string]bool{}
	for _, id := range req.Workers {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if _, ok := known[id]; !ok {
			writeError(c, http.StatusBadRequest, "unknown worker", id)
			return
		}
		seen[id] = true
		f.Workers = append(f.Workers, applyWorkerFile{ApplyWorker: ApplyWorker{ID: id, State: "pending"}})
	}

	s.applyMu.Lock()
	if s.applyBusy {
		s.applyMu.Unlock()
		writeError(c, http.StatusConflict, "update in progress", "an update is already being applied")
		return
	}
	s.applyBusy = true
	s.applyMu.Unlock()
	s.saveApply(&f)
	slog.Info("update.apply_started", "event", "update.apply_started", "id", f.ID, "from", f.From, "version", f.Version, "workers", len(f.Workers))
	view := f.public() // before the goroutine takes f (the workers slice is shared)
	go s.runUpdateApply(f, managed, dir)
	c.JSON(http.StatusAccepted, view)
}

// runUpdateApply is the old server's part: download every needed binary, check its
// version, wait for the machine to go idle (parking what arrives meanwhile), then hand
// the server binary to the managed upgrade.
func (s *Server) runUpdateApply(f applyFile, managed ManagedUpgradeFunc, dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()
	fail := func(err error) { s.failUpdateApply(&f, err) }

	verDir := filepath.Join(dir, f.Version)
	own, err := s.downloadRelease(ctx, runtime.GOOS, runtime.GOARCH, verDir, f.Version)
	if err != nil {
		fail(fmt.Errorf("download the server binary: %w", err))
		return
	}
	// Workers of another platform get their own release binary, downloaded now so a
	// missing asset is known before the server restarts.
	byPlatform := map[string]string{runtime.GOOS + "/" + runtime.GOARCH: ""}
	for i := range f.Workers {
		w := &f.Workers[i]
		var ws WorkerStatus
		if s.workers != nil {
			ws, _ = s.workers.WorkerStatus(w.ID)
		}
		if !ws.Connected || ws.OS == "" || ws.Arch == "" {
			w.State, w.Error = "skipped", "worker offline: its platform is unknown"
			continue
		}
		w.OS, w.Arch = ws.OS, ws.Arch
		key := ws.OS + "/" + ws.Arch
		bin, ok := byPlatform[key]
		if !ok {
			bin, err = s.downloadRelease(ctx, ws.OS, ws.Arch, verDir, "")
			if err != nil {
				w.State, w.Error = "skipped", "download its binary: "+err.Error()
				continue
			}
			byPlatform[key] = bin
		}
		w.Binary = bin
	}

	// gofer-dzv3: the machine now has to go idle, and new jobs are parked rather than
	// refused while we wait for that. The wait ends when nothing is left that a restart
	// would kill, when the deadline passes, or when somebody cancels it.
	// Pause before publishing waiting_idle. The phase is public, so persisting it first
	// leaves a window in which a newly submitted job can start instead of being parked.
	if s.jobs != nil {
		s.jobs.PauseStartsForUpdate(f.ID)
	}
	now := time.Now()
	f.WaitStartedAt = now.Unix()
	f.WaitDeadline = now.Add(time.Duration(f.WaitTimeoutSec) * time.Second).Unix()
	f.Phase = applyWaitingIdle
	s.saveApply(&f)
	applyWaitingIdlePublished()
	slog.Info("update.apply_waiting_idle", "event", "update.apply_waiting_idle", "id", f.ID,
		"deadline", time.Unix(f.WaitDeadline, 0).Format(time.RFC3339), "wait_sec", f.WaitTimeoutSec)
	outcome := s.waitForIdle(f.WaitDeadline)
	if outcome != waitIdleReached {
		s.endWaitingApply(&f, outcome)
		return
	}

	f.Phase = applyRestarting
	s.saveApply(&f)
	slog.Info("update.apply_restarting", "event", "update.apply_restarting", "id", f.ID, "binary", own)
	// The wait is over: the pause stays ON across the handover, so a submit that arrives
	// in the gap between "idle" and "restarted" parks instead of being refused.
	rCtx, rCancel := context.WithTimeout(context.Background(), applyRestartTimeout)
	defer rCancel()
	if err := managed(rCtx, own); err != nil {
		fail(errors.New(s.applyFailureMessage(err)))
		return
	}
	// Accepted: this process is about to be replaced; the new server resumes the apply.
}

// waitIdleOutcome is why an update stopped waiting for the machine to go idle.
type waitIdleOutcome int

const (
	waitIdleReached waitIdleOutcome = iota
	waitIdleTimedOut
	waitIdleCancelled
)

// waitForIdle blocks until nothing is left that a server restart would kill, the
// wait's deadline passes, or an administrator cancels. It pauses new starts for the
// whole wait, so what arrives meanwhile is parked (and counted as such) instead of
// starting a job the restart would have to wait for. deadlineUnix is the wait's own
// deadline (`wait_deadline`), shared by the update and the server restart.
func (s *Server) waitForIdle(deadlineUnix int64) waitIdleOutcome {
	if s.jobs == nil {
		return waitIdleReached
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.applyMu.Lock()
	s.applyWaitCancel = cancel
	s.applyMu.Unlock()
	defer func() {
		s.applyMu.Lock()
		s.applyWaitCancel = nil
		s.applyMu.Unlock()
		cancel()
	}()

	deadline := time.Unix(deadlineUnix, 0)
	for {
		if n, err := s.jobs.CountUpgradeRestartBlockers(nil); err == nil && n == 0 {
			return waitIdleReached
		}
		if !time.Now().Before(deadline) {
			return waitIdleTimedOut
		}
		select {
		case <-ctx.Done():
			return waitIdleCancelled
		case <-time.After(applyWaitIdlePoll):
		}
	}
}

// endWaitingApply closes an update that stopped waiting: the parked jobs run again and
// the apply is recorded as failed (the deadline passed) or cancelled (somebody asked).
func (s *Server) endWaitingApply(f *applyFile, outcome waitIdleOutcome) {
	released := s.releaseParkedJobs()
	f.Phase, f.WaitDeadline = applyFailed, 0
	if outcome == waitIdleCancelled {
		f.Phase, f.Error = applyCancelled, "cancelled while waiting for the server to go idle"
	} else {
		f.Error = fmt.Sprintf("waited %s for the server to go idle and gave up: %s",
			time.Duration(f.WaitTimeoutSec)*time.Second, s.idleWaitReason())
	}
	s.saveApply(f)
	s.applyMu.Lock()
	s.applyBusy = false
	s.applyMu.Unlock()
	slog.Info("update.apply_wait_over", "event", "update.apply_wait_over", "id", f.ID,
		"phase", f.Phase, "released", released, "error", f.Error)
}

// idleWaitReason names what the wait was still looking at, so a timeout says what to
// look at rather than just "no idle".
func (s *Server) idleWaitReason() string {
	if s.jobs == nil {
		return "no idle moment"
	}
	blockers, err := s.jobs.ListUpgradeRestartBlockers(nil, 5)
	if err != nil || len(blockers) == 0 {
		return "jobs kept arriving"
	}
	parts := make([]string, 0, len(blockers))
	for _, b := range blockers {
		parts = append(parts, fmt.Sprintf("%s (%s)", b.ID, b.Status))
	}
	return fmt.Sprintf("%d job(s) still running: %s", len(blockers), strings.Join(parts, ", "))
}

// releaseParkedJobs lets every job an update parked start again (gofer-dzv3). It is
// called on every exit of an apply — the new server at startup, a failure, a timeout, a
// cancel — and is a cheap no-op when this process parked nothing.
func (s *Server) releaseParkedJobs() int {
	if s.jobs == nil {
		return 0
	}
	n, err := s.jobs.ResumeStartsForUpdate()
	if err != nil {
		slog.Error("update.release_paused_failed", "event", "update.release_paused_failed", "error", err)
	}
	return n
}

// failUpdateApply ends an apply that failed before (or instead of) a handover: the jobs
// it parked run again, the phase records why, and a pending worker list is settled.
func (s *Server) failUpdateApply(f *applyFile, err error) {
	s.releaseParkedJobs()
	f.Phase, f.Error = applyFailed, err.Error()
	f.WaitDeadline = 0
	for i := range f.Workers {
		if f.Workers[i].State == "pending" {
			f.Workers[i].State = "skipped"
		}
	}
	s.saveApply(f)
	s.applyMu.Lock()
	s.applyBusy = false
	s.applyMu.Unlock()
	slog.Error("update.apply_failed", "event", "update.apply_failed", "id", f.ID, "error", err)
}

// handleUpdateApplyCancel is DELETE /v1/update/apply: stop an update that is waiting for
// the server to go idle. The parked jobs run again and the apply is recorded as
// cancelled; a cancel that arrives after the handover is refused (the restart is
// already under way).
//
// Its user side is the route's `Require(update.apply)` (authz_routes.go): cancelling upgrades
// nobody, so it does not ask worker.upgrade.
func (s *Server) handleUpdateApplyCancel(c *rux.Context) {
	if !adminUserOnly(c, "cancel a server update") {
		return
	}
	s.applyMu.Lock()
	cancel := s.applyWaitCancel
	s.applyMu.Unlock()
	if cancel == nil {
		writeError(c, http.StatusConflict, "no update to cancel",
			"no update is waiting for the server to go idle right now")
		return
	}
	cancel()
	// The waiter owns the state: it releases the parked jobs and writes the cancelled
	// phase a moment later. The caller polls GET /v1/update for that.
	c.JSON(http.StatusAccepted, s.applyView())
}

// applyFailureMessage turns a failed handover into something a person can act on: the
// last line `serve upgrade` logged (its own error, e.g. a drain that ran out of time),
// the jobs the restart was still waiting for, and where its output is.
func (s *Server) applyFailureMessage(err error) string {
	parts := make([]string, 0, 3)
	parts = append(parts, "upgrade the server: "+err.Error())
	if line := lastLogLine(s.upgradeLogPath()); line != "" {
		parts[0] = "upgrade the server: " + line
	}
	if s.jobs != nil {
		if blockers, berr := s.jobs.ListUpgradeRestartBlockers(nil, 5); berr == nil && len(blockers) > 0 {
			names := make([]string, 0, len(blockers))
			for _, b := range blockers {
				names = append(names, fmt.Sprintf("%s (%s)", b.ID, b.Status))
			}
			parts = append(parts, fmt.Sprintf("%d job(s) still running: %s", len(blockers), strings.Join(names, ", ")))
		}
	}
	if p := s.upgradeLogPath(); p != "" {
		parts = append(parts, "log "+p)
	}
	return strings.Join(parts, "; ")
}

// upgradeLogPath is the file the managed upgrade writes its output to (empty when this
// server has no apply dir).
func (s *Server) upgradeLogPath() string {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.applyDir == "" {
		return ""
	}
	return filepath.Join(s.applyDir, applyUpgradeLogFile)
}

// upgradeLogTailBytes is how much of the upgrade log is read to find its last word.
const upgradeLogTailBytes = 8192

// lastLogLine returns the last non-empty line of the upgrade log that says something
// new — the progress lines the command prints while it drains are skipped, an "ERROR"
// prefix gcli adds to a returned error is stripped. "" when there is nothing to read
// (the log is missing, empty, or holds nothing but progress).
func lastLogLine(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	off := int64(0)
	if size > upgradeLogTailBytes {
		off = size - upgradeLogTailBytes
	}
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return ""
	}
	lines := strings.Split(string(buf), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "upgrade_id=") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "ERROR:"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "ERROR"))
		return line
	}
	return ""
}

// downloadRelease fetches a platform's binary of the checked release into dir. For
// the server's own platform (wantVersion set) it also checks what the binary reports.
func (s *Server) downloadRelease(ctx context.Context, goos, goarch, dir, wantVersion string) (string, error) {
	asset, err := s.updater.AssetFor(goos, goarch)
	if err != nil {
		return "", err
	}
	path, err := s.updater.Download(ctx, asset, dir)
	if err != nil {
		return "", err
	}
	if wantVersion == "" {
		return path, nil
	}
	got, err := binaryVersion(ctx, path)
	if err != nil {
		return "", err
	}
	if !sameRelease(got, wantVersion) {
		return "", fmt.Errorf("the downloaded binary reports %q, expected %s", got, wantVersion)
	}
	return path, nil
}

// binaryVersion runs `<path> --version` (a var for tests).
var binaryVersion = func(ctx context.Context, path string) (string, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, path, "--version")
	procattr.Background(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", filepath.Base(path), err)
	}
	return util.ParseVersionOutput(string(out))
}

// sameRelease reports whether a reported version ("0.154.0 (abc1234)") is release v.
func sameRelease(reported, v string) bool {
	reported = strings.TrimPrefix(strings.TrimSpace(reported), "v")
	head, _, _ := strings.Cut(reported, " ")
	return head == strings.TrimPrefix(v, "v")
}

// ResumeUpdateApply is the new server's part, run once at startup. It first releases
// every job a previous process parked (gofer-dzv3): the pause belongs to the process
// that set it, so whatever this server finds parked was parked by an update that is
// gone — it must never stay parked forever. Then: when an apply was restarting this
// server, confirm the new version and upgrade the chosen workers; when it was still
// waiting for idle, that wait died with the old process and the apply is failed.
func (s *Server) ResumeUpdateApply(ctx context.Context) {
	if n := s.releaseParkedJobs(); n > 0 {
		slog.Info("update.paused_jobs_released", "event", "update.paused_jobs_released",
			"component", "server", "count", n, "reason", "serve restarted")
	}
	f, ok := s.loadApply()
	if !ok {
		return
	}
	if f.Phase == applyWaitingIdle {
		// Nothing can resume this wait: its deadline, its census loop and the pause that
		// belonged to the process that owned it.
		f.Phase, f.WaitDeadline = applyFailed, 0
		f.Error = "the server restarted while the update was waiting for it to go idle; run the update again"
		s.saveApply(&f)
		slog.Error("update.apply_failed", "event", "update.apply_failed", "id", f.ID, "error", f.Error)
		return
	}
	if f.Phase != applyRestarting {
		return
	}
	if !sameRelease(s.build.Version, f.Version) {
		f.Phase = applyFailed
		f.Error = fmt.Sprintf("the server still runs %s after the restart (the upgrade did not take effect; see gofer serve upgrade status)", s.build.DisplayVersion())
		s.saveApply(&f)
		slog.Error("update.apply_failed", "event", "update.apply_failed", "id", f.ID, "error", f.Error)
		return
	}
	// The restart worked: this server owns the apply now, and the jobs the old one
	// parked have been released above.
	s.applyMu.Lock()
	s.applyBusy = true
	s.applyMu.Unlock()
	defer func() {
		s.applyMu.Lock()
		s.applyBusy = false
		s.applyMu.Unlock()
	}()
	f.Phase = applyWorkers
	s.saveApply(&f)
	for i := range f.Workers {
		w := &f.Workers[i]
		if w.State != "pending" {
			continue
		}
		if !s.waitWorker(ctx, w.ID) {
			w.State, w.Error = "skipped", "worker did not reconnect"
			s.saveApply(&f)
			continue
		}
		rec, err := s.startWorkerUpgrade(ctx, w.ID, workerUpgradeRequest{}, w.Binary)
		if err != nil {
			var uf *upgradeFailure
			if errors.As(err, &uf) {
				w.Error = uf.detail
			} else {
				w.Error = err.Error()
			}
			w.State = "failed"
		} else {
			w.State, w.UpgradeID = "accepted", rec.UpgradeID
		}
		s.saveApply(&f)
	}
	f.Phase = applyDone
	s.saveApply(&f)
	slog.Info("update.apply_done", "event", "update.apply_done", "id", f.ID, "version", f.Version)
}

// waitWorker waits for a worker to be connected (it reconnects after the restart).
func (s *Server) waitWorker(ctx context.Context, id string) bool {
	deadline := time.Now().Add(applyWorkerWait)
	for {
		if s.workers != nil && s.upgrader != nil && s.upgrades != nil {
			if ws, _ := s.workers.WorkerStatus(id); ws.Connected {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
}
