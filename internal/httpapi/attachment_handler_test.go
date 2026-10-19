package httpapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/attachment"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// P1 of the workbench image attachments: upload, read, delete and the permission rules around
// them (design 2026-10-19-workbench-image-attachments-design.md).

const (
	attTokAlice = "tok-att-alice"
	attTokBob   = "tok-att-bob"
	attTokCarol = "tok-att-carol"
)

// attFixture is a server with three plain callers and an attachment store whose limits the
// test can change.
type attFixture struct {
	s   *Server
	svc *attachment.Service
	lim *attachment.Limits
}

func newAttFixture(t *testing.T) *attFixture {
	t.Helper()
	s := newTestServerCfgWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: attTokAlice}, {ID: "bob", Token: attTokBob}, {ID: "carol", Token: attTokCarol},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}, nil)
	lim := &attachment.Limits{MaxBytes: 1 << 20, HourlyPerCaller: 100, Retention: 24 * time.Hour}
	svc := attachment.New(t.TempDir(), s.jobs.Meta(), func() attachment.Limits { return *lim })
	s.SetAttachments(svc)
	s.setDBGrants(nil)
	return &attFixture{s: s, svc: svc, lim: lim}
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 7, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// attUpload posts a multipart upload (field `file`, the given file name and declared type).
func (f *attFixture) upload(t *testing.T, token, field, filename, ctype string, data []byte) (int, map[string]any, *http.Response) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
	h.Set("Content-Type", ctype)
	fw, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	return resp.StatusCode, body, resp
}

func (f *attFixture) uploadPNG(t *testing.T, token string) string {
	t.Helper()
	status, body, _ := f.upload(t, token, "file", "shot.png", "image/png", testPNG(t, 40, 30))
	if status != http.StatusCreated {
		t.Fatalf("upload = %d %v", status, body)
	}
	return body["id"].(string)
}

func (f *attFixture) req(t *testing.T, token, method, path string) (*http.Response, []byte) {
	t.Helper()
	resp := do(t, f.s, method, path, token, nil)
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestAttachmentUploadAcceptsImagesByMagicBytes(t *testing.T) {
	f := newAttFixture(t)
	data := testPNG(t, 40, 30)

	// A PNG named .jpg with a lying Content-Type is still a PNG.
	status, body, _ := f.upload(t, attTokAlice, "file", "photo.jpg", "image/jpeg", data)
	if status != http.StatusCreated {
		t.Fatalf("upload = %d %v", status, body)
	}
	if body["mime"] != "image/png" || body["width"] != float64(40) || body["height"] != float64(30) ||
		body["bytes"] != float64(len(data)) || body["id"] == "" || body["expires_at"] == nil {
		t.Fatalf("response = %v", body)
	}

	for name, tc := range map[string]struct {
		file, ctype string
		data        []byte
	}{
		"svg":   {"x.png", "image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		"html":  {"x.png", "image/png", []byte("<html><script>alert(1)</script></html>")},
		"plain": {"x.png", "image/png", []byte("just some text")},
		"empty": {"x.png", "image/png", nil},
	} {
		status, body, _ := f.upload(t, attTokAlice, "file", tc.file, tc.ctype, tc.data)
		if status != http.StatusUnsupportedMediaType {
			t.Errorf("%s: upload = %d %v, want 415", name, status, body)
		}
	}

	if status, _, _ := f.upload(t, attTokAlice, "other", "x.png", "image/png", data); status != http.StatusBadRequest {
		t.Errorf("wrong field name = %d, want 400", status)
	}
	if status, _, _ := f.upload(t, "", "file", "x.png", "image/png", data); status != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", status)
	}
}

func TestAttachmentUploadTooLarge(t *testing.T) {
	f := newAttFixture(t)
	f.lim.MaxBytes = 2048
	big := append(testPNG(t, 8, 8), bytes.Repeat([]byte{0}, 4096)...)
	// A declared content length beyond the cap is refused before the body is read.
	if status, body, _ := f.upload(t, attTokAlice, "file", "a.png", "image/png", append(big, bytes.Repeat([]byte{0}, 80<<10)...)); status != http.StatusRequestEntityTooLarge {
		t.Errorf("early size check = %d %v, want 413", status, body)
	}
	// Inside the envelope allowance the service's own limit applies.
	if status, body, _ := f.upload(t, attTokAlice, "file", "a.png", "image/png", big); status != http.StatusRequestEntityTooLarge {
		t.Errorf("service size check = %d %v, want 413", status, body)
	}
	if status, _, _ := f.upload(t, attTokAlice, "file", "a.png", "image/png", testPNG(t, 8, 8)); status != http.StatusCreated {
		t.Errorf("a small image = %d, want 201", status)
	}
}

func TestAttachmentUploadStreamedBodyOverTheCap(t *testing.T) {
	f := newAttFixture(t)
	f.lim.MaxBytes = 2048
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.png")
	_, _ = fw.Write(append(testPNG(t, 8, 8), bytes.Repeat([]byte{0}, 200<<10)...))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", &buf)
	req.ContentLength = -1 // chunked: no early length check, the body reader enforces the cap
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+attTokAlice)
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversize upload = %d %s, want 413", rec.Code, rec.Body.String())
	}
}

func TestAttachmentHourlyLimit(t *testing.T) {
	f := newAttFixture(t)
	f.lim.HourlyPerCaller = 2
	f.uploadPNG(t, attTokAlice)
	f.uploadPNG(t, attTokAlice)
	status, body, resp := f.upload(t, attTokAlice, "file", "a.png", "image/png", testPNG(t, 8, 8))
	if status != http.StatusTooManyRequests {
		t.Fatalf("third upload = %d %v, want 429", status, body)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	// The budget is per caller.
	f.uploadPNG(t, attTokBob)
}

func TestAttachmentUnsentIsTheUploadersAlone(t *testing.T) {
	f := newAttFixture(t)
	id := f.uploadPNG(t, attTokAlice)

	resp, body := f.req(t, attTokAlice, http.MethodGet, "/v1/attachments/"+id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("uploader read = %d %s", resp.StatusCode, body)
	}
	for k, want := range map[string]string{
		"Content-Type": "image/png", "Content-Disposition": "inline", "X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.HasPrefix(cc, "private") || !strings.Contains(cc, "max-age=") {
		t.Errorf("Cache-Control = %q", cc)
	}
	if !bytes.Equal(body, testPNG(t, 40, 30)) {
		t.Error("original bytes differ")
	}

	// Even a caller holding every read permission cannot see an unsent file.
	s := f.s
	s.setDBGrants(grantsFor(allPermissionNames()))
	for _, tok := range []string{attTokBob, attTokCarol} {
		for _, path := range []string{"/v1/attachments/" + id, "/v1/attachments/" + id + "?thumb=1"} {
			if resp, body := f.req(t, tok, http.MethodGet, path); resp.StatusCode != http.StatusNotFound {
				t.Errorf("other caller %s = %d %s, want 404", path, resp.StatusCode, body)
			}
		}
	}
	if resp, _ := f.req(t, attTokAlice, http.MethodGet, "/v1/attachments/unknown-id-0000000000"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", resp.StatusCode)
	}
	if resp, _ := f.req(t, attTokAlice, http.MethodGet, "/v1/attachments/..%2F..%2Fetc%2Fpasswd"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("traversal id = %d, want 404", resp.StatusCode)
	}
}

func TestAttachmentThumbnail(t *testing.T) {
	f := newAttFixture(t)
	status, body, _ := f.upload(t, attTokAlice, "file", "wide.png", "image/png", testPNG(t, 900, 450))
	if status != http.StatusCreated {
		t.Fatalf("upload = %d %v", status, body)
	}
	id := body["id"].(string)
	resp, data := f.req(t, attTokAlice, http.MethodGet, "/v1/attachments/"+id+"?thumb=1")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("thumb = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 320 || cfg.Height != 160 {
		t.Fatalf("thumb size = %+v err=%v, want 320x160", cfg, err)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Disposition") != "inline" {
		t.Error("thumbnail lacks the hardening headers")
	}
}

// attBindToJob inserts a job owned by `owner` and binds the attachment to its thread.
func (f *attFixture) bindToJob(t *testing.T, attID, jobID, owner string) string {
	t.Helper()
	now := time.Now().Unix()
	if err := f.s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: jobID, ProjectKey: "self", Agent: "exec", Runner: "local", Status: job.StatusDone,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now, CallerID: owner,
	}); err != nil {
		t.Fatal(err)
	}
	thread := "j:" + jobID
	if err := f.s.jobs.Meta().BindAttachment(attID, thread, "job", jobID); err != nil {
		t.Fatal(err)
	}
	return thread
}

func TestAttachmentSentFollowsThreadVisibility(t *testing.T) {
	f := newAttFixture(t)
	id := f.uploadPNG(t, attTokAlice)
	f.bindToJob(t, id, "att-job-1", "bob") // alice uploaded, bob owns the thread

	get := func(tok string) int {
		resp, _ := f.req(t, tok, http.MethodGet, "/v1/attachments/"+id)
		return resp.StatusCode
	}
	if got := get(attTokAlice); got != http.StatusOK {
		t.Errorf("uploader after send = %d, want 200", got)
	}
	if got := get(attTokBob); got != http.StatusOK {
		t.Errorf("owner of the thread = %d, want 200", got)
	}
	if got := get(attTokCarol); got != http.StatusNotFound {
		t.Errorf("unrelated caller without job.read.others = %d, want 404", got)
	}
	f.s.setDBGrants(grantsFor([]string{authz.PermJobReadOthers}))
	if got := get(attTokCarol); got != http.StatusOK {
		t.Errorf("caller with job.read.others = %d, want 200", got)
	}
	// A session-read permission is the wrong kind for a job thread.
	f.s.setDBGrants(grantsFor([]string{authz.PermSessionReadOthers}))
	if got := get(attTokCarol); got != http.StatusNotFound {
		t.Errorf("caller with only session.read.others = %d, want 404", got)
	}
}

func TestAttachmentSentFailsClosedOnUnknownThread(t *testing.T) {
	f := newAttFixture(t)
	f.s.setDBGrants(grantsFor(allPermissionNames()))
	id := f.uploadPNG(t, attTokAlice)
	if err := f.s.jobs.Meta().BindAttachment(id, "j:no-such-job", "job", "no-such-job"); err != nil {
		t.Fatal(err)
	}
	if resp, _ := f.req(t, attTokBob, http.MethodGet, "/v1/attachments/"+id); resp.StatusCode != http.StatusNotFound {
		t.Errorf("thread that does not resolve = %d, want 404 (fail closed)", resp.StatusCode)
	}
	id2 := f.uploadPNG(t, attTokAlice)
	if err := f.s.jobs.Meta().BindAttachment(id2, "garbage", "job", "x"); err != nil {
		t.Fatal(err)
	}
	if resp, _ := f.req(t, attTokBob, http.MethodGet, "/v1/attachments/"+id2); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unparseable thread = %d, want 404", resp.StatusCode)
	}
}

type denyAllAccess struct{ asked []string }

func (d *denyAllAccess) MayReadThread(_ *rux.Context, threadID string) bool {
	d.asked = append(d.asked, threadID)
	return false
}

func TestAttachmentThreadAccessHookOverridesDefault(t *testing.T) {
	f := newAttFixture(t)
	id := f.uploadPNG(t, attTokAlice)
	thread := f.bindToJob(t, id, "att-job-2", "bob")
	hook := &denyAllAccess{}
	f.s.SetAttachmentThreadAccess(hook)
	// bob owns the thread, so the default would allow him; the hook says no.
	if resp, _ := f.req(t, attTokBob, http.MethodGet, "/v1/attachments/"+id); resp.StatusCode != http.StatusNotFound {
		t.Errorf("hook denial = %d, want 404", resp.StatusCode)
	}
	if len(hook.asked) != 1 || hook.asked[0] != thread {
		t.Errorf("hook asked %v, want [%s]", hook.asked, thread)
	}
	// The uploader never needs the hook.
	if resp, _ := f.req(t, attTokAlice, http.MethodGet, "/v1/attachments/"+id); resp.StatusCode != http.StatusOK {
		t.Errorf("uploader = %d, want 200", resp.StatusCode)
	}
}

func TestAttachmentDeleteRules(t *testing.T) {
	f := newAttFixture(t)
	id := f.uploadPNG(t, attTokAlice)
	rel := filepath.Join(f.svc.Root(), time.Now().UTC().Format("20060102"), id)
	if _, err := os.Stat(rel); err != nil {
		t.Fatalf("stored file missing: %v", err)
	}

	// Somebody else's unsent attachment looks like a missing one.
	if resp, _ := f.req(t, attTokBob, http.MethodDelete, "/v1/attachments/"+id); resp.StatusCode != http.StatusNotFound {
		t.Errorf("other caller delete = %d, want 404", resp.StatusCode)
	}
	if resp, _ := f.req(t, attTokAlice, http.MethodDelete, "/v1/attachments/"+id); resp.StatusCode != http.StatusOK {
		t.Fatalf("uploader delete = %d, want 200", resp.StatusCode)
	}
	if _, err := os.Stat(rel); !os.IsNotExist(err) {
		t.Errorf("file still on disk after delete: %v", err)
	}
	if resp, _ := f.req(t, attTokAlice, http.MethodGet, "/v1/attachments/"+id); resp.StatusCode != http.StatusNotFound {
		t.Errorf("read after delete = %d, want 404", resp.StatusCode)
	}
	if resp, _ := f.req(t, attTokAlice, http.MethodDelete, "/v1/attachments/"+id); resp.StatusCode != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", resp.StatusCode)
	}

	// A sent attachment cannot be deleted, not even by its uploader (409), and survives.
	sent := f.uploadPNG(t, attTokAlice)
	f.bindToJob(t, sent, "att-job-3", "alice")
	if resp, _ := f.req(t, attTokAlice, http.MethodDelete, "/v1/attachments/"+sent); resp.StatusCode != http.StatusConflict {
		t.Errorf("delete of a sent attachment = %d, want 409", resp.StatusCode)
	}
	if resp, _ := f.req(t, attTokAlice, http.MethodGet, "/v1/attachments/"+sent); resp.StatusCode != http.StatusOK {
		t.Errorf("sent attachment gone after the refused delete: %d", resp.StatusCode)
	}
}

func TestAttachmentRoutesUnwired(t *testing.T) {
	f := newAttFixture(t)
	f.s.SetAttachments(nil)
	if resp, _ := f.req(t, attTokAlice, http.MethodGet, "/v1/attachments/anything-0000000000000"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unwired GET = %d, want 503", resp.StatusCode)
	}
	if status, _, _ := f.upload(t, attTokAlice, "file", "a.png", "image/png", testPNG(t, 4, 4)); status != http.StatusServiceUnavailable {
		t.Errorf("unwired upload = %d, want 503", status)
	}
}

// A job credential belongs to an agent, not to a person: it can neither upload nor read nor
// delete attachments, whatever its owner may do.
func TestAttachmentRefusesJobCredentials(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	in := seedCeilingWorld(t, s, "p1")
	tok := seedCredentialJob(t, s, "att-cred-job", "p1", "lim")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/attachments/" + in.attachmentID},
		{http.MethodDelete, "/v1/attachments/" + in.attachmentID},
	} {
		status, resp := ceilReq(t, s, tok, tc.method, tc.path, nil)
		if status != http.StatusForbidden {
			t.Errorf("%s %s with a job credential = %d (%s), want 403", tc.method, tc.path, status, resp)
		}
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.png")
	_, _ = fw.Write(testPNG(t, 4, 4))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("upload with a job credential = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// Ceilinged callers (project-restricted, even read-only) can upload their own files; what they
// can READ of a sent attachment is bounded by the project of its thread.
func TestAttachmentCeilingedCallers(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")

	for _, tok := range []string{ceilTokLim, ceilTokRO} {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "a.png")
		_, _ = fw.Write(testPNG(t, 4, 4))
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/v1/attachments", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Errorf("ceilinged upload (%s) = %d %s, want 201", tok, rec.Code, rec.Body.String())
		}
	}

	// Out-of-project sent attachment: the ceiling answers 404 before the handler runs.
	status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/attachments/"+out.attachmentID, nil)
	if status != http.StatusNotFound || !strings.Contains(body, `"error":"not found"`) {
		t.Errorf("attachment of another project = %d %s, want the ceiling 404", status, body)
	}
	// In-project: past the ceiling (the file itself is not on disk in this fixture).
	status, body = ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/attachments/"+in.attachmentID, nil)
	if strings.Contains(body, `"error":"not found"`) || strings.Contains(body, "caller ceiling") {
		t.Errorf("attachment of the own project stopped by the ceiling: %d %s", status, body)
	}
}

func TestAttachmentRoutesAreDeclared(t *testing.T) {
	for key, want := range map[string]string{
		"POST /v1/attachments":        "NoProject",
		"GET /v1/attachments/{id}":    "Resource(attachment)",
		"DELETE /v1/attachments/{id}": "Resource(attachment)",
	} {
		decl, ok := routeDecls[key]
		if !ok {
			t.Errorf("%s is not declared", key)
			continue
		}
		if decl.user.kind != userPublic {
			t.Errorf("%s: user category %v, want Public (the handler restricts to the owner)", key, decl.user.kind)
		}
		if decl.scope.String() != want {
			t.Errorf("%s: scope %s, want %s", key, decl.scope, want)
		}
		if decl.jobCred != "" || decl.workerSelf {
			t.Errorf("%s must not be reachable by a job credential or a worker", key)
		}
	}
	if _, ok := resourceKinds["attachment"]; !ok {
		t.Error("no resolver for Resource(attachment)")
	}
}
