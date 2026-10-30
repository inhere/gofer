package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/xfer"
)

// attachments.go is the send half of the workbench image attachments: which way an image
// reaches the receiving agent (RouteAttachments, pure), the thread capability bit List
// reports, and the Turn that actually delivers them.

// AttachMode is how the images of a message reach the receiver.
type AttachMode string

const (
	// AttachInline: ACP image content blocks (the session advertised promptCapabilities.image).
	AttachInline AttachMode = "inline"
	// AttachFile: the images are placed on the executing machine and the message names
	// their absolute paths; the agent opens them itself.
	AttachFile AttachMode = "file"
	// AttachArg: the images are placed on the executing machine and each absolute path rides
	// the ARGV of the job that carries the message (the agent's image_args template), so the
	// text stays clean — no `[附图 n]` line.
	AttachArg AttachMode = "arg"
	// AttachNone: the receiver cannot take images; Reason says why.
	AttachNone AttachMode = "none"
)

// Attachment errors; the HTTP layer maps them (403 / 409 / 400 / 502).
var (
	// ErrAttachmentForbidden: an attachment is unknown or belongs to another caller (the
	// two are indistinguishable to the caller, so ids do not leak).
	ErrAttachmentForbidden = errors.New("workbench: attachment not available to this caller")
	// ErrAttachmentSent: the attachment was already sent with another message, or expired.
	ErrAttachmentSent = errors.New("workbench: attachment already sent or expired")
	// ErrTooManyAttachments: more attachments than server.attachments.max_per_message.
	ErrTooManyAttachments = errors.New("workbench: too many attachments")
	// ErrAttachmentsUnsupported: the receiver takes no images (the reason follows).
	ErrAttachmentsUnsupported = errors.New("workbench: thread does not accept attachments")
	// ErrAttachmentDelivery: placing the images on the executing machine failed; nothing
	// was sent (design decision 6).
	ErrAttachmentDelivery = errors.New("workbench: attachment delivery failed")
)

// placeTimeout is the total budget for placing all of one message's images.
const placeTimeout = 60 * time.Second

// RouteInput is everything RouteAttachments decides on; the callers fill it from a thread
// (List) or from the concrete target of a Turn.
type RouteInput struct {
	Kind    ThreadKind
	Agent   string
	Runner  string
	Project string
	// Pty marks an interactive pty job: it takes no images at all.
	Pty bool
	// ACPAccepts: the thread is a live resident ACP session whose agent advertised
	// promptCapabilities.image (job.SessionAcceptsImages).
	ACPAccepts bool
	// FilePath: the agent can read an image by absolute path (config image_input = path).
	FilePath bool
	// Arg: the agent takes an image path as an argv flag (config image_input = arg, agent
	// image_args), so the placement result rides the next job's argv instead of the text.
	Arg bool
	// OwnDir: the executing machine can take the images into its OWN attachment directory
	// (<config-dir>/run/attachments/<date>/), which is what a session that belongs to no
	// project of that machine needs. True for the server itself and for a remote worker that
	// declared the capability (xfer.Manager.OwnAttachmentsSupported).
	OwnDir bool
}

// Route is the delivery decision; Reason is set only for AttachNone.
type Route struct {
	Mode   AttachMode
	Reason string
}

// RouteAttachments is the pure delivery-mode table:
//
//	pty job                                  -> none ("pty 会话不支持附图")
//	live ACP session accepting images        -> inline
//	agent cannot take images at all          -> none ("<agent> 暂不支持读取图片")
//	no runner to place onto                  -> none ("会话没有执行机信息")
//	no project and no own attachment dir     -> none (a hint to upgrade the worker)
//	agent takes images as argv flags (arg)   -> arg
//	otherwise                                -> file
//
// The project-less case is what OwnDir is for: with no project there is no project root to
// hang the placement under, so the images go to the executing machine's own attachment
// directory — which the server always has, a worker only when it declared
// wsproto.WorkerAttachMinProtocolVersion. An old worker is refused with the upgrade named
// instead of a placement it would drop.
func RouteAttachments(in RouteInput) Route {
	switch {
	case in.Pty:
		return Route{Mode: AttachNone, Reason: "pty 会话不支持附图"}
	case in.ACPAccepts:
		return Route{Mode: AttachInline}
	case !in.FilePath && !in.Arg:
		name := strings.TrimSpace(in.Agent)
		if name == "" {
			name = "该 agent"
		}
		return Route{Mode: AttachNone, Reason: name + " 暂不支持读取图片"}
	case strings.TrimSpace(in.Runner) == "":
		return Route{Mode: AttachNone, Reason: "会话没有执行机信息"}
	case strings.TrimSpace(in.Project) == "" && !in.OwnDir:
		return Route{Mode: AttachNone, Reason: ownDirReason(in.Runner)}
	case in.Arg:
		return Route{Mode: AttachArg}
	default:
		return Route{Mode: AttachFile}
	}
}

// ownDirReason names why a project-less session's images cannot be placed: the executor is
// a remote worker that did not declare its own attachment directory, which an operator
// fixes by upgrading (and restarting) that worker — or by connecting it, since an offline
// worker cannot declare anything either.
func ownDirReason(runner string) string {
	name := strings.TrimSpace(runner)
	if name == "" {
		name = "执行机"
	}
	return "会话没有项目，worker " + name + " 不能接收附件（未连接或版本过旧，请升级并重启该 worker）"
}

// AttachmentStore is the slice of jobstore.Store the send path uses.
type AttachmentStore interface {
	GetAttachment(id string) (jobstore.Attachment, bool, error)
	BindAttachment(id, threadID, refKind, refID string) error
	UnbindAttachment(id, threadID string) error
	ListAttachmentsByThread(threadID string) ([]jobstore.Attachment, error)
}

// AttachmentFiles reads the stored originals (attachment.Service).
type AttachmentFiles interface {
	Read(rec jobstore.Attachment) ([]byte, error)
	AbsPath(rec jobstore.Attachment) (string, error)
}

// Placer puts a local file onto the executing machine (xfer.Manager.PutAndWait).
type Placer interface {
	PutAndWait(ctx context.Context, caller, runner, projectKey, srcPath, dest string, timeout time.Duration) (string, error)
}

// OwnDirPlacer is the optional half of Placer for a session that belongs to no project: it
// places into the EXECUTING machine's own attachment directory and answers whether that
// machine can take one at all (xfer.Manager.PutAttachmentsAndWait /
// OwnAttachmentsSupported). A Placer that does not implement it cannot deliver to a
// project-less session, which is exactly what the route's refusal says.
type OwnDirPlacer interface {
	PutAttachmentsAndWait(ctx context.Context, caller, runner, srcPath, dest string, timeout time.Duration) (string, error)
	OwnAttachmentsSupported(runner string) bool
}

// ImageJobs is the image-carrying side of the job service.
type ImageJobs interface {
	SessionAcceptsImages(jobID string) (bool, error)
	SaySessionBlocks(jobID, text string, images []job.ImageInput) error
}

// RelaySessions is the optional lookup of a terminal session's executor.
type RelaySessions interface {
	Session(sid string) (jobstore.AgentSession, error)
}

// AttachmentDeps wires the send path; without Store/Files the service accepts no
// attachments and reports no capability bit.
type AttachmentDeps struct {
	Store         AttachmentStore
	Files         AttachmentFiles
	Xfer          Placer
	Jobs          ImageJobs
	Config        func() *config.Config
	MaxPerMessage func() int
}

// SetAttachments injects the attachment dependencies (the server wires them once its
// attachment store and transfer manager exist).
func (s *Service) SetAttachments(d AttachmentDeps) { s.att = d }

func (s *Service) attachmentsReady() bool {
	return s != nil && s.att.Store != nil && s.att.Files != nil && s.att.Config != nil
}

func (s *Service) maxPerMessage() int {
	if s.att.MaxPerMessage != nil {
		if n := s.att.MaxPerMessage(); n > 0 {
			return n
		}
	}
	return config.DefaultAttachmentMaxPerMessage
}

// imageInputAgent resolves how agent takes an image (config image_input: path|arg|none).
func (s *Service) imageInputAgent(agent string) string {
	cfg := s.att.Config()
	if cfg == nil {
		return config.ImageInputNone
	}
	return cfg.EffectiveImageInput(agent)
}

// applyImageInput fills the delivery bits the agent's image_input implies.
func (s *Service) applyImageInput(in *RouteInput) {
	switch s.imageInputAgent(in.Agent) {
	case config.ImageInputPath:
		in.FilePath = true
	case config.ImageInputArg:
		in.Arg = true
	}
}

// ownDir reports whether the executing machine behind runner can take an image into its own
// attachment directory: the server always can (this process writes it), a remote worker only
// when it declared the capability at registration.
func (s *Service) ownDir(runner string) bool {
	if strings.TrimSpace(runner) == "" {
		return false
	}
	if config.IsBuiltinLocalRunnerName(runner) {
		return true
	}
	placer, ok := s.att.Xfer.(OwnDirPlacer)
	if !ok {
		return false
	}
	return placer.OwnAttachmentsSupported(runner)
}

func (s *Service) deliverImageArgs(agent string) []string {
	cfg := s.att.Config()
	if cfg == nil {
		return nil
	}
	return cfg.EffectiveDeliverImageArgs(agent)
}

// annotateAttachments fills a listed thread's attachment capability bit.
func (s *Service) annotateAttachments(t *Thread) {
	if !s.attachmentsReady() {
		return
	}
	if !t.Resumable {
		t.Attachments, t.AttachmentsReason = AttachNone, "该会话已结束，不能继续发送"
		return
	}
	in := RouteInput{
		Kind: t.Kind, Agent: t.Agent, Runner: t.Runner, Project: t.ProjectKey,
		Pty:    t.Kind != KindRelay && t.Interactive,
		OwnDir: s.ownDir(t.Runner),
	}
	// A message to a live terminal session goes over the session's own delivery channel
	// (stdin / a queue command), which has no image flag yet: such a thread keeps the file
	// delivery for path-readers but is never advertised with the arg bit, so the 📎 never
	// promises what that send cannot do.
	if t.Kind == KindRelay {
		if s.imageInputAgent(t.Agent) == config.ImageInputPath {
			in.FilePath = true
		}
		// A relay thread whose delivery command can take image paths reports the arg bit:
		// the files are placed on the executor and each path becomes one argv flag.
		if len(s.deliverImageArgs(t.Agent)) > 0 {
			in.Arg = true
		}
	} else {
		s.applyImageInput(&in)
	}
	if t.Kind != KindRelay && !in.Pty && s.att.Jobs != nil && t.LatestJobID != "" {
		in.ACPAccepts, _ = s.att.Jobs.SessionAcceptsImages(t.LatestJobID)
	}
	r := RouteAttachments(in)
	t.Attachments = r.Mode
	if r.Mode == AttachNone {
		t.AttachmentsReason = r.Reason
	}
}

// loadAttachments resolves the ids of a send: each must exist, belong to caller and be
// unsent and unexpired; there may be at most max_per_message and no duplicates.
func (s *Service) loadAttachments(callerID string, ids []string) ([]jobstore.Attachment, error) {
	if !s.attachmentsReady() {
		return nil, ErrUnavailable
	}
	if limit := s.maxPerMessage(); len(ids) > limit {
		return nil, fmt.Errorf("%w: at most %d per message", ErrTooManyAttachments, limit)
	}
	seen := make(map[string]bool, len(ids))
	out := make([]jobstore.Attachment, 0, len(ids))
	now := s.now().Unix()
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return nil, fmt.Errorf("%w: duplicate or empty attachment id", job.ErrInvalidRequest)
		}
		seen[id] = true
		rec, ok, err := s.att.Store.GetAttachment(id)
		if err != nil {
			return nil, err
		}
		if !ok || rec.Caller == "" || rec.Caller != callerID {
			return nil, ErrAttachmentForbidden
		}
		if rec.ThreadID != "" || (rec.ExpiresAt > 0 && rec.ExpiresAt <= now) {
			return nil, ErrAttachmentSent
		}
		out = append(out, rec)
	}
	return out, nil
}

// bindAll marks every attachment as sent with the message (kind, ref) of threadID; on a
// failure the ones already bound are released again.
func (s *Service) bindAll(recs []jobstore.Attachment, threadID, refKind, refID string) error {
	for i, rec := range recs {
		if err := s.att.Store.BindAttachment(rec.ID, threadID, refKind, refID); err != nil {
			s.unbindAll(recs[:i], threadID)
			if errors.Is(err, jobstore.ErrAttachmentBound) || errors.Is(err, jobstore.ErrAttachmentNotFound) {
				return ErrAttachmentSent
			}
			return err
		}
	}
	return nil
}

func (s *Service) unbindAll(recs []jobstore.Attachment, threadID string) {
	for _, rec := range recs {
		if err := s.att.Store.UnbindAttachment(rec.ID, threadID); err != nil {
			slog.Warn("workbench: release attachment", "attachment", rec.ID, "err", err)
		}
	}
}

// bindAfterSend binds once the message is already out: a failure cannot unsend it, so it
// is logged only.
func (s *Service) bindAfterSend(recs []jobstore.Attachment, threadID, refKind, refID string) {
	if err := s.bindAll(recs, threadID, refKind, refID); err != nil {
		slog.Warn("workbench: bind attachments after send", "thread", threadID, "ref", refKind+":"+refID, "err", err)
	}
}

func imageExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
}

// placedImage is one image after it is on the executing machine.
// abs is the absolute path the agent can open; it is "" when a worker below protocol v25
// could not report where it wrote the file (dest is then the project-relative path).
type placedImage struct {
	abs  string
	dest string
}

// placeImages puts every image on the executing machine: under the project's attachment
// directory when the session has one, else under that MACHINE's own attachment directory
// (a project-less session, see RouteInput.OwnDir). All placements share one budget; any
// failure aborts with ErrAttachmentDelivery.
func (s *Service) placeImages(callerID string, recs []jobstore.Attachment, runner, projectKey string) ([]placedImage, error) {
	if s.att.Xfer == nil {
		return nil, fmt.Errorf("%w: file transfer is not available", ErrAttachmentDelivery)
	}
	if strings.TrimSpace(projectKey) == "" {
		return s.placeOwnImages(callerID, recs, runner)
	}
	cfg := s.att.Config()
	proj, ok := cfg.Projects[projectKey]
	if !ok {
		return nil, fmt.Errorf("%w: project %q is not configured on this server", ErrAttachmentDelivery, projectKey)
	}
	// <exchange subdir>/gofer-attachments/<yyyymmdd>: the base xfer prunes on the executing
	// machine (xfer.AttachmentsDir), as a project-relative path.
	base := path.Join(cfg.ResolvedExchangeSubdir(proj), xfer.AttachmentsDirName, s.now().Format("20060102"))
	deadline := s.now().Add(placeTimeout)
	out := make([]placedImage, 0, len(recs))
	for i, rec := range recs {
		src, err := s.att.Files.AbsPath(rec)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrAttachmentDelivery, err)
		}
		dest := path.Join(base, rec.ID+"."+imageExt(rec.Mime))
		remaining := deadline.Sub(s.now())
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: timed out placing the images (%s)", ErrAttachmentDelivery, placeTimeout)
		}
		abs, err := s.att.Xfer.PutAndWait(context.Background(), callerID, runner, projectKey, src, dest, remaining)
		if err != nil {
			return nil, fmt.Errorf("%w: 附图 %d: %v", ErrAttachmentDelivery, i+1, err)
		}
		out = append(out, placedImage{abs: abs, dest: dest})
	}
	return out, nil
}

// placeOwnImages places the images in the executing machine's own attachment directory:
// <config-dir>/run/attachments/<yyyymmdd>/<id>.<ext> on that machine. The date layout is
// the same one xfer prunes there, and the placement reports the absolute path (there is no
// project-relative path a receiver could resolve instead).
func (s *Service) placeOwnImages(callerID string, recs []jobstore.Attachment, runner string) ([]placedImage, error) {
	placer, ok := s.att.Xfer.(OwnDirPlacer)
	if !ok {
		return nil, fmt.Errorf("%w: 会话没有项目，无法把附图放到执行机", ErrAttachmentDelivery)
	}
	if !placer.OwnAttachmentsSupported(runner) {
		return nil, fmt.Errorf("%w: %s", ErrAttachmentDelivery, ownDirReason(runner))
	}
	base := s.now().Format("20060102")
	deadline := s.now().Add(placeTimeout)
	out := make([]placedImage, 0, len(recs))
	for i, rec := range recs {
		src, err := s.att.Files.AbsPath(rec)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrAttachmentDelivery, err)
		}
		// Relative to the machine's attachment dir: it owns the date level, exactly like the
		// project variant above (which is relative to the project root).
		dest := path.Join(base, rec.ID+"."+imageExt(rec.Mime))
		remaining := deadline.Sub(s.now())
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: timed out placing the images (%s)", ErrAttachmentDelivery, placeTimeout)
		}
		abs, err := placer.PutAttachmentsAndWait(context.Background(), callerID, runner, src, dest, remaining)
		if err != nil {
			return nil, fmt.Errorf("%w: 附图 %d: %v", ErrAttachmentDelivery, i+1, err)
		}
		out = append(out, placedImage{abs: abs, dest: dest})
	}
	return out, nil
}

// placeFiles puts every image on the executing machine and returns the text section naming
// them (appended to the message). oneLine puts the paths on the message's own line.
func (s *Service) placeFiles(callerID string, recs []jobstore.Attachment, runner, projectKey string, oneLine bool) (string, error) {
	placed, err := s.placeImages(callerID, recs, runner, projectKey)
	if err != nil {
		return "", err
	}
	// A terminal session takes the message line by line (every newline is an Enter, i.e. a
	// separate submission), so its image paths ride on ONE line after the text.
	sep := "\n"
	if oneLine {
		sep = " "
	}
	var b strings.Builder
	for i, p := range placed {
		if p.abs != "" {
			fmt.Fprintf(&b, "%s[附图 %d] %s", sep, i+1, p.abs)
		} else {
			// A worker below protocol v25 cannot report where it wrote the file.
			fmt.Fprintf(&b, "%s[附图 %d] %s（相对于项目 %s 根目录）", sep, i+1, p.dest, projectKey)
		}
	}
	return b.String(), nil
}

// readImages loads the original bytes for an inline send.
func (s *Service) readImages(recs []jobstore.Attachment) ([]job.ImageInput, error) {
	out := make([]job.ImageInput, 0, len(recs))
	for _, rec := range recs {
		data, err := s.att.Files.Read(rec)
		if err != nil {
			return nil, fmt.Errorf("%w: attachment %s: %v", ErrAttachmentDelivery, rec.ID, err)
		}
		out = append(out, job.ImageInput{Mime: rec.Mime, Data: data})
	}
	return out, nil
}

// PromptRef is the attachment ref of the prompt number n (1-based) of job id: the key the
// ACP history joins on (the n-th prompt event of the job's structured record).
func PromptRef(jobID string, n int) string { return fmt.Sprintf("%s#%d", jobID, n) }

// RefKindPrompt / RefKindDecision are the attachment ref kinds a send records.
const (
	RefKindPrompt   = "prompt"
	RefKindDecision = "decision"
	// RefKindMessage: a message to a terminal session (ref = its session outbox id).
	RefKindMessage = "message"
)

// rowTurnNo is the turn count of a resident-session row (session_state_json).
func rowTurnNo(rec jobstore.JobRecord) int {
	var state struct {
		TurnNo int `json:"turn_no"`
	}
	_ = json.Unmarshal([]byte(rec.SessionStateJSON), &state)
	return state.TurnNo
}

// imageSend describes one send of text plus images to a concrete target.
type imageSend struct {
	callerID string
	threadID string
	atts     []jobstore.Attachment
	in       RouteInput
	runner   string
	project  string
	// inline sends the blocks; nil when the target has no inline path.
	inline func(text string, images []job.ImageInput) error
	// text sends the final text (file delivery) and returns what to bind to.
	text func(text string) (refKind, refID string, err error)
	// paths sends the text with the placed image paths riding the next job's ARGV (arg
	// delivery, image_input: arg); nil when the target has no such path.
	paths func(text string, paths []string) (refKind, refID string, err error)
	// earlyKind / earlyRef, when set, is the ref known BEFORE sending (a say into a live session):
	// the attachments are bound first so the history shows them the moment the prompt is
	// echoed, and released again when the send fails.
	earlyKind, earlyRef string
	// oneLine puts the image paths on the message's own line (terminal sessions).
	oneLine bool
}

func (s *Service) deliverWithImages(m imageSend, body string) (refKind, refID string, err error) {
	route := RouteAttachments(m.in)
	if route.Mode == AttachNone {
		return "", "", fmt.Errorf("%w: %s", ErrAttachmentsUnsupported, route.Reason)
	}
	bound := false
	if m.earlyRef != "" {
		if err := s.bindAll(m.atts, m.threadID, m.earlyKind, m.earlyRef); err != nil {
			return "", "", err
		}
		bound = true
	}
	release := func() {
		if bound {
			s.unbindAll(m.atts, m.threadID)
		}
	}
	if route.Mode == AttachInline && m.inline != nil {
		images, err := s.readImages(m.atts)
		if err != nil {
			release()
			return "", "", err
		}
		err = m.inline(body, images)
		if err == nil {
			return m.earlyKind, m.earlyRef, nil
		}
		if !errors.Is(err, job.ErrImagesNotSupported) {
			release()
			return "", "", err
		}
		// The session turned out not to take image blocks: fall back to file delivery when
		// the agent can read by path, otherwise say why not.
		in := m.in
		in.ACPAccepts = false
		route = RouteAttachments(in)
		if route.Mode != AttachFile {
			release()
			return "", "", fmt.Errorf("%w: %s", ErrAttachmentsUnsupported, route.Reason)
		}
	}
	if route.Mode == AttachArg {
		if m.paths == nil {
			release()
			return "", "", fmt.Errorf("%w: %s 的附图还不能随这条消息送出", ErrAttachmentsUnsupported, m.in.Agent)
		}
		placed, err := s.placeImages(m.callerID, m.atts, m.runner, m.project)
		if err != nil {
			release()
			return "", "", err
		}
		paths := make([]string, 0, len(placed))
		for i, p := range placed {
			if p.abs == "" {
				// The argv needs an absolute path; a project-relative one would be resolved
				// against the agent's cwd, which is not where the image was placed.
				release()
				return "", "", fmt.Errorf("%w: 附图 %d 的绝对路径未知（执行机 worker 版本过旧，无法作为参数送出）", ErrAttachmentDelivery, i+1)
			}
			paths = append(paths, p.abs)
		}
		kind, ref, err := m.paths(body, paths)
		if err != nil {
			release()
			return "", "", err
		}
		if bound {
			return m.earlyKind, m.earlyRef, nil
		}
		return kind, ref, nil
	}
	section, err := s.placeFiles(m.callerID, m.atts, m.runner, m.project, m.oneLine)
	if err != nil {
		release()
		return "", "", err
	}
	joined := body + "\n" + section
	if m.oneLine {
		joined = body + section
	}
	kind, ref, err := m.text(strings.TrimSpace(joined))
	if err != nil {
		release()
		return "", "", err
	}
	if bound {
		return m.earlyKind, m.earlyRef, nil
	}
	return kind, ref, nil
}
