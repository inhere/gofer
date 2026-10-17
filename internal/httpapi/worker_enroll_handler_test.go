package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/workerenroll"
)

const (
	enrollAdminTok = "tok-root"
	enrollSecret1  = "enroll-secret-one"
)

// enrollEnv is a Server with a real enrollment Service over its own store; the
// registrar and config source are in-memory fakes (the "config" is a token map).
type enrollEnv struct {
	t       *testing.T
	s       *Server
	store   *jobstore.Store
	mu      sync.Mutex
	enabled bool
	tokens  map[string]string
}

func (e *enrollEnv) Enrollment() workerenroll.Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return workerenroll.Settings{Enabled: e.enabled, DecideSec: 120, CollectSec: 60, MaxPending: 20}
}

func (e *enrollEnv) WorkerToken(id string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	tok, ok := e.tokens[id]
	return tok, ok
}

func (e *enrollEnv) Register(id string, _, _ []string, token string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.tokens[id]; ok {
		return config.ErrWorkerAlreadyRegistered
	}
	e.tokens[id] = token
	return nil
}

func (e *enrollEnv) Remove(id, fp string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tok, ok := e.tokens[id]; ok && config.WorkerTokenFingerprint(tok) == fp {
		delete(e.tokens, id)
		return true, nil
	}
	return false, nil
}

func (e *enrollEnv) Disconnect(string) {}

func (e *enrollEnv) Record(kind, enrollID, actor string) {
	_, _ = e.store.AppendAuditEvent(kind, enrollID, actor, "{}")
}

func newEnrollEnv(t *testing.T, enabled bool, trusted ...string) *enrollEnv {
	t.Helper()
	sc := adminGroupConfig(false)
	sc.WorkerEnrollment.TrustedProxies = trusted
	s := newTestServerCfg(t, sc)
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "enroll.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &enrollEnv{t: t, s: s, store: st, enabled: enabled, tokens: map[string]string{}}
	lim := workerenroll.NewSourceLimiter(func() int { return 3 }, nil)
	svc, err := workerenroll.New(workerenroll.Options{Store: st, Registrar: e, Audit: e, Config: e, Limiter: lim, Blocker: lim})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	s.SetWorkerEnrollment(svc)
	return e
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type enrollReq struct {
	method, path, remote, token, secret string
	xff                                 string
	body                                any
}

func (e *enrollEnv) do(r enrollReq) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.doCtx(context.Background(), r)
}

func (e *enrollEnv) doCtx(ctx context.Context, r enrollReq) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *bytes.Reader
	switch b := r.body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(r.method, r.path, rd).WithContext(ctx)
	if r.remote != "" {
		req.RemoteAddr = r.remote
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.secret != "" {
		req.Header.Set(enrollSecretHeader, r.secret)
	}
	if r.xff != "" {
		req.Header.Set("X-Forwarded-For", r.xff)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

func validEnrollBody() map[string]any {
	return map[string]any{
		"requested_id": "gpu1", "labels": []string{"linux"},
		"host":        map[string]any{"hostname": "box-1", "os": "linux", "arch": "amd64", "agents": []string{"claude"}, "roots": []string{"/srv/work"}},
		"secret_hash": sha256Hex(enrollSecret1),
	}
}

func (e *enrollEnv) request(remote string) (id, code string) {
	e.t.Helper()
	rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", remote: remote, body: validEnrollBody()})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("enroll = %d: %s", rec.Code, rec.Body.String())
	}
	var out enrollRequestResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out.EnrollID, out.PairingCode
}

func (e *enrollEnv) accept(id, code, worker string) *httptest.ResponseRecorder {
	return e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enrollments/" + id + "/accept", token: enrollAdminTok,
		body: map[string]any{"pairing_code": code, "worker_id": worker, "projects": []string{"self"}}})
}

func (e *enrollEnv) poll(id, secret string, waitSec int) *httptest.ResponseRecorder {
	path := "/v1/worker-enroll/" + id
	if waitSec > 0 {
		path += "?wait=" + string(rune('0'+waitSec))
	}
	return e.do(enrollReq{method: http.MethodGet, path: path, secret: secret})
}

func (e *enrollEnv) list() enrollListResp {
	e.t.Helper()
	rec := e.do(enrollReq{method: http.MethodGet, path: "/v1/worker-enrollments", token: enrollAdminTok})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var out enrollListResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func TestEnrollRoutesAreDeclared(t *testing.T) {
	registered := registeredRouteKeys(t, newRouteTableServer(t))
	anon := map[string]string{
		"POST /v1/worker-enroll":              "none",
		"GET /v1/worker-enroll/{id}":          "enroll secret",
		"POST /v1/worker-enroll/{id}/confirm": "enroll secret",
		"POST /v1/worker-enroll/{id}/cancel":  "enroll secret",
	}
	for key, ext := range anon {
		d, ok := routeDecls[key]
		if !ok || !registered[key] {
			t.Fatalf("%s declared=%v registered=%v", key, ok, registered[key])
		}
		if d.user.kind != userNone || d.external != ext {
			t.Errorf("%s: kind=%v external=%q, want NoUserSurface + %q", key, d.user.kind, d.external, ext)
		}
	}
	for _, key := range []string{"GET /v1/worker-enrollments", "POST /v1/worker-enrollments/{id}/accept", "POST /v1/worker-enrollments/{id}/reject"} {
		d, ok := routeDecls[key]
		if !ok || !registered[key] {
			t.Fatalf("%s declared=%v registered=%v", key, ok, registered[key])
		}
		if d.user.kind != userRequire || len(d.user.perms) != 1 || d.user.perms[0] != "worker.manage" {
			t.Errorf("%s: decl=%+v, want Require(worker.manage)", key, d.user)
		}
	}
}

// The feature switch is checked before anything reads the database: with the store
// closed, a disabled server still answers its documented 404.
func TestEnrollDisabledIs404WithoutDB(t *testing.T) {
	e := newEnrollEnv(t, false)
	_ = e.store.Close()
	want := `{"error":"not found","detail":"worker enrollment is disabled on this server"}`
	for _, r := range []enrollReq{
		{method: http.MethodPost, path: "/v1/worker-enroll", body: validEnrollBody()},
		{method: http.MethodGet, path: "/v1/worker-enroll/we_x?wait=1", secret: "s"},
		{method: http.MethodPost, path: "/v1/worker-enroll/we_x/confirm", secret: "s"},
		{method: http.MethodPost, path: "/v1/worker-enroll/we_x/cancel", secret: "s"},
	} {
		rec := e.do(r)
		if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != want {
			t.Errorf("%s %s = %d %q, want 404 %s", r.method, r.path, rec.Code, rec.Body.String(), want)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: Cache-Control=%q", r.method, r.path, rec.Header().Get("Cache-Control"))
		}
	}
	// Without a service at all the answer is the same.
	e.s.SetWorkerEnrollment(nil)
	if rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", body: validEnrollBody()}); rec.Code != http.StatusNotFound {
		t.Errorf("no service = %d", rec.Code)
	}
}

func TestEnrollListReportsEnabledFlag(t *testing.T) {
	e := newEnrollEnv(t, false)
	if l := e.list(); l.Enabled || l.PendingCount != 0 || l.Items == nil {
		t.Fatalf("disabled list = %+v", l)
	}
	e.enabled = true
	if l := e.list(); !l.Enabled {
		t.Fatalf("enabled list = %+v", l)
	}
}

func TestEnrollBadRequests(t *testing.T) {
	e := newEnrollEnv(t, true)
	mut := func(f func(m map[string]any)) map[string]any {
		m := validEnrollBody()
		f(m)
		return m
	}
	host := func(k string, v any) map[string]any {
		return mut(func(m map[string]any) { m["host"] = map[string]any{k: v} })
	}
	many := func(n int, s string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = s
		}
		return out
	}
	cases := map[string]any{
		"body over 8 KiB":      bytes.Repeat([]byte(" "), 9<<10),
		"not json":             []byte("{nope"),
		"trailing data":        []byte(`{"secret_hash":"` + sha256Hex("x") + `"} {}`),
		"unknown field":        mut(func(m map[string]any) { m["extra"] = 1 }),
		"short hash":           mut(func(m map[string]any) { m["secret_hash"] = "abc" }),
		"non-hex hash":         mut(func(m map[string]any) { m["secret_hash"] = strings.Repeat("z", 64) }),
		"reserved id":          mut(func(m map[string]any) { m["requested_id"] = "server" }),
		"bad id chars":         mut(func(m map[string]any) { m["requested_id"] = "bad id!" }),
		"too many labels":      mut(func(m map[string]any) { m["labels"] = many(9, "a") }),
		"long label":           mut(func(m map[string]any) { m["labels"] = []string{strings.Repeat("a", 33)} }),
		"label bad chars":      mut(func(m map[string]any) { m["labels"] = []string{"a b"} }),
		"hostname non-ascii":   host("hostname", "héllo"),
		"hostname control":     host("hostname", "a\x01b"),
		"hostname too long":    host("hostname", strings.Repeat("h", 65)),
		"too many agents":      host("agents", many(17, "a")),
		"agent bad chars":      host("agents", []string{"a/b"}),
		"too many roots":       host("roots", many(17, "/r")),
		"root too long":        host("roots", []string{strings.Repeat("r", 257)}),
		"version too long":     host("gofer_version", strings.Repeat("1", 65)),
		"protocol non-visible": host("protocol_version", "1\n2"),
	}
	for name, body := range cases {
		rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", body: body})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
	if l := e.list(); len(l.Items) != 0 {
		t.Fatalf("a rejected body created rows: %+v", l.Items)
	}
	// Root entries are cleaned of control characters rather than rejected.
	rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", body: host("roots", []string{"/a\x07b"})})
	if rec.Code != http.StatusCreated {
		t.Fatalf("control char in root = %d", rec.Code)
	}
	if got := e.list().Items[0].Host.Roots; len(got) != 1 || got[0] != "/ab" {
		t.Fatalf("roots=%v, want [/ab]", got)
	}
	if rec := e.poll("we_x", enrollSecret1, 0); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d", rec.Code)
	}
	if rec := e.do(enrollReq{method: http.MethodGet, path: "/v1/worker-enroll/we_x?wait=abc", secret: "s"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad wait = %d", rec.Code)
	}
}

func TestEnrollSourceAddress(t *testing.T) {
	pfx := func(s ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, v := range s {
			out = append(out, netip.MustParsePrefix(v))
		}
		return out
	}
	cases := []struct {
		name    string
		remote  string
		xff     []string
		trusted []netip.Prefix
		want    string
	}{
		{"no proxies: header ignored", "198.51.100.7:5555", []string{"203.0.113.9"}, nil, "198.51.100.7"},
		{"untrusted peer: header ignored", "198.51.100.7:5555", []string{"203.0.113.9"}, pfx("10.0.0.0/8"), "198.51.100.7"},
		{"trusted peer: right-most untrusted", "10.1.1.1:1", []string{"1.2.3.4, 9.9.9.9, 10.2.2.2"}, pfx("10.0.0.0/8"), "9.9.9.9"},
		{"trusted peer: several header lines", "10.1.1.1:1", []string{"1.2.3.4", "9.9.9.9,10.3.3.3"}, pfx("10.0.0.0/8"), "9.9.9.9"},
		{"trusted peer: spoofed left hops lose", "10.1.1.1:1", []string{"6.6.6.6, 7.7.7.7"}, pfx("10.0.0.0/8"), "7.7.7.7"},
		{"trusted peer, no header", "10.1.1.1:1", nil, pfx("10.0.0.0/8"), "10.1.1.1"},
		{"all hops trusted", "10.1.1.1:1", []string{"10.2.2.2"}, pfx("10.0.0.0/8"), "10.1.1.1"},
		{"garbled hop", "10.1.1.1:1", []string{"1.1.1.1, nonsense"}, pfx("10.0.0.0/8"), "10.1.1.1"},
		{"ipv4-mapped peer", "[::ffff:10.1.1.1]:1", []string{"9.9.9.9"}, pfx("10.0.0.0/8"), "9.9.9.9"},
		{"ipv6 peer", "[2001:db8::1]:80", []string{"9.9.9.9"}, nil, "2001:db8::1"},
	}
	for _, c := range cases {
		if got := enrollSourceOf(c.remote, c.xff, c.trusted); got != c.want {
			t.Errorf("%s: source=%q, want %q", c.name, got, c.want)
		}
	}
}

func TestEnrollSourceRecordedFromConnectionNotHeader(t *testing.T) {
	e := newEnrollEnv(t, true)
	rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", remote: "198.51.100.7:5555", xff: "203.0.113.9", body: validEnrollBody()})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if got := e.list().Items[0].Source; got != "198.51.100.7" {
		t.Fatalf("source=%q: a spoofed X-Forwarded-For must not be believed without trusted proxies", got)
	}

	p := newEnrollEnv(t, true, "10.0.0.0/8")
	rec = p.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", remote: "10.1.1.1:1", xff: "1.2.3.4, 9.9.9.9, 10.2.2.2", body: validEnrollBody()})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if got := p.list().Items[0].Source; got != "9.9.9.9" {
		t.Fatalf("source=%q, want the right-most untrusted hop 9.9.9.9", got)
	}
}

func TestEnrollUnknownIDAndWrongSecretAreIndistinguishable(t *testing.T) {
	e := newEnrollEnv(t, true)
	id, _ := e.request("198.51.100.1:1")
	unknown := e.poll("we_doesnotexist", enrollSecret1, 0)
	wrong := e.poll(id, "not-the-secret", 0)
	missing := e.do(enrollReq{method: http.MethodGet, path: "/v1/worker-enroll/" + id})
	confirmWrong := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll/" + id + "/confirm", secret: "nope"})
	cancelWrong := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll/" + id + "/cancel", secret: "nope"})
	for name, rec := range map[string]*httptest.ResponseRecorder{"wrong": wrong, "missing": missing, "confirm": confirmWrong, "cancel": cancelWrong} {
		if rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() {
			t.Errorf("%s: %d %q differs from unknown id %d %q", name, rec.Code, rec.Body.String(), unknown.Code, unknown.Body.String())
		}
	}
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d", unknown.Code)
	}
	// The right secret still works: a missing header did not count as a failure.
	if rec := e.poll(id, enrollSecret1, 0); rec.Code != http.StatusOK {
		t.Fatalf("right secret = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEnrollAdminRoutesRefuseNonPeopleAndMissingPermission(t *testing.T) {
	e := newEnrollEnv(t, true)
	id, code := e.request("198.51.100.1:1")
	jobToken := seedJobToken(t, e.s, "job-enroll", jobstore.JobCredentialMember, "")
	routes := []enrollReq{
		{method: http.MethodGet, path: "/v1/worker-enrollments"},
		{method: http.MethodPost, path: "/v1/worker-enrollments/" + id + "/accept", body: map[string]any{"pairing_code": code, "worker_id": "w9"}},
		{method: http.MethodPost, path: "/v1/worker-enrollments/" + id + "/reject", body: map[string]any{}},
	}
	for _, tok := range []string{jobToken, "tok-case-worker"} {
		for _, r := range routes {
			r.token = tok
			if rec := e.do(r); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with a non-user credential = %d, want 403", r.method, r.path, rec.Code)
			}
		}
	}
	if _, ok := e.WorkerToken("w9"); ok {
		t.Fatal("a refused accept registered a worker")
	}

	// Without worker.manage a person is refused too (the permission is not implied).
	sc := adminGroupConfig(true)
	gated := newEnrollEnv(t, true)
	gated.s = newTestServerCfg(t, sc)
	svc, _ := workerenroll.New(workerenroll.Options{Store: gated.store, Registrar: gated, Audit: gated, Config: gated})
	gated.s.SetWorkerEnrollment(svc)
	for _, r := range routes {
		r.token = "tok-op"
		if rec := gated.do(r); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without worker.manage = %d, want 403", r.method, r.path, rec.Code)
		}
	}
	// The anonymous routes need no credential at all, gate or not.
	if rec := gated.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", body: validEnrollBody()}); rec.Code != http.StatusCreated {
		t.Errorf("anonymous enroll = %d", rec.Code)
	}
}

// The full path plus the leak checks: request -> accept (code) -> poll gets the token ->
// confirm, with every response carrying no-store and the token confined to the one
// collect response (not in the list, the accept answer, the audit rows, nor the logs).
func TestEnrollEndToEndAndTokenConfinement(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	e := newEnrollEnv(t, true)
	noStore := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control=%q, want no-store", what, got)
		}
	}
	id, code := e.request("198.51.100.1:1")
	var seen []string // every response body except the collect one

	rec := e.poll(id, enrollSecret1, 0)
	noStore("poll pending", rec)
	seen = append(seen, rec.Body.String())
	if !strings.Contains(rec.Body.String(), `"pending"`) {
		t.Fatalf("poll = %s", rec.Body.String())
	}

	// A wrong code fails and changes nothing; the right one (typed with a dash) works.
	wrong := e.accept(id, "AAAA-AAAA", "gpu1")
	noStore("accept wrong code", wrong)
	if wrong.Code != http.StatusBadRequest || !strings.Contains(wrong.Body.String(), "pairing code mismatch") {
		t.Fatalf("wrong code = %d %s", wrong.Code, wrong.Body.String())
	}
	if rec := e.accept(id, code, "server"); rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved worker id = %d", rec.Code)
	}
	acc := e.accept(id, code[:4]+"-"+code[4:], "gpu1")
	noStore("accept", acc)
	if acc.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", acc.Code, acc.Body.String())
	}
	seen = append(seen, acc.Body.String())
	if again := e.accept(id, code, "gpu1"); again.Code != http.StatusConflict {
		t.Fatalf("second accept = %d, want 409", again.Code)
	}

	list := e.do(enrollReq{method: http.MethodGet, path: "/v1/worker-enrollments", token: enrollAdminTok})
	noStore("list", list)
	seen = append(seen, list.Body.String())
	for _, banned := range []string{"secret_hash", "token_fp", "pairing_code", code, sha256Hex(enrollSecret1)} {
		if strings.Contains(list.Body.String(), banned) {
			t.Errorf("list response leaks %q: %s", banned, list.Body.String())
		}
	}

	collect := e.poll(id, enrollSecret1, 0)
	noStore("collect", collect)
	var got enrollPollResp
	if err := json.Unmarshal(collect.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "accepted" || got.WorkerID != "gpu1" || !strings.HasPrefix(got.WorkerToken, "gw_") || got.CollectUntil == 0 {
		t.Fatalf("collect = %s", collect.Body.String())
	}
	if strings.Contains(strings.ToLower(collect.Body.String()), "url") {
		t.Fatalf("collect response carries a connect url: %s", collect.Body.String())
	}
	if tok, _ := e.WorkerToken("gpu1"); tok != got.WorkerToken {
		t.Fatal("the collected token is not the registered one")
	}
	// Collecting again is allowed until confirmed.
	if again := e.poll(id, enrollSecret1, 0); !strings.Contains(again.Body.String(), got.WorkerToken) {
		t.Fatalf("second collect = %s", again.Body.String())
	}

	conf := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll/" + id + "/confirm", secret: enrollSecret1})
	noStore("confirm", conf)
	if conf.Code != http.StatusNoContent {
		t.Fatalf("confirm = %d %s", conf.Code, conf.Body.String())
	}
	after := e.poll(id, enrollSecret1, 0)
	if strings.Contains(after.Body.String(), got.WorkerToken) || !strings.Contains(after.Body.String(), "collected") {
		t.Fatalf("poll after confirm = %s", after.Body.String())
	}
	seen = append(seen, after.Body.String())

	// A second request is cancelled by its applicant.
	id2, _ := e.request("198.51.100.2:1")
	can := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll/" + id2 + "/cancel", secret: enrollSecret1})
	noStore("cancel", can)
	if can.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d", can.Code)
	}
	rej := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enrollments/" + id2 + "/reject", token: enrollAdminTok, body: map[string]any{}})
	noStore("reject", rej)
	if rej.Code != http.StatusConflict {
		t.Fatalf("reject of a cancelled request = %d, want 409", rej.Code)
	}
	for _, rec := range []*httptest.ResponseRecorder{
		e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", body: []byte("{")}),
	} {
		noStore("bad request", rec)
	}

	// Token confinement.
	for _, body := range seen {
		if strings.Contains(body, got.WorkerToken) {
			t.Errorf("token leaked into a response: %s", body)
		}
	}
	if l := logs.String(); strings.Contains(l, got.WorkerToken) || strings.Contains(l, enrollSecret1) {
		t.Errorf("token or enroll secret in the logs:\n%s", l)
	}
	for _, kind := range []string{workerenroll.AuditRequest, workerenroll.AuditAccept, workerenroll.AuditCollect, workerenroll.AuditCancel, workerenroll.AuditCodeMismatch, workerenroll.AuditSecretMismatch} {
		rows, err := e.store.ListAuditEvents(kind, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if b, _ := json.Marshal(r); strings.Contains(string(b), got.WorkerToken) || strings.Contains(string(b), enrollSecret1) {
				t.Errorf("audit row leaks a credential: %s", b)
			}
		}
	}
	if rows, _ := e.store.ListAuditEvents(workerenroll.AuditAccept, id); len(rows) != 1 || rows[0].Actor != "root" {
		t.Errorf("accept audit rows = %+v, want one by root", rows)
	}
}

func TestEnrollRejectWithBlockThenRateLimited(t *testing.T) {
	e := newEnrollEnv(t, true)
	id, _ := e.request("198.51.100.9:1")
	rej := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enrollments/" + id + "/reject", token: enrollAdminTok,
		body: map[string]any{"reason": "unknown host", "block_source_min": 10}})
	if rej.Code != http.StatusNoContent {
		t.Fatalf("reject = %d %s", rej.Code, rej.Body.String())
	}
	if p := e.poll(id, enrollSecret1, 0); !strings.Contains(p.Body.String(), `"rejected"`) || !strings.Contains(p.Body.String(), "unknown host") {
		t.Fatalf("poll after reject = %s", p.Body.String())
	}
	blocked := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", remote: "198.51.100.9:1", body: validEnrollBody()})
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked source = %d (Retry-After %q)", blocked.Code, blocked.Header().Get("Retry-After"))
	}
	if rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", remote: "198.51.100.10:1", body: validEnrollBody()}); rec.Code != http.StatusCreated {
		t.Fatalf("another source = %d", rec.Code)
	}
	bad := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enrollments/x/reject", token: enrollAdminTok, body: map[string]any{"block_source_min": 5000}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("huge block = %d", bad.Code)
	}
}

func TestEnrollPerSourceRateLimit(t *testing.T) {
	e := newEnrollEnv(t, true)
	for i := 0; i < 3; i++ {
		e.request("198.51.100.20:1")
	}
	if rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enroll", remote: "198.51.100.20:1", body: validEnrollBody()}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th request = %d, want 429", rec.Code)
	}
}

func TestEnrollAcceptDuplicateWorkerIs409(t *testing.T) {
	e := newEnrollEnv(t, true)
	e.tokens["gpu1"] = "gw_existing"
	id, code := e.request("198.51.100.1:1")
	rec := e.accept(id, code, "gpu1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate worker id = %d %s", rec.Code, rec.Body.String())
	}
	// The request is back to pending and can be accepted under another id.
	if rec := e.accept(id, code, "gpu2"); rec.Code != http.StatusOK {
		t.Fatalf("retry with another id = %d %s", rec.Code, rec.Body.String())
	}
}

func TestEnrollLongPoll(t *testing.T) {
	old := enrollPollInterval
	enrollPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { enrollPollInterval = old })

	e := newEnrollEnv(t, true)
	id, code := e.request("198.51.100.1:1")

	// A poll that waits sees the accept that happens meanwhile.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.poll(id, enrollSecret1, 9) }()
	if rec := e.accept(id, code, "gpu1"); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body.String())
	}
	select {
	case rec := <-done:
		if !strings.Contains(rec.Body.String(), `"accepted"`) || !strings.Contains(rec.Body.String(), "worker_token") {
			t.Fatalf("long poll = %s", rec.Body.String())
		}
	case <-time.After(wait.Timeout(t, 8*time.Second)):
		t.Fatal("the long poll did not return after the accept")
	}

	// A cancelled request context ends a waiting poll at once.
	id2, _ := e.request("198.51.100.2:1")
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		e.doCtx(ctx, enrollReq{method: http.MethodGet, path: "/v1/worker-enroll/" + id2 + "?wait=9", secret: enrollSecret1})
	}()
	cancel()
	select {
	case <-ended:
	case <-time.After(wait.Timeout(t, 8*time.Second)):
		t.Fatal("a cancelled context did not end the long poll")
	}
}

func TestEnrollWaitIsCapped(t *testing.T) {
	// The cap is applied before the loop; a pending poll with wait=0 returns at once.
	e := newEnrollEnv(t, true)
	id, _ := e.request("198.51.100.1:1")
	start := time.Now()
	rec := e.poll(id, enrollSecret1, 0)
	if rec.Code != http.StatusOK || time.Since(start) > 2*time.Second {
		t.Fatalf("wait=0 = %d after %v", rec.Code, time.Since(start))
	}
	if enrollMaxWait != 25*time.Second {
		t.Fatalf("enrollMaxWait=%v", enrollMaxWait)
	}
}

// The console write branch for server.worker_enrollment: the editable members are
// replaced, trusted_proxies is file-only and survives (and cannot be set) via the console.
func TestApplyServerFieldWorkerEnrollment(t *testing.T) {
	t.Parallel()
	sc := config.ServerConfig{WorkerEnrollment: config.WorkerEnrollmentConfig{
		Enabled: true, DecideSec: 100, CollectSec: 100, PerSourcePerMin: 9, MaxPending: 9,
		TrustedProxies: []string{"10.0.0.0/8"},
	}}
	raw := json.RawMessage(`{"enabled":false,"decide_sec":90,"collect_sec":45,"per_source_per_min":5,"max_pending":7,"trusted_proxies":["0.0.0.0/0"]}`)
	if err := applyServerField(&sc, configBodyField{name: "worker_enrollment", path: "server.worker_enrollment", raw: raw}); err != nil {
		t.Fatalf("applyServerField: %v", err)
	}
	w := sc.WorkerEnrollment
	if w.Enabled || w.DecideSec != 90 || w.CollectSec != 45 || w.PerSourcePerMin != 5 || w.MaxPending != 7 {
		t.Fatalf("editable members not replaced: %+v", w)
	}
	if len(w.TrustedProxies) != 1 || w.TrustedProxies[0] != "10.0.0.0/8" {
		t.Fatalf("trusted_proxies=%v, want the configured list untouched", w.TrustedProxies)
	}
	// An omitted block (null) resets the editable members and still keeps the proxies.
	if err := applyServerField(&sc, configBodyField{name: "worker_enrollment", path: "server.worker_enrollment", raw: json.RawMessage("null")}); err != nil {
		t.Fatal(err)
	}
	if sc.WorkerEnrollment.Enabled || sc.WorkerEnrollment.DecideSec != 0 || len(sc.WorkerEnrollment.TrustedProxies) != 1 {
		t.Fatalf("null write: %+v", sc.WorkerEnrollment)
	}
	if p := config.SectionPolicies("server")["worker_enrollment"]; !p.Editable {
		t.Fatalf("worker_enrollment policy=%+v, want editable", p)
	}
}

func TestConsoleWriteWorkerEnrollmentKeepsTrustedProxies(t *testing.T) {
	t.Parallel()
	yamlText, _, _ := configWriteFixture(t)
	yamlText = strings.Replace(yamlText, "server:\n", "server:\n  worker_enrollment:\n    enabled: true\n    trusted_proxies: [\"10.0.0.0/8\"]\n", 1)
	s, cr, _ := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"worker_enrollment": map[string]any{"enabled": false, "decide_sec": 90, "collect_sec": 45,
			"per_source_per_min": 5, "max_pending": 7, "trusted_proxies": []string{"0.0.0.0/0"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}
	w := cr.Jobs.Config().Server.WorkerEnrollment
	if w.Enabled || w.DecideSec != 90 || w.CollectSec != 45 || w.PerSourcePerMin != 5 || w.MaxPending != 7 {
		t.Fatalf("live config not replaced: %+v", w)
	}
	if len(w.TrustedProxies) != 1 || w.TrustedProxies[0] != "10.0.0.0/8" {
		t.Fatalf("trusted_proxies=%v, want the file value kept", w.TrustedProxies)
	}
}
