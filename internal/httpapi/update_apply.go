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

// Apply phases. downloading and restarting belong to the old server; the new server
// moves restarting on to upgrading_workers and done (or failed).
const (
	applyDownloading = "downloading"
	applyRestarting  = "restarting"
	applyWorkers     = "upgrading_workers"
	applyDone        = "done"
	applyFailed      = "failed"
)

const (
	// applyTimeout bounds the old server's part: downloads plus the drain.
	applyTimeout = 30 * time.Minute
	// applyWorkerWait is how long the new server waits for a worker to reconnect.
	applyWorkerWait = 3 * time.Minute
	applyStateFile  = "apply.json"
)

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
// from its upgrade record.
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
	return &view
}

type applyFile struct {
	ApplyState
	Workers []applyWorkerFile `json:"workers"`
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
}

// handleUpdateApply is POST /v1/update/apply: start a one-click update. It answers
// 202 with the apply state; progress is GET /v1/update's apply.
func (s *Server) handleUpdateApply(c *rux.Context) {
	if !s.upgradeAdmin(c) {
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
	known := s.workerConfigs()
	f := applyFile{ApplyState: ApplyState{ID: newUpgradeID(), From: st.Current, Version: st.Latest, Phase: applyDownloading, StartedAt: time.Now().Unix()}}
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
// version, then hand the server binary to the managed upgrade.
func (s *Server) runUpdateApply(f applyFile, managed ManagedUpgradeFunc, dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()
	fail := func(err error) {
		f.Phase, f.Error = applyFailed, err.Error()
		s.saveApply(&f)
		s.applyMu.Lock()
		s.applyBusy = false
		s.applyMu.Unlock()
		slog.Error("update.apply_failed", "event", "update.apply_failed", "id", f.ID, "error", err)
	}
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
	f.Phase = applyRestarting
	s.saveApply(&f)
	slog.Info("update.apply_restarting", "event", "update.apply_restarting", "id", f.ID, "binary", own)
	if err := managed(ctx, own); err != nil {
		fail(fmt.Errorf("upgrade the server: %w", err))
		return
	}
	// Accepted: this process is about to be replaced; the new server resumes the apply.
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

// ResumeUpdateApply is the new server's part, run once at startup: when an apply was
// restarting this server, confirm the new version and upgrade the chosen workers.
func (s *Server) ResumeUpdateApply(ctx context.Context) {
	f, ok := s.loadApply()
	if !ok || f.Phase != applyRestarting {
		return
	}
	if !sameRelease(s.build.Version, f.Version) {
		f.Phase = applyFailed
		f.Error = fmt.Sprintf("the server still runs %s after the restart (the upgrade did not take effect; see gofer serve upgrade status)", s.build.DisplayVersion())
		s.saveApply(&f)
		slog.Error("update.apply_failed", "event", "update.apply_failed", "id", f.ID, "error", f.Error)
		return
	}
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
