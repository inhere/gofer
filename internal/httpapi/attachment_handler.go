package httpapi

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/attachment"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/workbench"
)

// AttachmentThreadAccess decides whether the caller of a request may read the workbench thread
// an attachment was SENT to. It is the seam the workbench send task plugs its own visibility
// rule into (SetAttachmentThreadAccess); until then threadReadableByCaller below applies the
// same rule the thread routes use (the thread's owner, or the matching *.read.others
// permission). An implementation must fail closed: an unknown or unparseable thread is false.
type AttachmentThreadAccess interface {
	MayReadThread(c *rux.Context, threadID string) bool
}

// SetAttachments injects the image-attachment store (serve passes the one it also prunes).
// Like SetXfer the routes are always mounted and answer 503 while it is nil.
func (s *Server) SetAttachments(a *attachment.Service) {
	s.attachments = a
	s.wireWorkbenchAttachments()
}

// wireWorkbenchAttachments hands the workbench its send-side dependencies once the pieces
// exist (the store and the transfer manager are injected after the workbench is built, in
// either order). Without an attachment store the workbench keeps reporting no capability.
func (s *Server) wireWorkbenchAttachments() {
	if s.workbench == nil || s.attachments == nil || s.jobs == nil {
		return
	}
	deps := workbench.AttachmentDeps{
		Store: s.jobs.Meta(), Files: s.attachments, Jobs: s.jobs, Config: s.jobs.Config,
		MaxPerMessage: func() int { return s.jobs.Config().EffectiveAttachments().MaxPerMessage },
	}
	if s.xfer != nil {
		deps.Xfer = s.xfer
	}
	s.workbench.SetAttachments(deps)
}

// SetAttachmentThreadAccess replaces the default sent-attachment visibility rule; nil restores the
// default. The default (threadReadableByCaller) is already the workbench's own rule: the owner
// resolution workbenchThreadOwner plus the job.read.others / session.read.others permissions the
// thread routes use, so the workbench send path needs no override.
func (s *Server) SetAttachmentThreadAccess(a AttachmentThreadAccess) { s.attachmentAccess = a }

// attachmentMultipartOverhead is how much the multipart envelope (boundary, part headers) may
// add on top of the file itself before the body is cut off.
const attachmentMultipartOverhead = 64 << 10

type attachmentResp struct {
	ID        string `json:"id"`
	Mime      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

func attachmentRespOf(a jobstore.Attachment) attachmentResp {
	return attachmentResp{ID: a.ID, Mime: a.Mime, Bytes: a.Bytes, Width: a.Width, Height: a.Height, ExpiresAt: a.ExpiresAt}
}

// attachmentGuard is the shared head of the three handlers: a user-class caller only (a job
// credential or a worker has no attachments of its own), and the store must be wired.
func (s *Server) attachmentGuard(c *rux.Context) bool {
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "attachments require a user caller", "worker and job credentials cannot use attachments")
		return false
	}
	if s.attachments == nil {
		writeError(c, http.StatusServiceUnavailable, "attachments unavailable", "the attachment store is not wired")
		return false
	}
	return true
}

func (s *Server) handleAttachmentUpload(c *rux.Context) {
	if !s.attachmentGuard(c) {
		return
	}
	max := s.attachments.MaxBytes()
	if c.Req.ContentLength > max+attachmentMultipartOverhead {
		writeError(c, http.StatusRequestEntityTooLarge, "attachment too large", "the upload exceeds "+strconv.FormatInt(max, 10)+" bytes")
		return
	}
	c.Req.Body = http.MaxBytesReader(c.Resp, c.Req.Body, max+attachmentMultipartOverhead)
	mr, err := c.Req.MultipartReader()
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid multipart body", err.Error())
		return
	}
	var file io.Reader
	for {
		part, perr := mr.NextPart()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			writeAttachmentReadError(c, perr)
			return
		}
		if part.FormName() == "file" && part.FileName() != "" {
			file = filePart{part}
			break
		}
		_ = part.Close()
	}
	if file == nil {
		writeError(c, http.StatusBadRequest, "missing file", `multipart field "file" is required`)
		return
	}
	rec, err := s.attachments.Save(callerFromCtx(c), file)
	switch {
	case err == nil:
		c.JSON(http.StatusCreated, attachmentRespOf(rec))
	case errors.Is(err, attachment.ErrRateLimited):
		c.SetHeader("Retry-After", "60")
		writeError(c, http.StatusTooManyRequests, "attachment upload limit", "too many uploads this hour; retry later")
	case errors.Is(err, attachment.ErrTooLarge):
		writeError(c, http.StatusRequestEntityTooLarge, "attachment too large", "the file exceeds "+strconv.FormatInt(max, 10)+" bytes")
	case errors.Is(err, attachment.ErrUnsupportedType):
		writeError(c, http.StatusUnsupportedMediaType, "unsupported attachment type", "only PNG, JPEG, GIF and WebP images are accepted")
	default:
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(c, http.StatusRequestEntityTooLarge, "attachment too large", "the upload exceeds "+strconv.FormatInt(max, 10)+" bytes")
			return
		}
		writeError(c, http.StatusInternalServerError, "store attachment failed", err.Error())
	}
}

// filePart narrows a multipart part to an io.Reader (the part also carries a Close the upload
// does not need).
type filePart struct{ p *multipart.Part }

func (f filePart) Read(b []byte) (int, error) { return f.p.Read(b) }

func writeAttachmentReadError(c *rux.Context, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeError(c, http.StatusRequestEntityTooLarge, "attachment too large", "the upload is larger than the configured limit")
		return
	}
	writeError(c, http.StatusBadRequest, "invalid multipart body", err.Error())
}

// lookupReadable resolves {id} for a read: ok=false means the response was already written. An
// attachment the caller may not see is a 404, like a missing one, so ids do not leak.
func (s *Server) lookupReadable(c *rux.Context) (jobstore.Attachment, bool) {
	rec, found, err := s.attachments.Get(strings.TrimSpace(c.Param("id")))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get attachment failed", err.Error())
		return jobstore.Attachment{}, false
	}
	if !found || !s.attachmentReadable(c, rec) {
		writeError(c, http.StatusNotFound, "no such attachment", "attachment not found")
		return jobstore.Attachment{}, false
	}
	return rec, true
}

// attachmentReadable is the read rule. UNSENT (no thread): the uploader only. SENT: the
// uploader, or whoever may read the thread it belongs to (AttachmentThreadAccess; default
// threadReadableByCaller). Fail closed.
func (s *Server) attachmentReadable(c *rux.Context, rec jobstore.Attachment) bool {
	if rec.Caller != "" && rec.Caller == callerFromCtx(c) {
		return true
	}
	if rec.ThreadID == "" {
		return false
	}
	if s.attachmentAccess != nil {
		return s.attachmentAccess.MayReadThread(c, rec.ThreadID)
	}
	return s.threadReadableByCaller(c, rec.ThreadID)
}

// threadReadableByCaller is the default rule for a sent attachment: the caller owns the thread
// (the job / session it stands for) or holds the *.read.others permission of the referenced
// resource kind — job.read.others for job and agent threads, session.read.others for terminal
// relay threads. It mirrors how the thread routes judge reading; the workbench send task may
// override it through SetAttachmentThreadAccess.
func (s *Server) threadReadableByCaller(c *rux.Context, threadID string) bool {
	kind, raw, err := workbench.ParseThreadID(threadID)
	if err != nil {
		return false
	}
	owner, ok := s.workbenchThreadOwner(kind, raw)
	if !ok {
		return false
	}
	if owner != "" && owner == callerFromCtx(c) {
		return true
	}
	switch kind {
	case workbench.KindRelay:
		return s.can(c, authz.PermSessionReadOthers)
	case workbench.KindJob, workbench.KindAgent:
		return s.can(c, authz.PermJobReadOthers)
	}
	return false
}

func (s *Server) handleAttachmentGet(c *rux.Context) {
	if !s.attachmentGuard(c) {
		return
	}
	rec, ok := s.lookupReadable(c)
	if !ok {
		return
	}
	var (
		data []byte
		mime = rec.Mime
		err  error
	)
	if v := c.Query("thumb"); v == "1" || v == "true" {
		data, mime, err = s.attachments.ReadThumb(rec)
	} else {
		data, err = s.attachments.Read(rec)
	}
	if errors.Is(err, attachment.ErrNotFound) {
		writeError(c, http.StatusNotFound, "no such attachment", "attachment file is gone")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read attachment failed", err.Error())
		return
	}
	h := c.Resp.Header()
	h.Set("Content-Type", mime)
	h.Set("Content-Disposition", "inline")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	c.Resp.WriteHeader(http.StatusOK)
	_, _ = c.Resp.Write(data)
}

func (s *Server) handleAttachmentDelete(c *rux.Context) {
	if !s.attachmentGuard(c) {
		return
	}
	// Only the uploader may delete, and only before the send: anyone else gets the same 404 as
	// for a missing attachment.
	rec, found, err := s.attachments.Get(strings.TrimSpace(c.Param("id")))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "get attachment failed", err.Error())
		return
	}
	if !found || rec.Caller == "" || rec.Caller != callerFromCtx(c) {
		writeError(c, http.StatusNotFound, "no such attachment", "attachment not found")
		return
	}
	switch err := s.attachments.DeleteUnsent(rec); {
	case err == nil:
		c.JSON(http.StatusOK, map[string]any{"deleted": rec.ID})
	case errors.Is(err, jobstore.ErrAttachmentBound):
		writeError(c, http.StatusConflict, "attachment already sent", "a sent attachment cannot be deleted")
	case errors.Is(err, jobstore.ErrAttachmentNotFound):
		writeError(c, http.StatusNotFound, "no such attachment", "attachment not found")
	default:
		writeError(c, http.StatusInternalServerError, "delete attachment failed", err.Error())
	}
}
