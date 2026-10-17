package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	yaml "github.com/goccy/go-yaml"
	"github.com/gookit/gcli/v3"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// joinTestServer is the fake hub a `worker join` test talks to on ONE address: the four
// anonymous enrollment routes (with a scripted decision), the read-only assignable route
// and the real register handshake the wizard's doctor probes once the token is in hand.
// Every call is counted so a test can assert what the command really did — no real
// server is contacted (G053).
type joinTestServer struct {
	srv *httptest.Server

	mu       sync.Mutex
	enrollID string
	pairing  string
	// decideFor is how long the request stays decidable.
	decideFor time.Duration

	requests    int
	polls       int
	confirms    int
	cancels     int
	requestedID string
	labels      []string
	secretHash  string
	host        client.EnrollHost
	enrollAuth  string

	// decide answers poll n (1-based); a pending state means "wait longer".
	decide func(n int) client.EnrollPoll
	// block, when non-nil, holds a pending poll until it is closed, so a test can act
	// while the command is waiting without racing the poll.
	block chan struct{}
	// enrollStatus / enrollDetail make POST /v1/worker-enroll answer an error instead.
	enrollStatus int
	enrollDetail string
	// failAssignable makes the assignable route fail, so the `worker init` step after an
	// accepted request cannot finish.
	failAssignable bool
}

// newJoinTestServer serves the fake hub. workerID/token are what the accepted request
// hands back; projects is what the assignable route reports.
func newJoinTestServer(t *testing.T, workerID, token string, projects []map[string]string) *joinTestServer {
	t.Helper()
	s := &joinTestServer{enrollID: "we_test", pairing: "K7QXM3PA", decideFor: 5 * time.Minute}
	hub := wshub.New(map[string]string{workerID: workerID})
	r := rux.New()
	r.GET("/v1/workers/connect", func(c *rux.Context) { hub.Accept(c.Resp, c.Req, workerID) })
	r.GET("/v1/workers/{id}/assignable", func(c *rux.Context) {
		s.mu.Lock()
		fail := s.failAssignable
		s.mu.Unlock()
		if fail {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": "assignable failed"})
			return
		}
		c.JSON(http.StatusOK, map[string]any{
			"worker_id":        c.Param("id"),
			"projects":         projects,
			"server_version":   "v0.99-test",
			"protocol_version": wsproto.CurrentProtocolVersion,
		})
	})
	r.POST("/v1/worker-enroll", s.handleEnrollRequest)
	r.GET("/v1/worker-enroll/{id}", s.handleEnrollPoll)
	r.POST("/v1/worker-enroll/{id}/confirm", func(c *rux.Context) { s.handleApplicantAction(c, "confirm") })
	r.POST("/v1/worker-enroll/{id}/cancel", func(c *rux.Context) { s.handleApplicantAction(c, "cancel") })
	s.srv = httptest.NewServer(r)
	t.Cleanup(s.srv.Close)
	return s
}

// stats reads the counted calls: requests, polls, confirms, cancels.
func (s *joinTestServer) stats() (requests, polls, confirms, cancels int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.polls, s.confirms, s.cancels
}

// lastRequest reads what the enrollment request carried.
func (s *joinTestServer) lastRequest() (requestedID string, labels []string, host client.EnrollHost, secretHash, auth string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requestedID, s.labels, s.host, s.secretHash, s.enrollAuth
}

func (s *joinTestServer) handleEnrollRequest(c *rux.Context) {
	var body client.EnrollRequest
	if err := json.NewDecoder(c.Req.Body).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request", "detail": "malformed JSON body"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enrollStatus != 0 {
		c.JSON(s.enrollStatus, map[string]string{"error": "not found", "detail": s.enrollDetail})
		return
	}
	s.requests++
	s.requestedID, s.labels, s.host, s.secretHash = body.RequestedID, body.Labels, body.Host, body.SecretHash
	s.enrollAuth = c.Req.Header.Get("Authorization")
	c.JSON(http.StatusCreated, map[string]any{
		"enroll_id":    s.enrollID,
		"pairing_code": s.pairing,
		"decide_until": time.Now().Add(s.decideFor).Unix(),
	})
}

func (s *joinTestServer) handleEnrollPoll(c *rux.Context) {
	if c.Req.Header.Get("X-Gofer-Enroll-Secret") == "" {
		// A request that cannot be authenticated gets the uniform 404 (no id, no state).
		c.JSON(http.StatusNotFound, map[string]string{"error": "not found", "detail": "enrollment not found"})
		return
	}
	s.mu.Lock()
	s.polls++
	n, decide, block := s.polls, s.decide, s.block
	s.mu.Unlock()
	answer := client.EnrollPoll{State: "pending"}
	if decide != nil {
		answer = decide(n)
	}
	if answer.State == "pending" && block != nil {
		select {
		case <-block:
		case <-c.Req.Context().Done():
			return
		}
	}
	c.JSON(http.StatusOK, answer)
}

func (s *joinTestServer) handleApplicantAction(c *rux.Context, action string) {
	if c.Req.Header.Get("X-Gofer-Enroll-Secret") == "" {
		c.JSON(http.StatusNotFound, map[string]string{"error": "not found", "detail": "enrollment not found"})
		return
	}
	s.mu.Lock()
	if action == "confirm" {
		s.confirms++
	} else {
		s.cancels++
	}
	s.mu.Unlock()
	c.Resp.WriteHeader(http.StatusNoContent)
}

// joinTestConfigDir points the config dir (and the home dir a workspace would land in) at
// temp dirs, so a join test never writes into the developer's own gofer home. It returns
// the config dir and the .env path inside it.
func joinTestConfigDir(t *testing.T) (dir, envPath string) {
	t.Helper()
	dir = t.TempDir()
	setTestHome(t, t.TempDir())
	t.Setenv(config.EnvConfigDir, dir)
	t.Setenv(workerTokenEnv, "") // runWorkerInit exports the issued token; restored afterwards
	return dir, filepath.Join(dir, config.EnvFileName)
}

// workerJoinCmd binds `worker join` the way the CLI does and replaces the package-level
// seams a join test needs: the flag struct (gcli wrote its defaults while binding), the
// host probe (the same fixture agents the worker-init tests use) and the interrupt
// channel (so no test raises a real signal). Binding FIRST matters.
func workerJoinCmd(t *testing.T) (*gcli.Command, chan os.Signal) {
	t.Helper()
	c := bindCmd(NewWorkerJoinCmd(buildinfo.Info{}))
	oldOpts, oldDet, oldSignals := workerJoinOpts, workerInitDetector, workerJoinInterrupts
	t.Cleanup(func() { workerJoinOpts, workerInitDetector, workerJoinInterrupts = oldOpts, oldDet, oldSignals })
	workerJoinOpts = workerJoinFlags{}
	workerInitDetector = workerInitFixtureDetector()
	sigs := make(chan os.Signal, 1)
	workerJoinInterrupts = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	return c, sigs
}

// joinAccepted is the scripted decision of an accepted request.
func joinAccepted(id, token string) func(int) client.EnrollPoll {
	return func(int) client.EnrollPoll {
		return client.EnrollPoll{State: "accepted", WorkerID: id, WorkerToken: token, CollectUntil: time.Now().Add(time.Minute).Unix()}
	}
}

// TestWorkerJoinAccepted pins the happy path end to end: the request is anonymous and
// carries this host's description, the pairing code is shown split 4+4 (never the token),
// the collected token lands in .env, worker.yaml is written for the ACCEPTED worker id and
// the collection is confirmed.
func TestWorkerJoinAccepted(t *testing.T) {
	dir, envPath := joinTestConfigDir(t)
	root := t.TempDir()
	srv := newJoinTestServer(t, "w-joined", "tok-joined", []map[string]string{{"key": "p-a", "host_path": slashPath(root)}})
	srv.decide = joinAccepted("w-joined", "tok-joined")

	c, _ := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.id = "w-joined"
	workerJoinOpts.labels = []string{"linux", "gpu"}
	workerJoinOpts.roots = []string{root + "=" + root}
	workerJoinOpts.yes = true

	var err error
	out := captureOutput(t, func() { err = runWorkerJoin(c, buildinfo.Info{}) })
	if err != nil {
		t.Fatalf("worker join: %v\noutput:\n%s", err, out)
	}
	// The pairing code, split 4+4, and where to type it.
	if !strings.Contains(out, "K7QX-M3PA") || !strings.Contains(out, "Runners") {
		t.Fatalf("the pairing code and the Runners hint must be printed:\n%s", out)
	}
	// Rule: no output ever carries the token.
	if strings.Contains(out, "tok-joined") {
		t.Fatalf("the worker token must never be printed:\n%s", out)
	}

	raw, err := os.ReadFile(filepath.Join(dir, config.WorkerConfigFileName))
	if err != nil {
		t.Fatalf("read generated worker.yaml: %v", err)
	}
	var wc config.WorkerConfig
	if err := yaml.Unmarshal(raw, &wc); err != nil {
		t.Fatalf("generated worker.yaml does not parse: %v\n%s", err, raw)
	}
	if wc.WorkerID != "w-joined" {
		t.Fatalf("worker_id = %q, want w-joined", wc.WorkerID)
	}
	if wc.ServerLink.TokenEnv != workerTokenEnv {
		t.Fatalf("token_env = %q, want %s", wc.ServerLink.TokenEnv, workerTokenEnv)
	}
	if len(wc.Roots) != 1 || wc.Roots[0].From != root || wc.Roots[0].To != root {
		t.Fatalf("roots = %+v, want the explicit --roots mapping", wc.Roots)
	}
	env, err := os.ReadFile(envPath)
	if err != nil || !strings.Contains(string(env), workerTokenEnv+"=tok-joined") {
		t.Fatalf(".env = %q (err %v), want the collected token", env, err)
	}

	requests, polls, confirms, cancels := srv.stats()
	if requests != 1 || confirms != 1 || polls < 1 || cancels != 0 {
		t.Fatalf("server calls = requests:%d polls:%d confirms:%d cancels:%d, want 1/>=1/1/0", requests, polls, confirms, cancels)
	}
	requestedID, labels, host, secretHash, auth := srv.lastRequest()
	if requestedID != "w-joined" {
		t.Fatalf("requested_id = %q, want the proposed w-joined", requestedID)
	}
	if strings.Join(labels, ",") != "linux,gpu" {
		t.Fatalf("labels = %v, want [linux gpu]", labels)
	}
	if auth != "" {
		t.Fatalf("the enrollment request must be anonymous, got Authorization %q", auth)
	}
	if len(secretHash) != 64 || strings.ContainsAny(secretHash, "GHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("secret_hash = %q, want 64 hex characters", secretHash)
	}
	if host.Hostname == "" || host.OS != runtime.GOOS || host.Arch != runtime.GOARCH {
		t.Fatalf("host = %+v, want this machine's hostname/os/arch", host)
	}
	if host.ProtocolVersion != strconv.Itoa(wsproto.CurrentProtocolVersion) {
		t.Fatalf("host.protocol_version = %q, want %d", host.ProtocolVersion, wsproto.CurrentProtocolVersion)
	}
	if strings.Join(host.Agents, ",") != "claude,codex" {
		t.Fatalf("host.agents = %v, want the detected fixture agents", host.Agents)
	}
	if strings.Join(host.Roots, ",") != root+"="+root {
		// Only the roots the operator named explicitly are reported.
		t.Fatalf("host.roots = %v, want exactly the explicit --roots", host.Roots)
	}
}

// TestWorkerJoinUsesTheAcceptedWorkerID: the person accepting may pick another id than
// the one proposed, and everything written here must follow the ACCEPTED id.
func TestWorkerJoinUsesTheAcceptedWorkerID(t *testing.T) {
	dir, _ := joinTestConfigDir(t)
	root := t.TempDir()
	srv := newJoinTestServer(t, "w-final", "tok-final", nil)
	srv.decide = joinAccepted("w-final", "tok-final")

	c, _ := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.id = "w-proposed"
	workerJoinOpts.roots = []string{root + "=" + root}
	workerJoinOpts.yes = true

	var err error
	out := captureOutput(t, func() { err = runWorkerJoin(c, buildinfo.Info{}) })
	if err != nil {
		t.Fatalf("worker join: %v\noutput:\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, config.WorkerConfigFileName))
	if err != nil {
		t.Fatalf("read generated worker.yaml: %v", err)
	}
	var wc config.WorkerConfig
	if err := yaml.Unmarshal(raw, &wc); err != nil {
		t.Fatalf("generated worker.yaml does not parse: %v\n%s", err, raw)
	}
	if wc.WorkerID != "w-final" {
		t.Fatalf("worker_id = %q, want the accepted w-final (not the proposed w-proposed)", wc.WorkerID)
	}
	if requestedID, _, _, _, _ := srv.lastRequest(); requestedID != "w-proposed" {
		t.Fatalf("requested_id = %q, want the proposal w-proposed", requestedID)
	}
}

// TestWorkerJoinRejected: a rejection prints the server's reason, exits non-zero and
// writes nothing locally.
func TestWorkerJoinRejected(t *testing.T) {
	dir, _ := joinTestConfigDir(t)
	srv := newJoinTestServer(t, "w-joined", "tok-joined", nil)
	srv.decide = func(int) client.EnrollPoll {
		return client.EnrollPoll{State: "rejected", Reason: "untrusted host"}
	}
	c, _ := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.yes = true

	err := runWorkerJoin(c, buildinfo.Info{})
	if err == nil {
		t.Fatal("a rejected request must end the command with a non-zero exit")
	}
	assertCodedExit(t, err)
	if !strings.Contains(err.Error(), "拒绝") || !strings.Contains(err.Error(), "untrusted host") {
		t.Fatalf("the rejection must carry the server's reason, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.WorkerConfigFileName)); statErr == nil {
		t.Fatal("no worker.yaml may be written for a rejected request")
	}
}

// TestWorkerJoinExpired: an expired request (nobody decided in time) is a distinct,
// actionable outcome.
func TestWorkerJoinExpired(t *testing.T) {
	joinTestConfigDir(t)
	srv := newJoinTestServer(t, "w-joined", "tok-joined", nil)
	srv.decide = func(int) client.EnrollPoll { return client.EnrollPoll{State: "expired"} }
	c, _ := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.yes = true

	err := runWorkerJoin(c, buildinfo.Info{})
	if err == nil {
		t.Fatal("an expired request must end the command with a non-zero exit")
	}
	assertCodedExit(t, err)
	if !strings.Contains(err.Error(), "过期") {
		t.Fatalf("the expiry must be named, got %v", err)
	}
}

// TestWorkerJoinSuperseded: a newer request from the same source replaces the old one,
// and the old command says so instead of looking like a failure.
func TestWorkerJoinSuperseded(t *testing.T) {
	joinTestConfigDir(t)
	srv := newJoinTestServer(t, "w-joined", "tok-joined", nil)
	srv.decide = func(int) client.EnrollPoll { return client.EnrollPoll{State: "superseded"} }
	c, _ := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.yes = true

	err := runWorkerJoin(c, buildinfo.Info{})
	if err == nil {
		t.Fatal("a superseded request must end the command with a non-zero exit")
	}
	assertCodedExit(t, err)
	if !strings.Contains(err.Error(), "取代") {
		t.Fatalf("the supersede must be explained, got %v", err)
	}
}

// TestWorkerJoinCtrlCGivesTheRequestUp: Ctrl-C during the wait calls cancel once (so the
// request stops waiting in front of a person) and exits non-zero without writing config.
func TestWorkerJoinCtrlCGivesTheRequestUp(t *testing.T) {
	dir, _ := joinTestConfigDir(t)
	srv := newJoinTestServer(t, "w-joined", "tok-joined", nil)
	srv.block = make(chan struct{}) // the first poll waits here until the test releases it
	srv.decide = func(int) client.EnrollPoll { return client.EnrollPoll{State: "pending"} }
	// Registered AFTER the server's Close, so it runs first (t.Cleanup is LIFO) and the
	// still-blocked poll finishes before Close waits for it.
	t.Cleanup(func() { close(srv.block) })

	c, interrupts := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.yes = true
	interrupts <- os.Interrupt // Ctrl-C arrives while the poll is in flight

	var err error
	out := captureOutput(t, func() { err = runWorkerJoin(c, buildinfo.Info{}) })
	if err == nil {
		t.Fatal("Ctrl-C must end the command with a non-zero exit")
	}
	assertCodedExit(t, err)
	if !strings.Contains(err.Error(), "已放弃") {
		t.Fatalf("giving up must be stated, got %v\noutput:\n%s", err, out)
	}
	if requests, _, _, cancels := srv.stats(); requests != 1 || cancels != 1 {
		t.Fatalf("server calls = requests:%d cancels:%d, want exactly one give-up", requests, cancels)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.WorkerConfigFileName)); statErr == nil {
		t.Fatal("no worker.yaml may be written when the request was given up")
	}
}

// TestWorkerJoinKeepsTheTokenWhenInitFails: once the token is collected the registration
// exists on the server, so a failure in the `worker init` step must leave the token in
// .env, confirm the collection and tell the operator how to finish.
func TestWorkerJoinKeepsTheTokenWhenInitFails(t *testing.T) {
	dir, envPath := joinTestConfigDir(t)
	root := t.TempDir()
	srv := newJoinTestServer(t, "w-half", "tok-half", nil)
	srv.failAssignable = true
	srv.decide = joinAccepted("w-half", "tok-half")

	c, _ := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.roots = []string{root + "=" + root}
	workerJoinOpts.yes = true

	var err error
	out := captureOutput(t, func() { err = runWorkerJoin(c, buildinfo.Info{}) })
	if err == nil {
		t.Fatal("a failing worker init after the accept must end the command with a non-zero exit")
	}
	assertCodedExit(t, err)
	if !strings.Contains(out, "已登记为 w-half") || !strings.Contains(out, "gofer worker init") {
		t.Fatalf("the recovery hint must be printed:\n%s", out)
	}
	env, rerr := os.ReadFile(envPath)
	if rerr != nil || !strings.Contains(string(env), workerTokenEnv+"=tok-half") {
		t.Fatalf(".env = %q (err %v), want the collected token kept", env, rerr)
	}
	if _, _, confirms, _ := srv.stats(); confirms != 1 {
		t.Fatal("the token must be confirmed as collected before the doctor can fail")
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.WorkerConfigFileName)); statErr == nil {
		t.Fatal("the wizard never got past assignable, so no worker.yaml may exist")
	}
}

// TestWorkerJoinRefusesExistingWorkerConfig: an existing worker.yaml is refused BEFORE any
// request, so a pending row never appears in front of a person for a join that cannot be
// written.
func TestWorkerJoinRefusesExistingWorkerConfig(t *testing.T) {
	dir, _ := joinTestConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, config.WorkerConfigFileName), []byte("worker_id: old\n"), 0o600); err != nil {
		t.Fatalf("seed worker.yaml: %v", err)
	}
	srv := newJoinTestServer(t, "w-joined", "tok-joined", nil)
	c, _ := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.yes = true

	err := runWorkerJoin(c, buildinfo.Info{})
	if err == nil {
		t.Fatal("an existing worker.yaml must be refused without --force")
	}
	assertCodedExit(t, err)
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("the refusal must name --force, got %v", err)
	}
	if requests, _, _, _ := srv.stats(); requests != 0 {
		t.Fatalf("no enrollment request may be sent, got %d", requests)
	}
}

// TestWorkerJoinWarnsOnNonLoopbackPlainHTTP: the token crosses that very connection, so a
// plain-http server outside the loopback interface produces a warning (https and loopback
// do not).
func TestWorkerJoinWarnsOnNonLoopbackPlainHTTP(t *testing.T) {
	cases := []struct {
		addr     string
		insecure bool
	}{
		{"http://worker-host:8767", true},
		{"worker-host:8767", true}, // a bare host:port is http for the client too
		{"http://192.0.2.10:8767", true},
		{"http://[2001:db8::1]:8767", true},
		{"https://worker-host:8767", false},
		{"wss://worker-host:8767/v1/workers/connect", false},
		{"http://127.0.0.1:8767", false},
		{"http://localhost:8767", false},
		{"http://[::1]:8767", false},
		{"http://0.0.0.0:8767", false}, // the default listen address connects to loopback
	}
	for _, tc := range cases {
		if got := joinServerInsecure(tc.addr); got != tc.insecure {
			t.Errorf("joinServerInsecure(%q) = %t, want %t", tc.addr, got, tc.insecure)
		}
	}
	c, _ := workerJoinCmd(t)
	out := captureOutput(t, func() { warnIfInsecureJoinServer(c, "http://worker-host:8767") })
	if !strings.Contains(out, "明文") || !strings.Contains(out, "https") {
		t.Fatalf("plain http to a remote host must warn about the clear-text token:\n%s", out)
	}
	if out := captureOutput(t, func() { warnIfInsecureJoinServer(c, "http://127.0.0.1:8767") }); out != "" {
		t.Fatalf("loopback http must not warn, got %q", out)
	}
}

// TestWorkerJoinReportsDisabledEnrollment: the feature is off by default, and the 404 the
// anonymous routes answer with says exactly that — join must translate it instead of
// reporting a bare "not found".
func TestWorkerJoinReportsDisabledEnrollment(t *testing.T) {
	joinTestConfigDir(t)
	srv := newJoinTestServer(t, "w-joined", "tok-joined", nil)
	srv.enrollStatus = http.StatusNotFound
	srv.enrollDetail = "worker enrollment is disabled on this server"
	c, _ := workerJoinCmd(t)
	workerJoinOpts.server = srv.srv.URL
	workerJoinOpts.yes = true

	var err error
	_ = captureOutput(t, func() { err = runWorkerJoin(c, buildinfo.Info{}) })
	if err == nil {
		t.Fatal("a disabled feature must end the command with a non-zero exit")
	}
	assertCodedExit(t, err)
	if !strings.Contains(err.Error(), "未开启自助注册") {
		t.Fatalf("the disabled feature must be named, got %v", err)
	}
}
