package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/xfer"
)

// xferView is the JSON projection of a transfer journal row (XFER-01 design
// §一.2). The wire keys are fixed by the CLI/client contract: project (not
// project_key) names the project, and every timestamp is unix SECONDS.
type xferView struct {
	ID         string `json:"id"`
	Op         string `json:"op"`
	Runner     string `json:"runner"`
	Project    string `json:"project"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	CallerID   string `json:"caller_id,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
}

func xferJSON(rec jobstore.XferRecord) xferView {
	return xferView{
		ID: rec.ID, Op: rec.Op, Runner: rec.Runner, Project: rec.ProjectKey, Path: rec.Path,
		Size: rec.Size, SHA256: rec.SHA256, State: rec.State, Error: rec.Error,
		CallerID: rec.CallerID, CreatedAt: rec.CreatedAt, FinishedAt: rec.FinishedAt, ExpiresAt: rec.ExpiresAt,
	}
}

// xferMeta is the JSON `meta` field of the multipart create request (a pull
// sends the same object as the whole body).
type xferMeta struct {
	Op      string `json:"op"`
	Runner  string `json:"runner"`
	Project string `json:"project"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Force   bool   `json:"force"`
}

// xferUnavailable answers 503 when no transfer manager is wired (mcp / tests):
// the routes are always mounted so the surface is uniform, exactly like
// /v1/workers/{id}/reload.
func (s *Server) xferUnavailable(c *rux.Context) bool {
	if s.xfer == nil {
		writeError(c, http.StatusServiceUnavailable, "xfer unavailable", "this server has no transfer manager wired")
		return true
	}
	return false
}

// handleXferCreate serves POST /v1/xfer: both the multipart push (op=put, the
// payload rides along) and the JSON pull (op=get, the record is created and
// dispatched). It is a USER-caller surface: a worker token may only fetch or
// upload the content of a transfer assigned to it (see the content handlers),
// never create one — otherwise a compromised worker could park arbitrary bytes
// in the server's staging area.
func (s *Server) handleXferCreate(c *rux.Context) {
	if s.xferUnavailable(c) {
		return
	}
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "user caller required", "only a user caller may create a transfer")
		return
	}
	caller := callerFromCtx(c)
	if strings.HasPrefix(c.Req.Header.Get("Content-Type"), "multipart/form-data") {
		s.xferCreatePush(c, caller)
		return
	}
	s.xferCreatePull(c, caller)
}

// xferCreatePull handles the JSON form: {op:"get", runner, project, path}.
func (s *Server) xferCreatePull(c *rux.Context, caller string) {
	body, err := io.ReadAll(io.LimitReader(c.Req.Body, 64<<10))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid body", err.Error())
		return
	}
	var meta xferMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json", err.Error())
		return
	}
	if meta.Op != string(xfer.OpGet) {
		writeError(c, http.StatusBadRequest, "unsupported op", "a JSON body must carry op=\"get\"; a push is multipart/form-data")
		return
	}
	if status, msg, detail := s.validateXferTarget(meta.Runner, meta.Project, meta.Path); status != 0 {
		writeError(c, status, msg, detail)
		return
	}
	rec, err := s.xfer.StageGet(caller, meta.Runner, meta.Project, meta.Path)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "stage get failed", err.Error())
		return
	}
	s.xfer.Dispatch(xferDispatchContext(c), rec.ID)
	c.JSON(http.StatusOK, map[string]any{"id": rec.ID, "state": rec.State})
}

// xferDispatchContext returns a context for the BACKGROUND delivery a transfer
// starts. It must NOT be the request's: net/http cancels that the moment the
// handler returns, which is immediately after the record is staged — every
// transfer would then settle `failed: context canceled` without the payload ever
// being touched. WithoutCancel keeps the request's values (tracing) but drops its
// cancellation, exactly because the dispatch is designed to outlive the response
// (design §一.2); the transfer's own timeout bounds it instead.
func xferDispatchContext(c *rux.Context) context.Context {
	return context.WithoutCancel(c.Req.Context())
}

// xferCreatePush handles the multipart form: the `meta` field (first) then the
// `file` part, which is streamed into the staging area while its sha256 is
// computed. The declared digest is verified against the streamed one — a
// mismatch (or an oversized payload) DELETES the staging directory before
// answering 400/413, so a corrupted upload can never be dispatched.
func (s *Server) xferCreatePush(c *rux.Context, caller string) {
	mr, err := c.Req.MultipartReader()
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid multipart body", err.Error())
		return
	}
	metaSeen := false
	var meta xferMeta
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid multipart body", err.Error())
			return
		}
		switch part.FormName() {
		case "meta":
			if metaSeen {
				writeError(c, http.StatusBadRequest, "duplicate meta", "the meta field must appear once, before the file")
				return
			}
			raw, rerr := io.ReadAll(io.LimitReader(part, 64<<10))
			if rerr != nil {
				writeError(c, http.StatusBadRequest, "invalid meta", rerr.Error())
				return
			}
			if derr := json.Unmarshal(raw, &meta); derr != nil {
				writeError(c, http.StatusBadRequest, "invalid meta", derr.Error())
				return
			}
			metaSeen = true
			if meta.Op != string(xfer.OpPut) {
				writeError(c, http.StatusBadRequest, "unsupported op", "a multipart body must carry op=\"put\"")
				return
			}
			if status, msg, detail := s.validateXferTarget(meta.Runner, meta.Project, meta.Path); status != 0 {
				writeError(c, status, msg, detail)
				return
			}
			if meta.Size > s.xfer.Limits().MaxBytes {
				writeError(c, http.StatusRequestEntityTooLarge, "too large",
					"transfer is "+strconv.FormatInt(meta.Size, 10)+" bytes; the limit is "+
						strconv.FormatInt(s.xfer.Limits().MaxBytes, 10))
				return
			}
		case "file":
			if !metaSeen {
				writeError(c, http.StatusBadRequest, "meta must come first", "the meta field must precede the file part")
				return
			}
			s.xferAcceptFile(c, caller, meta, part)
			return
		}
	}
	if !metaSeen {
		writeError(c, http.StatusBadRequest, "missing meta", "the multipart body needs a meta json field")
		return
	}
	writeError(c, http.StatusBadRequest, "missing file", "op=put needs a file part")
}

// xferAcceptFile streams one file part into the staging area for a staged put.
func (s *Server) xferAcceptFile(c *rux.Context, caller string, meta xferMeta, body io.Reader) {
	rec, err := s.xfer.StagePut(caller, meta.Runner, meta.Project, meta.Path, meta.Size, meta.SHA256, meta.Force)
	if err != nil {
		if errors.Is(err, xfer.ErrTooLarge) {
			writeError(c, http.StatusRequestEntityTooLarge, "too large", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "stage put failed", err.Error())
		return
	}
	w, err := s.xfer.Store().Writer(rec.ID)
	if err != nil {
		_ = s.xfer.Fail(rec.ID, err.Error())
		writeError(c, http.StatusInternalServerError, "stage put failed", err.Error())
		return
	}
	max := s.xfer.Limits().MaxBytes
	sum := sha256.New()
	// Read one byte past the cap so an over-limit stream is DETECTED rather than
	// silently truncated (a truncated file would still look like a valid upload).
	n, cerr := io.Copy(io.MultiWriter(w, sum), io.LimitReader(body, max+1))
	closeErr := w.Close()
	if cerr != nil || closeErr != nil {
		reason := cerr
		if reason == nil {
			reason = closeErr
		}
		_ = s.xfer.Fail(rec.ID, reason.Error())
		writeError(c, http.StatusInternalServerError, "upload failed", reason.Error())
		return
	}
	if n > max {
		_ = s.xfer.Fail(rec.ID, xfer.ErrTooLarge.Error())
		writeError(c, http.StatusRequestEntityTooLarge, "too large",
			"upload exceeded the "+strconv.FormatInt(max, 10)+" byte limit")
		return
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if meta.Size > 0 && n != meta.Size {
		_ = s.xfer.Fail(rec.ID, "size mismatch")
		writeError(c, http.StatusBadRequest, "size mismatch",
			"declared "+strconv.FormatInt(meta.Size, 10)+" bytes, received "+strconv.FormatInt(n, 10))
		return
	}
	if meta.SHA256 != "" && !strings.EqualFold(got, meta.SHA256) {
		_ = s.xfer.Fail(rec.ID, "sha256 mismatch")
		writeError(c, http.StatusBadRequest, "sha256 mismatch",
			"declared "+meta.SHA256+", computed "+got)
		return
	}
	if err := s.xfer.CommitPut(rec.ID, n, got); err != nil {
		_ = s.xfer.Fail(rec.ID, err.Error())
		writeError(c, http.StatusInternalServerError, "commit failed", err.Error())
		return
	}
	s.xfer.Dispatch(xferDispatchContext(c), rec.ID)
	c.JSON(http.StatusOK, map[string]any{"id": rec.ID, "state": string(xfer.StateStaged)})
}

// handleXferList serves GET /v1/xfer?state=&runner=&limit=.
func (s *Server) handleXferList(c *rux.Context) {
	if s.xferUnavailable(c) {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := s.xfer.List(jobstore.XferFilter{
		State:  c.Query("state"),
		Runner: c.Query("runner"),
		Limit:  limit,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list failed", err.Error())
		return
	}
	out := make([]xferView, 0, len(rows))
	for _, r := range rows {
		out = append(out, xferJSON(r))
	}
	c.JSON(http.StatusOK, map[string]any{"xfers": out})
}

// handleXferStatus serves GET /v1/xfer/{id}.
func (s *Server) handleXferStatus(c *rux.Context) {
	if s.xferUnavailable(c) {
		return
	}
	rec, ok, err := s.xfer.Get(c.Param("id"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown xfer", "no transfer with id "+c.Param("id"))
		return
	}
	c.JSON(http.StatusOK, xferJSON(rec))
}

// handleXferDelete serves DELETE /v1/xfer/{id} (`gofer tool xfer rm`). Only a
// user caller may drop a transfer; a worker token has no business deleting the
// server's staging entries.
func (s *Server) handleXferDelete(c *rux.Context) {
	if s.xferUnavailable(c) {
		return
	}
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "user caller required", "only a user caller may delete a transfer")
		return
	}
	id := c.Param("id")
	rec, ok, err := s.xfer.Get(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown xfer", "no transfer with id "+id)
		return
	}
	if err := s.xfer.Remove(id); err != nil {
		writeError(c, http.StatusInternalServerError, "delete failed", err.Error())
		return
	}
	slog.Info("xfer.removed", "event", "xfer.removed", "component", "server",
		"xfer_id", rec.ID, "runner", rec.Runner, "project", rec.ProjectKey, "path", rec.Path)
	c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// handleXferContentGet streams a transfer's payload.
//
// Two audiences, two rules (design §一.2): a USER downloads the RESULT of a get
// (the file a worker uploaded), and a WORKER downloads the SOURCE of a put
// assigned to it. The worker check is what keeps a transfer id from being a
// capability: a worker may only touch an id whose runner is itself.
func (s *Server) handleXferContentGet(c *rux.Context) {
	if s.xferUnavailable(c) {
		return
	}
	id := c.Param("id")
	rec, ok, err := s.xfer.Get(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown xfer", "no transfer with id "+id)
		return
	}
	if callerKindFromCtx(c) == callerKindWorker {
		if rec.Runner != callerFromCtx(c) {
			writeError(c, http.StatusForbidden, "not your transfer", "this transfer is assigned to runner "+rec.Runner)
			return
		}
		if rec.Op != string(xfer.OpPut) {
			writeError(c, http.StatusForbidden, "wrong direction", "a worker may only fetch the source of a put")
			return
		}
	} else {
		if rec.Op != string(xfer.OpGet) {
			writeError(c, http.StatusConflict, "nothing to download",
				"this is a put transfer; its payload was uploaded by the caller")
			return
		}
		if xfer.State(rec.State) != xfer.StateDone {
			writeError(c, http.StatusConflict, "not ready",
				"the transfer is "+rec.State+"; download once it is done")
			return
		}
	}
	f, err := s.xfer.Store().Reader(id)
	if err != nil {
		writeError(c, http.StatusNotFound, "no payload", err.Error())
		return
	}
	defer f.Close()
	if rec.SHA256 != "" {
		c.SetHeader("X-Gofer-Sha256", rec.SHA256)
	}
	if rec.Size > 0 {
		c.SetHeader("X-Gofer-Size", strconv.FormatInt(rec.Size, 10))
	}
	c.SetHeader("Content-Type", "application/octet-stream")
	if _, err := io.Copy(c.Resp, f); err != nil {
		slog.Warn("xfer.content_stream_failed", "event", "xfer.content_stream_failed", "component", "server",
			"xfer_id", id, "err", err)
	}
}

// handleXferContentPut accepts the payload a worker uploads for a get: the bytes
// are streamed into the staging area while their sha256 is computed, then
// verified against the X-Gofer-Sha256 / X-Gofer-Size headers. A verified upload
// settles the transfer done — the result frame the worker sends next is then a
// no-op, so a lost frame can never strand a transfer whose bytes are already
// safely staged.
func (s *Server) handleXferContentPut(c *rux.Context) {
	if s.xferUnavailable(c) {
		return
	}
	if callerKindFromCtx(c) != callerKindWorker {
		writeError(c, http.StatusForbidden, "worker caller required", "only the assigned worker may upload a transfer's content")
		return
	}
	id := c.Param("id")
	rec, ok, err := s.xfer.Get(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown xfer", "no transfer with id "+id)
		return
	}
	if rec.Runner != callerFromCtx(c) {
		writeError(c, http.StatusForbidden, "not your transfer", "this transfer is assigned to runner "+rec.Runner)
		return
	}
	if rec.Op != string(xfer.OpGet) {
		writeError(c, http.StatusConflict, "wrong direction", "a worker only uploads the result of a get")
		return
	}
	w, err := s.xfer.Store().Writer(id)
	if err != nil {
		_ = s.xfer.Fail(id, err.Error())
		writeError(c, http.StatusInternalServerError, "stage failed", err.Error())
		return
	}
	max := s.xfer.Limits().MaxBytes
	sum := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(w, sum), io.LimitReader(c.Req.Body, max+1))
	closeErr := w.Close()
	if cerr != nil || closeErr != nil {
		reason := cerr
		if reason == nil {
			reason = closeErr
		}
		_ = s.xfer.Fail(id, reason.Error())
		writeError(c, http.StatusInternalServerError, "upload failed", reason.Error())
		return
	}
	if n > max {
		_ = s.xfer.Fail(id, xfer.ErrTooLarge.Error())
		writeError(c, http.StatusRequestEntityTooLarge, "too large",
			"upload exceeded the "+strconv.FormatInt(max, 10)+" byte limit")
		return
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if want := c.Req.Header.Get("X-Gofer-Sha256"); want != "" && !strings.EqualFold(want, got) {
		_ = s.xfer.Fail(id, "sha256 mismatch")
		writeError(c, http.StatusBadRequest, "sha256 mismatch", "declared "+want+", computed "+got)
		return
	}
	if want := c.Req.Header.Get("X-Gofer-Size"); want != "" {
		if wantN, perr := strconv.ParseInt(want, 10, 64); perr == nil && wantN != n {
			_ = s.xfer.Fail(id, "size mismatch")
			writeError(c, http.StatusBadRequest, "size mismatch",
				"declared "+want+" bytes, received "+strconv.FormatInt(n, 10))
			return
		}
	}
	if err := s.xfer.CommitGet(id, n, got); err != nil {
		_ = s.xfer.Fail(id, err.Error())
		writeError(c, http.StatusInternalServerError, "commit failed", err.Error())
		return
	}
	if err := s.xfer.MarkDone(id); err != nil {
		writeError(c, http.StatusInternalServerError, "settle failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"id": id, "size": n, "sha256": got, "state": string(xfer.StateDone)})
}

// validateXferTarget checks a transfer target before anything is staged: the
// runner must be the server itself or a REGISTERED worker (404), the project must
// be known (404) and the path must resolve inside that project's execution root
// (400). The path check is the same boundary a job's --cwd obeys, applied to the
// SERVER's view of the project root — the executing worker re-validates against
// its own root (design §一.1).
func (s *Server) validateXferTarget(runner, projectKey, path string) (int, string, string) {
	name := config.NormalizeRunnerName(runner)
	if name == "" {
		return http.StatusBadRequest, "missing runner", "the meta needs a runner (a worker id, or server/local)"
	}
	if name != config.BuiltinLocalRunner {
		if s.cfg == nil {
			return http.StatusNotFound, "unknown runner", "no worker " + name + " is registered on this server"
		}
		if _, ok := s.cfg.Workers[name]; !ok {
			return http.StatusNotFound, "unknown runner", "no worker " + name + " is registered on this server"
		}
	}
	if strings.TrimSpace(path) == "" {
		return http.StatusBadRequest, "missing path", "the meta needs a project-relative path"
	}
	if s.projects == nil {
		return 0, "", ""
	}
	proj, err := s.projects.Get(projectKey)
	if err != nil {
		return http.StatusNotFound, "unknown project", "no project with key " + projectKey
	}
	if _, err := project.SafeJoin(s.projects.Config().ExecPath(proj), path); err != nil {
		return http.StatusBadRequest, "path escapes project", err.Error()
	}
	return 0, "", ""
}
