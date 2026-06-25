package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newCLIAgentServer builds a Server whose "self" project allows a cli-agent
// "echoagent" (backed by /bin/echo so submitted jobs actually run) on the local
// runner. It lets the md-submit path reach Submit successfully so the
// server-stamped caller_id can be observed in the response.
func newCLIAgentServer(t *testing.T, token string) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: token},
		Storage: config.StorageConfig{Root: root},
		Agents: map[string]config.AgentConfig{
			"echoagent": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}},
		},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"echoagent"},
				AllowedRunners: []string{"local"},
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, filepath.Join(root, "db")), nil)
	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	return New(&cfg.Server, token, false, jobs, jobsEng, projects, agents, nil, nil, nil, nil)
}

// doRaw posts a raw body with an explicit content type (the md submit path needs
// a non-JSON content type, which the shared `do` helper does not support).
func doRaw(t *testing.T, s *Server, method, path, token, contentType string, body []byte) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

// TestSyncSubmitFastCommandReturnsTerminal: a fast exec job submitted with
// sync:true returns 200 + the terminal (done) JobResult in one round trip.
func TestSyncSubmitFastCommandReturnsTerminal(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		Sync: true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Gofer-Async") != "" {
		t.Fatalf("unexpected X-Gofer-Async header on a completed sync submit")
	}
	var jr job.JobResult
	decode(t, resp, &jr)
	if jr.Status != job.StatusDone {
		t.Fatalf("status=%s want done (err=%s)", jr.Status, jr.Error)
	}
	if jr.ExitCode != 0 {
		t.Fatalf("exit_code=%d want 0", jr.ExitCode)
	}
}

// TestSyncSubmitQueryParam: ?wait=1 enables sync submit the same as body.sync.
func TestSyncSubmitQueryParam(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs?wait=1", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var jr job.JobResult
	decode(t, resp, &jr)
	if jr.Status != job.StatusDone {
		t.Fatalf("status=%s want done", jr.Status)
	}
}

// TestSyncSubmitSlowCommandFallsBackTo202: a slow job that does not finish
// within the (clamped) wait cap returns 202 + X-Gofer-Async + the initial
// result, and the job keeps running. wait_timeout_sec=1 is the smallest cap the
// clamp allows, so a `sleep 5` reliably exceeds it.
func TestSyncSubmitSlowCommandFallsBackTo202(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
		Sync: true, WaitTimeoutSec: 1,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, want 202 (async fallback)", resp.StatusCode)
	}
	if resp.Header.Get("X-Gofer-Async") != "1" {
		t.Fatalf("missing X-Gofer-Async:1 header on async fallback")
	}
	var jr job.JobResult
	decode(t, resp, &jr)
	if jr.ID == "" {
		t.Fatalf("202 body missing job id: %+v", jr)
	}
	if job.IsTerminal(jr.Status) {
		t.Fatalf("job is terminal (%s) on 202 fallback; should still be running", jr.Status)
	}
}

// TestAsyncSubmitUnchanged: a plain submit (no sync) still returns 200 + the
// initial (non-terminal) result immediately, with no async header.
func TestAsyncSubmitUnchanged(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Gofer-Async") != "" {
		t.Fatalf("plain async submit should not set X-Gofer-Async")
	}
}

// --- parseMarkdownRequest unit tests ---

func TestParseMarkdownRequestNormal(t *testing.T) {
	md := []byte("---\nproject_key: my-proj\nagent: codex\nrunner: worker\ntitle: gen\n---\nplease generate a script\nwith two lines\n")
	req, err := parseMarkdownRequest(md)
	if err != nil {
		t.Fatalf("parseMarkdownRequest: %v", err)
	}
	if req.ProjectKey != "my-proj" || req.Agent != "codex" || req.Runner != "worker" || req.Title != "gen" {
		t.Fatalf("frontmatter not parsed: %+v", req)
	}
	if req.Prompt != "please generate a script\nwith two lines" {
		t.Fatalf("prompt body mismatch: %q", req.Prompt)
	}
}

// TestParseMarkdownRequestTags: a `tags:` frontmatter list lands in
// JobRequest.Tags (E5 P2-d). The md submit path reuses the struct's yaml tags,
// so tags round-trip without any special-casing in the parser.
func TestParseMarkdownRequestTags(t *testing.T) {
	md := []byte("---\nproject_key: p\nagent: codex\nrunner: local\ntags:\n  - x\n  - y\n---\nbody\n")
	req, err := parseMarkdownRequest(md)
	if err != nil {
		t.Fatalf("parseMarkdownRequest: %v", err)
	}
	if len(req.Tags) != 2 || req.Tags[0] != "x" || req.Tags[1] != "y" {
		t.Fatalf("tags not parsed from frontmatter: %+v", req.Tags)
	}
}

// TestParseMarkdownRequestTagsInline: the flow/inline list form `tags: [x, y]`
// also parses (same struct, goccy/go-yaml handles both block and flow lists).
func TestParseMarkdownRequestTagsInline(t *testing.T) {
	md := []byte("---\nproject_key: p\nagent: codex\nrunner: local\ntags: [x, y]\n---\nbody\n")
	req, err := parseMarkdownRequest(md)
	if err != nil {
		t.Fatalf("parseMarkdownRequest: %v", err)
	}
	if len(req.Tags) != 2 || req.Tags[0] != "x" || req.Tags[1] != "y" {
		t.Fatalf("inline tags not parsed: %+v", req.Tags)
	}
}

func TestParseMarkdownRequestNoFrontmatter(t *testing.T) {
	if _, err := parseMarkdownRequest([]byte("just a prompt, no frontmatter\n")); err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseMarkdownRequestUnterminatedFrontmatter(t *testing.T) {
	// Opening '---' but no closing '---' line.
	if _, err := parseMarkdownRequest([]byte("---\nagent: codex\nstill in frontmatter\n")); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestParseMarkdownRequestOversize(t *testing.T) {
	big := make([]byte, maxMarkdownBytes+1)
	if _, err := parseMarkdownRequest(big); err == nil {
		t.Fatal("expected error for oversize body")
	}
}

// TestParseMarkdownRequestCallerIDNotForged: caller_id in frontmatter is ignored
// by the yaml decoder (yaml:"-"), so it never lands in the parsed request.
func TestParseMarkdownRequestCallerIDNotForged(t *testing.T) {
	md := []byte("---\nproject_key: p\nagent: codex\nrunner: local\ncaller_id: attacker\n---\nbody\n")
	req, err := parseMarkdownRequest(md)
	if err != nil {
		t.Fatalf("parseMarkdownRequest: %v", err)
	}
	if req.CallerID != "" {
		t.Fatalf("caller_id was forged from frontmatter: %q", req.CallerID)
	}
}

// --- handler content-type branch tests ---

// TestMarkdownSubmitExecRejected: an md submit declaring agent=exec is a 400
// (md prose -> prompt is for cli-agents; exec wants JSON + argv).
func TestMarkdownSubmitExecRejected(t *testing.T) {
	s := newTestServer(t, testToken, false)
	md := []byte("---\nproject_key: self\nagent: exec\nrunner: local\n---\nrun something\n")
	resp := doRaw(t, s, http.MethodPost, "/v1/jobs", testToken, "text/markdown", md)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (exec via markdown)", resp.StatusCode)
	}
	var eb errorBody
	decode(t, resp, &eb)
	if !strings.Contains(strings.ToLower(eb.Error), "markdown") {
		t.Fatalf("error should mention markdown: %+v", eb)
	}
}

// TestMarkdownSubmitBadFrontmatter: a md body with no frontmatter is a 400.
func TestMarkdownSubmitBadFrontmatter(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := doRaw(t, s, http.MethodPost, "/v1/jobs", testToken, "text/markdown", []byte("no frontmatter here"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (no frontmatter)", resp.StatusCode)
	}
}

// TestMarkdownSubmitCallerIDOverridden: a successful md submit (cli-agent) whose
// frontmatter tries to forge caller_id ends up with the server-stamped caller id
// ("default" for the legacy token), never the forged value — end-to-end proof of
// the anti-spoof handler stamp on the md path.
func TestMarkdownSubmitCallerIDOverridden(t *testing.T) {
	s := newCLIAgentServer(t, testToken)
	md := []byte("---\nproject_key: self\nagent: echoagent\nrunner: local\ncaller_id: attacker\n---\ngenerate a script\n")
	resp := doRaw(t, s, http.MethodPost, "/v1/jobs", testToken, "text/markdown", md)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200 (md submit accepted)", resp.StatusCode)
	}
	var jr job.JobResult
	decode(t, resp, &jr)
	if jr.CallerID == "attacker" {
		t.Fatalf("caller_id was forged via frontmatter: %q", jr.CallerID)
	}
	if jr.CallerID != "default" {
		t.Fatalf("caller_id=%q, want server-stamped \"default\"", jr.CallerID)
	}
}

// TestMarkdownSubmitReachesSubmit: a non-exec md submit forwards to Submit (the
// agent-allow check rejects an unlisted cli-agent with a 400 whose message is NOT
// the md-exec guard) — proving the content-type branch parsed and forwarded.
func TestMarkdownSubmitReachesSubmit(t *testing.T) {
	s := newTestServer(t, testToken, false)
	md := []byte("---\nproject_key: self\nagent: codex\nrunner: local\n---\nhello\n")
	resp := doRaw(t, s, http.MethodPost, "/v1/jobs", testToken, "text/markdown", md)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (agent not allowed)", resp.StatusCode)
	}
	var eb errorBody
	decode(t, resp, &eb)
	if strings.Contains(strings.ToLower(eb.Error), "markdown") {
		t.Fatalf("expected agent-allow rejection, got md-exec guard: %+v", eb)
	}
}
