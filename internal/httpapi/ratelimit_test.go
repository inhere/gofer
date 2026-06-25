package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"

	"github.com/gookit/rux/v2"
	"golang.org/x/time/rate"
)

// newRateLimitedServer builds a Server like newTestServer but installs an E17
// governance rate limit (rps/burst) that applies to the "default" caller (the
// legacy token's caller id). The cfg is shared with the job service so
// CallerRate reads it live. Returns the server (token = testToken).
func newRateLimitedServer(t *testing.T, rps float64, burst int) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token: testToken,
			Governance: config.GovernanceConfig{
				DefaultRateLimit: rps,
				DefaultRateBurst: burst,
			},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	return New(&cfg.Server, testToken, false, jobs, jobsEng, projects, agents, nil, nil, nil, nil)
}

// TestRateLimitSubmitJobs (E17, design §7.3) proves that with rate=1/burst=1 the
// first POST /v1/jobs passes (burst token) and the next ones are 429 + Retry-After.
func TestRateLimitSubmitJobs(t *testing.T) {
	s := newRateLimitedServer(t, 1, 1)
	req := job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	}

	// First submit consumes the single burst token → accepted (200).
	first := do(t, s, http.MethodPost, "/v1/jobs", testToken, req)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first submit status=%d, want 200", first.StatusCode)
	}
	first.Body.Close()

	// The next several submits (no token left) must be rate-limited.
	var limited int
	for i := 0; i < 4; i++ {
		resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, req)
		if resp.StatusCode == http.StatusTooManyRequests {
			limited++
			if ra := resp.Header.Get("Retry-After"); ra == "" {
				t.Errorf("429 response missing Retry-After header")
			}
			var eb errorBody
			decode(t, resp, &eb)
			if eb.Error != "rate limited" {
				t.Errorf("429 error body = %+v, want error=\"rate limited\"", eb)
			}
		} else {
			resp.Body.Close()
		}
	}
	if limited == 0 {
		t.Fatalf("expected at least one 429 after exhausting the burst, got none")
	}
}

// TestRateLimitReadsNotLimited (E17) proves only submit writes are gated:
// GET /v1/jobs (a read) is never rate-limited even when the caller's bucket is
// empty.
func TestRateLimitReadsNotLimited(t *testing.T) {
	s := newRateLimitedServer(t, 1, 1)
	// Many reads in a row must all pass (reads are not in isSubmitPath).
	for i := 0; i < 10; i++ {
		resp := do(t, s, http.MethodGet, "/v1/jobs", testToken, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/jobs #%d status=%d, want 200 (reads never rate-limited)", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestRateLimitSubActionsNotLimited (E17) proves isSubmitPath excludes job
// sub-actions: POST /v1/jobs/{id}/cancel is not the submit path, so it is never
// rate-limited (here it 404s for an unknown id but is NOT 429).
func TestRateLimitSubActionsNotLimited(t *testing.T) {
	s := newRateLimitedServer(t, 1, 1)
	// Drain the bucket with one accepted submit so any rate gating would now fire.
	first := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	first.Body.Close()
	// A cancel on an unknown id is NOT the submit path: must be 404 (handler), never 429.
	for i := 0; i < 5; i++ {
		resp := do(t, s, http.MethodPost, "/v1/jobs/nope/cancel", testToken, nil)
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("cancel sub-action was rate-limited (429); isSubmitPath must exclude it")
		}
		resp.Body.Close()
	}
}

// TestRateLimitMiddlewareSeesCallerID (E17) proves the rux group middleware order
// runs authMiddleware BEFORE rateLimitMiddleware: rateLimitMiddleware reads the
// caller id that authMiddleware set. We use a multi-caller config and assert the
// limiter is created under the AUTH-RESOLVED caller id ("ci"), not "" — which
// could only happen if rate ran before auth.
func TestRateLimitMiddlewareSeesCallerID(t *testing.T) {
	root := t.TempDir()
	const ciToken = "ci-secret"
	cfg := &config.Config{
		Server: config.ServerConfig{
			Callers: []config.CallerConfig{
				{ID: "ci", Token: ciToken, RateLimit: 1, RateBurst: 1},
			},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	// token "" + the callers slice gives caller id "ci" on the ci token. allowEmpty
	// false (callers present), so New uses the callers set.
	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	s := New(&cfg.Server, "", false, jobs, jobsEng, projects, agents, nil, nil, nil, nil)

	req := job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	}
	resp := do(t, s, http.MethodPost, "/v1/jobs", ciToken, req)
	resp.Body.Close()

	// The limiter must have been registered under the auth-resolved caller id "ci"
	// (proves auth ran before rate-limit and set the ctx caller id it read).
	s.limMu.Lock()
	_, hasCI := s.limiters["ci"]
	_, hasEmpty := s.limiters[""]
	s.limMu.Unlock()
	if !hasCI {
		t.Fatalf("expected a limiter under caller id \"ci\" (auth must run before rate-limit)")
	}
	if hasEmpty {
		t.Fatalf("found a limiter under empty caller id: rate-limit ran before auth set caller_id")
	}
}

// TestLimiterForHotReload (E17, design §7.4) proves limiterFor re-syncs an
// existing limiter's Limit/Burst to the latest config on every call (SetLimit /
// SetBurst), so a SIGHUP rate change takes effect without rebuilding the map.
func TestLimiterForHotReload(t *testing.T) {
	s := newRateLimitedServer(t, 5, 10)

	// First call builds the limiter at rps=5, burst=10.
	lim := s.limiterFor("ci", 5, 10)
	if float64(lim.Limit()) != 5 || lim.Burst() != 10 {
		t.Fatalf("initial limiter = (%v, %d), want (5, 10)", lim.Limit(), lim.Burst())
	}

	// A subsequent call with new config mutates the SAME limiter in place.
	lim2 := s.limiterFor("ci", 20, 40)
	if lim2 != lim {
		t.Fatalf("limiterFor returned a different limiter instance; should mutate in place")
	}
	if float64(lim2.Limit()) != 20 || lim2.Burst() != 40 {
		t.Fatalf("after hot-reload limiter = (%v, %d), want (20, 40)", lim2.Limit(), lim2.Burst())
	}

	// burst <= 0 defaults to max(1, ceil(rps)).
	lim3 := s.limiterFor("ci2", 2.3, 0)
	if lim3.Burst() != 3 {
		t.Fatalf("default burst for rps=2.3 = %d, want 3", lim3.Burst())
	}
	// Sanity: rate.Limit type matches what we expect.
	if rate.Limit(2.3) != lim3.Limit() {
		t.Fatalf("limiter limit type mismatch: %v", lim3.Limit())
	}
}

// TestIsSubmitPath unit-checks the exact-path matcher: only POST /v1/jobs and
// POST /v1/workflows are submit paths; reads and sub-actions are not.
func TestIsSubmitPath(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/v1/jobs", true},
		{http.MethodPost, "/v1/workflows", true},
		{http.MethodGet, "/v1/jobs", false},
		{http.MethodGet, "/v1/workflows", false},
		{http.MethodPost, "/v1/jobs/abc/cancel", false},
		{http.MethodPost, "/v1/jobs/abc/interactions", false},
		{http.MethodPost, "/v1/jobs/abc/interactions/1/answer", false},
		{http.MethodPost, "/v1/workflows/abc/cancel", false},
		{http.MethodGet, "/v1/jobs/abc", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		c := &rux.Context{}
		c.Req = req
		if got := isSubmitPath(c); got != tc.want {
			t.Errorf("isSubmitPath(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
