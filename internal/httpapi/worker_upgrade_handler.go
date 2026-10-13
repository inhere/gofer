package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/pushhub"
	"github.com/inhere/gofer/internal/workerupgrade"
	"github.com/inhere/gofer/internal/wsproto"
)

// Upgrade failure classes the hub adapter (serve) maps its own errors onto, so this
// layer can pick a status without importing wshub (D2/G022).
var (
	ErrUpgradeWorkerOffline = errors.New("worker offline")
	ErrUpgradeTooOld        = errors.New("worker too old for remote upgrade")
	ErrUpgradeTimeout       = errors.New("worker did not answer the upgrade request in time")
)

// workerUpgrader sends the upgrade request to a connected worker and returns the
// version its candidate binary reported once the worker has accepted it.
type workerUpgrader interface {
	UpgradeWorker(context.Context, string, wsproto.Upgrade) (string, error)
}

func (s *Server) SetWorkerUpgrader(u workerUpgrader) { s.upgrader = u }

// SetWorkerUpgrades injects the staging + upgrade-record store (nil = remote upgrade
// unavailable, the routes answer 503).
func (s *Server) SetWorkerUpgrades(m *workerupgrade.Manager) { s.upgrades = m }

// upgradeAcceptTimeout bounds the wait for the worker's FIRST answer (download +
// checksum + `--version`); the drain and handover that follow are asynchronous.
const upgradeAcceptTimeout = 5 * time.Minute

// upgradeStaleAfter is how long a pending upgrade blocks a new one: the full drain +
// handover budget plus slack.
const upgradeStaleAfter = time.Duration(wsproto.DefaultUpgradeDrainSec+wsproto.DefaultUpgradeReadySec)*time.Second + 5*time.Minute

// upgradeAdmin gates the admin-facing upgrade routes: a registered worker's own
// token must not be able to upgrade (or stage binaries for) any worker.
func (s *Server) upgradeAdmin(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser || !s.callerMayAdmin(callerFromCtx(c)) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return false
	}
	return true
}

func (s *Server) upgradeWorkerID(c *rux.Context) (string, bool) {
	id := c.Param("id")
	if _, ok := s.workerConfigs()[id]; !ok {
		writeError(c, http.StatusNotFound, "unknown worker", id)
		return "", false
	}
	if s.upgrades == nil {
		writeError(c, http.StatusServiceUnavailable, "worker upgrade unavailable", "this server has no upgrade staging area")
		return "", false
	}
	return id, true
}

// handleWorkerUpgradeStage is PUT /v1/workers/{id}/upgrade/file: the raw request body
// becomes the binary worker {id} will download. The digest is computed here.
func (s *Server) handleWorkerUpgradeStage(c *rux.Context) {
	if !s.upgradeAdmin(c) {
		return
	}
	id, ok := s.upgradeWorkerID(c)
	if !ok {
		return
	}
	body := http.MaxBytesReader(c.Resp, c.Req.Body, workerupgrade.MaxBinaryBytes)
	st, err := s.upgrades.Stage(id, body, workerupgrade.MaxBinaryBytes)
	if err != nil {
		writeError(c, http.StatusBadRequest, "stage upgrade binary failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"worker_id": id, "sha256": st.SHA256, "size": st.Size})
}

// handleWorkerUpgradeFile is GET /v1/workers/{id}/upgrade/file: a worker downloads
// ITS staged binary with its own worker token. Every other credential is refused,
// including another worker's token and an ordinary caller token.
func (s *Server) handleWorkerUpgradeFile(c *rux.Context) {
	id := c.Param("id")
	if callerKindFromCtx(c) != callerKindWorker || callerFromCtx(c) != id {
		writeError(c, http.StatusForbidden, "not your upgrade file", "only worker "+id+" may download its upgrade binary")
		return
	}
	if s.upgrades == nil {
		writeError(c, http.StatusServiceUnavailable, "worker upgrade unavailable", "this server has no upgrade staging area")
		return
	}
	f, st, err := s.upgrades.Open(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, workerupgrade.ErrNotStaged) {
			status = http.StatusNotFound
		}
		writeError(c, status, "no upgrade binary", err.Error())
		return
	}
	defer f.Close()
	c.SetHeader("X-Gofer-Sha256", st.SHA256)
	c.SetHeader("X-Gofer-Size", strconv.FormatInt(st.Size, 10))
	c.SetHeader("Content-Type", "application/octet-stream")
	c.SetHeader("Content-Length", strconv.FormatInt(st.Size, 10))
	http.ServeContent(c.Resp, c.Req, "", time.Time{}, f)
}

// workerUpgradeRequest is the body of POST /v1/workers/{id}/upgrade. Source "staged"
// uses the binary uploaded with PUT .../upgrade/file; "server" (the default) stages
// this server's own executable, which only fits a worker of the same os/arch.
type workerUpgradeRequest struct {
	Source          string `json:"source"`
	Force           bool   `json:"force"`
	DrainTimeoutSec int    `json:"drain_timeout_sec"`
	ReadyTimeoutSec int    `json:"ready_timeout_sec"`
}

// handleWorkerUpgrade is POST /v1/workers/{id}/upgrade. It answers 202 once the
// worker has downloaded, verified and test-run the new binary and starts the handover;
// the final outcome is the `upgrade` record on GET /v1/workers/{id}.
func (s *Server) handleWorkerUpgrade(c *rux.Context) {
	if !s.upgradeAdmin(c) {
		return
	}
	id, ok := s.upgradeWorkerID(c)
	if !ok {
		return
	}
	if s.upgrader == nil {
		writeError(c, http.StatusServiceUnavailable, "worker upgrade unavailable", "this server has no worker upgrade hub")
		return
	}
	var req workerUpgradeRequest
	if c.Req.Body != nil && c.Req.ContentLength != 0 {
		if err := json.NewDecoder(c.Req.Body).Decode(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}
	rec, err := s.startWorkerUpgrade(c.Req.Context(), id, req, "")
	if err != nil {
		var f *upgradeFailure
		if errors.As(err, &f) {
			writeError(c, f.status, f.title, f.detail)
		} else {
			writeError(c, http.StatusInternalServerError, "worker upgrade failed", err.Error())
		}
		return
	}
	c.JSON(http.StatusAccepted, map[string]any{"worker_id": id, "accepted": true, "upgrade": rec})
}

// upgradeFailure is a refused or failed worker upgrade with the HTTP status the
// handler answers.
type upgradeFailure struct {
	status        int
	title, detail string
}

func (f *upgradeFailure) Error() string { return f.title + ": " + f.detail }

func failUpgrade(status int, title, detail string) error {
	return &upgradeFailure{status: status, title: title, detail: detail}
}

// startWorkerUpgrade is the core of POST /v1/workers/{id}/upgrade, shared with the
// one-click update (gofer-29gi): it stages the binary, asks the worker to take it and
// returns the record once the worker accepted. binary non-empty stages that file (a
// downloaded release binary for the worker's platform) instead of req.Source.
func (s *Server) startWorkerUpgrade(ctx context.Context, id string, req workerUpgradeRequest, binary string) (workerupgrade.Record, error) {
	if req.DrainTimeoutSec < 0 || req.ReadyTimeoutSec < 0 {
		return workerupgrade.Record{}, failUpgrade(http.StatusBadRequest, "invalid upgrade request", "timeouts must not be negative")
	}
	var ws WorkerStatus
	if s.workers != nil {
		ws, _ = s.workers.WorkerStatus(id)
	}
	if !ws.Connected {
		return workerupgrade.Record{}, failUpgrade(http.StatusConflict, "worker offline", "worker "+id+" is not connected")
	}
	if !wsproto.SupportsUpgrade(ws.ProtocolVersion) {
		return workerupgrade.Record{}, failUpgrade(http.StatusConflict, "worker too old",
			fmt.Sprintf("该 worker 版本过旧，需要手动升级一次 (worker %s speaks protocol v%d, remote upgrade needs v%d)", id, ws.ProtocolVersion, wsproto.UpgradeMinProtocolVersion))
	}

	var staged workerupgrade.Staged
	var err error
	targetVersion := ""
	switch source := strings.TrimSpace(req.Source); {
	case binary != "":
		staged, err = s.upgrades.StageFile(id, binary)
	case source == "staged":
		staged, err = s.upgrades.Staged(id)
		if errors.Is(err, workerupgrade.ErrNotStaged) {
			return workerupgrade.Record{}, failUpgrade(http.StatusBadRequest, "no staged binary", "upload one first: PUT /v1/workers/"+id+"/upgrade/file")
		}
	case source == "" || source == "server":
		if ws.OS != runtime.GOOS || ws.Arch != runtime.GOARCH {
			return workerupgrade.Record{}, failUpgrade(http.StatusConflict, "platform mismatch",
				fmt.Sprintf("worker %s is %s/%s but this server is %s/%s: upgrade it with an explicit binary (gofer worker upgrade %s --file <worker binary>)",
					id, ws.OS, ws.Arch, runtime.GOOS, runtime.GOARCH, id))
		}
		var exe string
		if exe, err = os.Executable(); err == nil {
			staged, err = s.upgrades.StageFile(id, exe)
		}
		targetVersion = s.build.DisplayVersion()
	default:
		return workerupgrade.Record{}, failUpgrade(http.StatusBadRequest, "invalid upgrade request", `source must be "staged" or "server"`)
	}
	if err != nil {
		return workerupgrade.Record{}, failUpgrade(http.StatusInternalServerError, "stage upgrade binary failed", err.Error())
	}

	upgradeID := newUpgradeID()
	rec, err := s.upgrades.Begin(id, upgradeID, ws.GoferVersion, targetVersion, staged.SHA256, req.Force, upgradeStaleAfter)
	if s.live != nil {
		defer s.live.Notify(pushhub.TopicRunners) // the runners page shows the upgrade record
	}
	if err != nil {
		return workerupgrade.Record{}, failUpgrade(http.StatusConflict, "upgrade already in progress", fmt.Sprintf("worker %s has a pending upgrade started at %s", id, time.UnixMilli(rec.StartedAt).Format(time.RFC3339)))
	}
	wreq := wsproto.Upgrade{
		RequestID:       upgradeID,
		SHA256:          staged.SHA256,
		Size:            staged.Size,
		Version:         targetVersion,
		URLPath:         "/v1/workers/" + id + "/upgrade/file",
		Force:           req.Force,
		DrainTimeoutSec: req.DrainTimeoutSec,
		ReadyTimeoutSec: req.ReadyTimeoutSec,
	}
	ctx, cancel := context.WithTimeout(ctx, upgradeAcceptTimeout)
	defer cancel()
	version, err := s.upgrader.UpgradeWorker(ctx, id, wreq)
	if err != nil {
		s.upgrades.Finish(id, upgradeID, workerupgrade.StateFailed, err.Error(), "")
		status := http.StatusConflict
		switch {
		case errors.Is(err, ErrUpgradeTimeout), errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		case errors.Is(err, ErrUpgradeWorkerOffline), errors.Is(err, ErrUpgradeTooOld):
			status = http.StatusConflict
		}
		return workerupgrade.Record{}, failUpgrade(status, "worker upgrade failed", err.Error())
	}
	s.upgrades.SetTargetVersion(id, upgradeID, version)
	rec, _ = s.upgrades.Latest(id)
	return rec, nil
}

func (s *Server) latestUpgrade(id string) *workerupgrade.Record {
	if s.upgrades == nil {
		return nil
	}
	rec, ok := s.upgrades.Latest(id)
	if !ok {
		return nil
	}
	return &rec
}

func (s *Server) upgradeHistory(id string) []workerupgrade.Record {
	if s.upgrades == nil {
		return nil
	}
	history, ok := s.upgrades.History(id)
	if !ok {
		return nil
	}
	return history
}

func newUpgradeID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "up-" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + hex.EncodeToString(b[:])
}
