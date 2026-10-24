package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/tracker"
)

// Server restart (gofer-4uk5): restart the managed serve without downloading anything.
//
// It is the one-click update's sibling and reuses its handshake end to end — the same
// managed handover (`serve upgrade`), the same park-new-jobs-then-wait-for-idle step and
// the same cancel switch — and differs only in where the candidate binary comes from:
// the update downloads a release, a restart stages a copy of the RUNNING executable.
// Copying it (rather than handing `os.Executable()` over) keeps the swap honest: the
// managed upgrade replaces the installed binary with the candidate file, and replacing
// a file with itself is exactly the case the upgrade helper is not written for.
const (
	restartStateFile = "restart.json"
	// restartStageDir holds the staged copy of the running binary, per restart id.
	restartStageDir = "restart"
)

// ServerRestartState is the progress of the latest server restart. The phases are the
// update's waiting_idle / restarting plus the two ways a wait can end without a restart
// (failed, cancelled); a restart that went through is not observable from the process
// that asked for it, so the new process reports it as done (see ResumeServerRestart).
type ServerRestartState struct {
	ID    string `json:"id"`
	Phase string `json:"phase"`
	Error string `json:"error,omitempty"`
	// Version is the version the restarted server comes back as (this build's).
	Version   string `json:"version,omitempty"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
	// The wait-for-idle block, identical to the update's (gofer-dzv3): when it gives up,
	// which jobs block the restart, and how many submits it parked meanwhile.
	WaitDeadline   int64             `json:"wait_deadline,omitempty"`
	WaitTimeoutSec int               `json:"wait_timeout_sec,omitempty"`
	Waiting        []ApplyWaitingJob `json:"waiting,omitempty"`
	Paused         int               `json:"paused,omitempty"`
}

// restartFile is ServerRestartState as persisted.
type restartFile struct {
	ServerRestartState
	// WaitStartedAt is the durable half of the wait, the same contract as the update's:
	// a process that finds a waiting_idle restart it did not start knows the waiter is
	// gone and fails that state instead of leaving the console waiting forever.
	WaitStartedAt int64 `json:"wait_started_at,omitempty"`
}

// serverRestartView is GET /v1/server/restart: whether this server can restart itself
// at all (a registered native service can, a foreground `gofer serve` cannot) and the
// latest restart's progress.
type serverRestartView struct {
	Managed bool                `json:"managed"`
	State   *ServerRestartState `json:"state,omitempty"`
}

// handleServerRestartStatus is GET /v1/server/restart.
func (s *Server) handleServerRestartStatus(c *rux.Context) {
	s.applyMu.Lock()
	managed := s.managedUpgrade != nil
	s.applyMu.Unlock()
	c.JSON(http.StatusOK, serverRestartView{Managed: managed, State: s.restartStateView()})
}

// handleServerRestart is POST /v1/server/restart: restart the managed server. It
// answers 202 with the state; progress is GET /v1/server/restart. The body is optional
// and carries only how long the restart may wait for the machine to go idle — the same
// `wait_idle_sec` the update takes, with the same default and clamps.
func (s *Server) handleServerRestart(c *rux.Context) {
	if !adminUserOnly(c, "restart the server") {
		return
	}
	s.applyMu.Lock()
	managed, dir, busy := s.managedUpgrade, s.applyDir, s.applyBusy
	s.applyMu.Unlock()
	if managed == nil || dir == "" {
		writeError(c, http.StatusConflict, "not a managed server",
			"only a server registered with `gofer serve register` can restart itself; restart it by hand")
		return
	}
	if busy {
		writeError(c, http.StatusConflict, "an update or restart is in progress",
			"an update or a restart is already being applied")
		return
	}
	var req updateApplyRequest
	if c.Req.Body != nil && c.Req.ContentLength != 0 {
		if err := json.NewDecoder(c.Req.Body).Decode(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	f := restartFile{ServerRestartState: ServerRestartState{
		ID: newUpgradeID(), Version: s.build.Version, Phase: applyWaitingIdle,
		StartedAt: time.Now().Unix(), WaitTimeoutSec: waitIdleSec(req.WaitIdleSec),
	}}

	s.applyMu.Lock()
	if s.applyBusy {
		s.applyMu.Unlock()
		writeError(c, http.StatusConflict, "an update or restart is in progress",
			"an update or a restart is already being applied")
		return
	}
	s.applyBusy = true
	s.applyMu.Unlock()
	// Pause before publishing waiting_idle: the phase is public, so persisting it first
	// would leave a window in which a new job starts instead of being parked.
	if s.jobs != nil {
		s.jobs.PauseStartsForUpdate(f.ID)
	}
	now := time.Now()
	f.WaitStartedAt = now.Unix()
	f.WaitDeadline = now.Add(time.Duration(f.WaitTimeoutSec) * time.Second).Unix()
	waitCtx := s.armWaitIdle()
	s.saveRestart(&f)
	slog.Info("server.restart_waiting_idle", "event", "server.restart_waiting_idle", "id", f.ID,
		"deadline", time.Unix(f.WaitDeadline, 0).Format(time.RFC3339), "wait_sec", f.WaitTimeoutSec)
	view := f.ServerRestartState
	go s.runServerRestart(waitCtx, f, managed, dir)
	c.JSON(http.StatusAccepted, view)
}

// runServerRestart waits for the machine to go idle, stages a copy of this executable
// and hands it to the managed upgrade. Nothing is validated against a release: the
// candidate is the binary that is already running.
func (s *Server) runServerRestart(waitCtx context.Context, f restartFile, managed ManagedUpgradeFunc, dir string) {
	if outcome := s.waitForIdle(waitCtx, f.WaitDeadline); outcome != waitIdleReached {
		s.endWaitingRestart(&f, outcome)
		return
	}
	f.Phase = applyRestarting
	s.saveRestart(&f)
	own, err := stageRestartBinary(dir, f.ID)
	if err != nil {
		s.failRestart(&f, fmt.Errorf("stage the running binary: %w", err))
		return
	}
	slog.Info("server.restart_restarting", "event", "server.restart_restarting", "id", f.ID, "binary", own)
	// The pause stays ON across the handover, so a submit that arrives in the gap
	// between "idle" and "restarted" parks instead of being refused.
	rCtx, rCancel := context.WithTimeout(context.Background(), applyRestartTimeout)
	defer rCancel()
	if err := managed(rCtx, own); err != nil {
		s.failRestart(&f, errors.New(s.applyFailureMessage(err)))
		return
	}
	// Accepted: this process is about to be replaced; the new server records the outcome.
}

// stageRestartBinary copies the running executable under dir, the way the update hands
// over a downloaded file rather than its own.
func stageRestartBinary(dir, id string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return "", err
	}
	target := filepath.Join(dir, restartStageDir, id, filepath.Base(exe))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	src, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	return target, nil
}

// endWaitingRestart closes a restart that stopped waiting for idle: the parked jobs run
// again and the restart is recorded as failed (deadline) or cancelled (somebody asked).
func (s *Server) endWaitingRestart(f *restartFile, outcome waitIdleOutcome) {
	released := s.releaseParkedJobs()
	f.Phase, f.WaitDeadline = applyFailed, 0
	if outcome == waitIdleCancelled {
		f.Phase, f.Error = applyCancelled, "cancelled while waiting for the server to go idle"
	} else {
		f.Error = fmt.Sprintf("waited %s for the server to go idle and gave up: %s",
			time.Duration(f.WaitTimeoutSec)*time.Second, s.idleWaitReason())
	}
	s.saveRestart(f)
	s.applyMu.Lock()
	s.applyBusy = false
	s.applyMu.Unlock()
	slog.Info("server.restart_wait_over", "event", "server.restart_wait_over", "id", f.ID,
		"phase", f.Phase, "released", released, "error", f.Error)
}

// failRestart ends a restart that failed before (or instead of) the handover.
func (s *Server) failRestart(f *restartFile, err error) {
	s.releaseParkedJobs()
	f.Phase, f.Error, f.WaitDeadline = applyFailed, err.Error(), 0
	s.saveRestart(f)
	s.applyMu.Lock()
	s.applyBusy = false
	s.applyMu.Unlock()
	slog.Error("server.restart_failed", "event", "server.restart_failed", "id", f.ID, "error", f.Error)
}

// handleServerRestartCancel is DELETE /v1/server/restart: stop a restart that is waiting
// for the server to go idle. A cancel that arrives after the handover is refused, the
// restart being under way already.
func (s *Server) handleServerRestartCancel(c *rux.Context) {
	if !adminUserOnly(c, "cancel a server restart") {
		return
	}
	s.applyMu.Lock()
	cancel := s.applyWaitCancel
	s.applyMu.Unlock()
	if cancel == nil {
		writeError(c, http.StatusConflict, "no restart to cancel",
			"no restart is waiting for the server to go idle right now")
		return
	}
	cancel()
	// The waiter owns the state: it releases the parked jobs and writes the cancelled
	// phase a moment later. The caller polls GET /v1/server/restart for that.
	c.JSON(http.StatusAccepted, s.restartStateView())
}

// ResumeServerRestart settles the restart state a previous process left behind and is
// how a restart becomes observable as finished: the process that asked for it exits, so
// the new one reads `restarting` and reports done. A `waiting_idle` state whose waiter
// is gone (a restart the operator cancelled by hand, say) is failed, not left hanging.
func (s *Server) ResumeServerRestart() {
	f, ok := s.loadRestart()
	if !ok {
		return
	}
	switch f.Phase {
	case applyRestarting:
		f.Phase, f.WaitDeadline = applyDone, 0
		s.saveRestart(&f)
		slog.Info("server.restart_done", "event", "server.restart_done", "id", f.ID)
	case applyWaitingIdle:
		f.Phase, f.WaitDeadline = applyFailed, 0
		f.Error = "the server restarted while the restart was waiting for it to go idle; run it again"
		s.saveRestart(&f)
		slog.Error("server.restart_failed", "event", "server.restart_failed", "id", f.ID, "error", f.Error)
	}
}

// restartStateView is the persisted restart state with the live wait census filled in.
func (s *Server) restartStateView() *ServerRestartState {
	f, ok := s.loadRestart()
	if !ok {
		return nil
	}
	view := f.ServerRestartState
	if view.Phase == applyWaitingIdle {
		view.Waiting, view.Paused = s.waitingSnapshot()
	}
	return &view
}

func (s *Server) restartStatePath() string {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if s.applyDir == "" {
		return ""
	}
	return filepath.Join(s.applyDir, restartStateFile)
}

func (s *Server) loadRestart() (restartFile, bool) {
	var f restartFile
	p := s.restartStatePath()
	if p == "" {
		return f, false
	}
	data, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(data, &f) != nil || f.ID == "" {
		return f, false
	}
	return f, true
}

func (s *Server) saveRestart(f *restartFile) {
	p := s.restartStatePath()
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
		slog.Error("server.restart_state_write_failed", "error", err)
	}
}
