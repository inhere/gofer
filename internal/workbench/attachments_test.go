package workbench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/xfer"
)

func TestRouteAttachmentsTruthTable(t *testing.T) {
	cases := []struct {
		name   string
		in     RouteInput
		mode   AttachMode
		reason string
	}{
		{"pty job", RouteInput{Kind: KindAgent, Agent: "claude", Runner: "local", Project: "p", Pty: true, FilePath: true}, AttachNone, "pty 会话不支持附图"},
		{"pty beats acp", RouteInput{Pty: true, ACPAccepts: true}, AttachNone, "pty 会话不支持附图"},
		{"acp accepts images", RouteInput{Kind: KindAgent, Agent: "claude-acp", Runner: "local", Project: "p", ACPAccepts: true, FilePath: true}, AttachInline, ""},
		{"acp inline needs no executor info", RouteInput{Kind: KindAgent, Agent: "x", ACPAccepts: true}, AttachInline, ""},
		{"acp without image support, agent reads files", RouteInput{Kind: KindAgent, Agent: "claude-acp", Runner: "w1", Project: "p", FilePath: true}, AttachFile, ""},
		{"cli session job", RouteInput{Kind: KindAgent, Agent: "claude", Runner: "local", Project: "p", FilePath: true}, AttachFile, ""},
		{"terminal session", RouteInput{Kind: KindRelay, Agent: "codex", Runner: "w-docker-claude", Project: "p", FilePath: true}, AttachFile, ""},
		{"agent cannot read images", RouteInput{Kind: KindRelay, Agent: "suga", Runner: "local", Project: "p"}, AttachNone, "suga 暂不支持读取图片"},
		{"unnamed agent cannot", RouteInput{Runner: "local", Project: "p"}, AttachNone, "该 agent 暂不支持读取图片"},
		{"no runner", RouteInput{Agent: "claude", Project: "p", FilePath: true}, AttachNone, "会话没有执行机信息"},
		{"no project", RouteInput{Agent: "claude", Runner: "local", FilePath: true}, AttachNone, "会话没有执行机信息"},
	}
	for _, c := range cases {
		got := RouteAttachments(c.in)
		if got.Mode != c.mode || got.Reason != c.reason {
			t.Errorf("%s: got %+v, want mode=%s reason=%q", c.name, got, c.mode, c.reason)
		}
	}
}

// fakeFiles serves attachment originals from a directory.
type fakeFiles struct{ dir string }

func (f fakeFiles) AbsPath(rec jobstore.Attachment) (string, error) {
	return filepath.Join(f.dir, rec.ID), nil
}
func (f fakeFiles) Read(rec jobstore.Attachment) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.dir, rec.ID))
}

type imageJobs struct {
	recordingJobs
	accepts    bool
	blocksErr  error
	sayText    string
	sayImages  []job.ImageInput
	plainSays  []string
	blocksSays int
}

func (j *imageJobs) SaySession(_, text string) error {
	j.plainSays = append(j.plainSays, text)
	return nil
}
func (j *imageJobs) SessionAcceptsImages(string) (bool, error) { return j.accepts, nil }
func (j *imageJobs) SaySessionBlocks(_, text string, images []job.ImageInput) error {
	j.blocksSays++
	if j.blocksErr != nil {
		return j.blocksErr
	}
	j.sayText, j.sayImages = text, images
	return nil
}

type fakePlacer struct {
	abs   string
	err   error
	calls []string
}

func (p *fakePlacer) PutAndWait(_ context.Context, _, runner, project, _, dest string, _ time.Duration) (string, error) {
	p.calls = append(p.calls, runner+"|"+project+"|"+dest)
	return p.abs, p.err
}

type sessionRelay struct {
	recordingRelay
	session jobstore.AgentSession
}

func (r *sessionRelay) Session(string) (jobstore.AgentSession, error) { return r.session, nil }

type attFixture struct {
	store   *jobstore.Store
	svc     *Service
	jobs    *imageJobs
	relay   *sessionRelay
	root    string
	srcDir  string
	cfg     *config.Config
	maxPer  int
	nextAtt int
}

func newAttFixture(t *testing.T, placer Placer) *attFixture {
	t.Helper()
	f := &attFixture{store: openWorkbenchStore(t), jobs: &imageJobs{}, relay: &sessionRelay{}, root: t.TempDir(), srcDir: t.TempDir(), maxPer: 3}
	f.cfg = &config.Config{Projects: map[string]config.ProjectConfig{"alpha": {HostPath: f.root}}}
	f.svc = NewService(f.store, f.jobs, f.relay)
	f.svc.SetAttachments(AttachmentDeps{
		Store: f.store, Files: fakeFiles{dir: f.srcDir}, Xfer: placer, Jobs: f.jobs,
		Config: func() *config.Config { return f.cfg }, MaxPerMessage: func() int { return f.maxPer },
	})
	return f
}

// upload registers an unsent attachment of caller whose original is body.
func (f *attFixture) upload(t *testing.T, caller, body string) string {
	t.Helper()
	f.nextAtt++
	id := "att" + string(rune('a'+f.nextAtt)) + strings.Repeat("0", 16)
	assert.NoErr(t, os.WriteFile(filepath.Join(f.srcDir, id), []byte(body), 0o600))
	assert.NoErr(t, f.store.InsertAttachment(jobstore.Attachment{
		ID: id, Caller: caller, Mime: "image/png", Bytes: int64(len(body)), Width: 4, Height: 3, Path: "20261019/" + id,
	}))
	return id
}

func (f *attFixture) put(t *testing.T, rec jobstore.JobRecord) {
	t.Helper()
	if rec.StartedAt == 0 {
		rec.StartedAt = 100
	}
	if rec.UpdatedAt == 0 {
		rec.UpdatedAt = 110
	}
	putWorkbenchJob(t, f.store, rec, rec.ID, "first")
}

func (f *attFixture) bound(t *testing.T, id string) jobstore.Attachment {
	t.Helper()
	rec, ok, err := f.store.GetAttachment(id)
	assert.NoErr(t, err)
	assert.True(t, ok)
	return rec
}

func residentRow() jobstore.JobRecord {
	return jobstore.JobRecord{
		ID: "resident", ProjectKey: "alpha", Agent: "claude-acp", Runner: "local", Cwd: ".",
		SessionID: "sid-1", SessionStateJSON: `{"session":true,"turn_no":2}`, Status: job.StatusAwaitingInput,
	}
}

func TestTurnInlineSendsImageBlocks(t *testing.T) {
	f := newAttFixture(t, &fakePlacer{})
	f.jobs.accepts = true
	f.put(t, residentRow())
	id := f.upload(t, "alice", "PNGDATA")

	res, err := f.svc.TurnWithAttachments("alice", "s:sid-1", "look", []string{id})
	assert.NoErr(t, err)
	assert.Eq(t, "resident", res.JobID)
	assert.Eq(t, "look", f.jobs.sayText)
	assert.Len(t, f.jobs.sayImages, 1)
	assert.Eq(t, "image/png", f.jobs.sayImages[0].Mime)
	assert.Eq(t, "PNGDATA", string(f.jobs.sayImages[0].Data))
	assert.Len(t, f.jobs.plainSays, 0)
	rec := f.bound(t, id)
	assert.Eq(t, "s:sid-1", rec.ThreadID)
	assert.Eq(t, RefKindPrompt, rec.RefKind)
	assert.Eq(t, "resident#3", rec.RefID) // turn_no 2 + 1
}

func TestTurnInlineFallsBackToFileWhenNotSupported(t *testing.T) {
	placer := &fakePlacer{abs: "/exec/alpha/tmp/gofer-attachments/20261019/x.png"}
	f := newAttFixture(t, placer)
	f.jobs.accepts = true
	f.jobs.blocksErr = job.ErrImagesNotSupported
	f.put(t, residentRow())
	id := f.upload(t, "alice", "PNGDATA")

	_, err := f.svc.TurnWithAttachments("alice", "s:sid-1", "look", []string{id})
	assert.NoErr(t, err)
	assert.Len(t, f.jobs.plainSays, 1)
	assert.Eq(t, "look\n\n[附图 1] /exec/alpha/tmp/gofer-attachments/20261019/x.png", f.jobs.plainSays[0])
	assert.Eq(t, "resident#3", f.bound(t, id).RefID)
}

func TestTurnFileDeliveryLocalRunnerWritesRealFile(t *testing.T) {
	f := newAttFixture(t, nil)
	mgr, err := xfer.NewManager(xfer.Options{Root: t.TempDir(), Repo: f.store})
	assert.NoErr(t, err)
	mgr.SetRunner(&xfer.LocalRunner{Config: func() *config.Config { return f.cfg }, Store: mgr.Store(), OnGetContent: mgr.CommitGet})
	f.svc.att.Xfer = mgr
	// An ended session: the turn continues it with a new job (file delivery only).
	f.put(t, jobstore.JobRecord{ID: "src", ProjectKey: "alpha", Agent: "claude", Runner: "local", Cwd: ".",
		SessionID: "sid-2", Status: job.StatusDone, EndedAt: 110})
	id := f.upload(t, "alice", "PNGDATA")

	res, err := f.svc.TurnWithAttachments("alice", "s:sid-2", "see this", []string{id})
	assert.NoErr(t, err)
	assert.Eq(t, "continued-job", res.JobID)
	prompt := f.jobs.prompt
	assert.True(t, strings.HasPrefix(prompt, "see this\n\n[附图 1] "), prompt)
	abs := strings.TrimPrefix(prompt, "see this\n\n[附图 1] ")
	assert.True(t, filepath.IsAbs(abs), abs)
	assert.True(t, strings.HasSuffix(abs, id+".png"), abs)
	assert.True(t, strings.Contains(filepath.ToSlash(abs), "/gofer-attachments/"+time.Now().Format("20060102")+"/"), abs)
	data, err := os.ReadFile(abs)
	assert.NoErr(t, err)
	assert.Eq(t, "PNGDATA", string(data))
	rec := f.bound(t, id)
	assert.Eq(t, "s:sid-2", rec.ThreadID)
	assert.Eq(t, "continued-job#1", rec.RefID)
}

func TestTurnPlacementFailureSendsNothing(t *testing.T) {
	f := newAttFixture(t, &fakePlacer{err: errors.New("worker offline")})
	f.put(t, jobstore.JobRecord{ID: "src", ProjectKey: "alpha", Agent: "claude", Runner: "w1", Cwd: ".",
		SessionID: "sid-3", Status: job.StatusDone, EndedAt: 110})
	id := f.upload(t, "alice", "PNGDATA")

	_, err := f.svc.TurnWithAttachments("alice", "s:sid-3", "see", []string{id})
	assert.True(t, errors.Is(err, ErrAttachmentDelivery), err)
	assert.True(t, strings.Contains(err.Error(), "worker offline"))
	assert.Eq(t, "", f.jobs.prompt) // ResumeJob never ran
	assert.Eq(t, "", f.bound(t, id).ThreadID)
}

func TestTurnOldWorkerFallsBackToRelativePath(t *testing.T) {
	placer := &fakePlacer{} // abs "" = a worker below protocol v25
	f := newAttFixture(t, placer)
	f.relay.session = jobstore.AgentSession{SessionID: "t1", Agent: "claude", Runner: "w-old", ProjectKey: "alpha", Cwd: "/x"}
	id := f.upload(t, "alice", "PNGDATA")

	res, err := f.svc.TurnWithAttachments("alice", "r:t1", "look", []string{id})
	assert.NoErr(t, err)
	assert.Eq(t, "answered-decision", res.DecisionID)
	want := "look [附图 1] tmp/gofer-attachments/" + time.Now().Format("20060102") + "/" + id + ".png（相对于项目 alpha 根目录）"
	assert.Eq(t, want, f.relay.answer)
	assert.Eq(t, []string{"w-old|alpha|tmp/gofer-attachments/" + time.Now().Format("20060102") + "/" + id + ".png"}, placer.calls)
	rec := f.bound(t, id)
	assert.Eq(t, "r:t1", rec.ThreadID)
	assert.Eq(t, RefKindDecision, rec.RefKind)
	assert.Eq(t, "answered-decision", rec.RefID)
}

func TestTurnAttachmentChecks(t *testing.T) {
	f := newAttFixture(t, &fakePlacer{abs: "/a/b.png"})
	f.relay.session = jobstore.AgentSession{SessionID: "t1", Agent: "claude", Runner: "local", ProjectKey: "alpha"}
	mine := f.upload(t, "alice", "x")
	theirs := f.upload(t, "bob", "x")

	_, err := f.svc.TurnWithAttachments("alice", "r:t1", "hi", []string{theirs})
	assert.True(t, errors.Is(err, ErrAttachmentForbidden), err)
	_, err = f.svc.TurnWithAttachments("alice", "r:t1", "hi", []string{"nosuchid"})
	assert.True(t, errors.Is(err, ErrAttachmentForbidden), err)

	f.maxPer = 1
	_, err = f.svc.TurnWithAttachments("alice", "r:t1", "hi", []string{mine, theirs})
	assert.True(t, errors.Is(err, ErrTooManyAttachments), err)
	f.maxPer = 3
	_, err = f.svc.TurnWithAttachments("alice", "r:t1", "hi", []string{mine, mine})
	assert.True(t, errors.Is(err, job.ErrInvalidRequest), err)

	_, err = f.svc.TurnWithAttachments("alice", "r:t1", "hi", []string{mine})
	assert.NoErr(t, err)
	// The same attachment cannot be sent twice.
	_, err = f.svc.TurnWithAttachments("alice", "r:t1", "again", []string{mine})
	assert.True(t, errors.Is(err, ErrAttachmentSent), err)

	// An expired one is refused like a sent one.
	old := f.upload(t, "alice", "x")
	assert.NoErr(t, f.store.InsertAttachment(jobstore.Attachment{ID: "expired0000000000000", Caller: "alice", Mime: "image/png", Path: "x/y", ExpiresAt: 5}))
	_, err = f.svc.TurnWithAttachments("alice", "r:t1", "hi", []string{"expired0000000000000"})
	assert.True(t, errors.Is(err, ErrAttachmentSent), err)
	_ = old

	// Images only (no text) are a valid message.
	empty := f.upload(t, "alice", "x")
	_, err = f.svc.TurnWithAttachments("alice", "r:t1", "", []string{empty})
	assert.NoErr(t, err)
	_, err = f.svc.TurnWithAttachments("alice", "r:t1", "", nil)
	assert.True(t, errors.Is(err, ErrEmptyTurn))
}

func TestTurnUnsupportedReceiverSendsNothing(t *testing.T) {
	placer := &fakePlacer{abs: "/a/b.png"}
	f := newAttFixture(t, placer)
	f.relay.session = jobstore.AgentSession{SessionID: "t2", Agent: "suga", Runner: "local", ProjectKey: "alpha"}
	f.put(t, jobstore.JobRecord{ID: "pty", ProjectKey: "alpha", Agent: "claude", Runner: "local", Cwd: ".",
		SessionID: "sid-pty", Interactive: true, Status: job.StatusDone, EndedAt: 110})
	id := f.upload(t, "alice", "x")

	_, err := f.svc.TurnWithAttachments("alice", "r:t2", "hi", []string{id})
	assert.True(t, errors.Is(err, ErrAttachmentsUnsupported), err)
	assert.True(t, strings.Contains(err.Error(), "suga 暂不支持读取图片"))
	assert.Eq(t, "", f.relay.answer)

	_, err = f.svc.TurnWithAttachments("alice", "s:sid-pty", "hi", []string{id})
	assert.True(t, errors.Is(err, ErrAttachmentsUnsupported), err)
	assert.True(t, strings.Contains(err.Error(), "pty 会话不支持附图"))
	assert.Eq(t, "", f.jobs.prompt)
	assert.Len(t, placer.calls, 0)
	assert.Eq(t, "", f.bound(t, id).ThreadID)
}

func TestListReportsAttachmentCapability(t *testing.T) {
	f := newAttFixture(t, &fakePlacer{})
	f.jobs.accepts = true
	f.put(t, residentRow()) // live ACP accepting images -> inline
	f.put(t, jobstore.JobRecord{ID: "cli", ProjectKey: "alpha", Agent: "claude", Runner: "local", Cwd: ".",
		SessionID: "sid-cli", Interactive: false, SessionStateJSON: `{"session":true}`, Status: job.StatusDone, EndedAt: 120, StartedAt: 100, UpdatedAt: 120})
	f.put(t, jobstore.JobRecord{ID: "suga", ProjectKey: "alpha", Agent: "suga-acp", Runner: "local", Cwd: ".",
		SessionID: "sid-suga", SessionStateJSON: `{"session":true}`, Status: job.StatusDone, EndedAt: 120, StartedAt: 100, UpdatedAt: 120})
	got, err := f.svc.List("alice", Query{Since: 1})
	assert.NoErr(t, err)
	modes := map[string]Thread{}
	for _, g := range got.Projects {
		for _, th := range g.Threads {
			modes[th.ID] = th
		}
	}
	// The "cli" and "suga" jobs are not live, so SessionAcceptsImages is asked per latest job id:
	// the fake answers true for all, so only the per-agent file capability separates them.
	assert.Eq(t, AttachInline, modes["s:sid-1"].Attachments)
	f.jobs.accepts = false
	got, err = f.svc.List("alice", Query{Since: 1})
	assert.NoErr(t, err)
	for _, g := range got.Projects {
		for _, th := range g.Threads {
			modes[th.ID] = th
		}
	}
	assert.Eq(t, AttachFile, modes["s:sid-1"].Attachments)
	assert.Eq(t, AttachFile, modes["s:sid-cli"].Attachments)
	assert.Eq(t, AttachNone, modes["s:sid-suga"].Attachments)
	assert.Eq(t, "suga-acp 暂不支持读取图片", modes["s:sid-suga"].AttachmentsReason)

	// Not wired: the bit is simply absent.
	bare := NewService(f.store, f.jobs, f.relay)
	got, err = bare.List("alice", Query{Since: 1})
	assert.NoErr(t, err)
	assert.Eq(t, AttachMode(""), got.Projects[0].Threads[0].Attachments)
}
