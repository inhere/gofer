package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobRunSkillFlags covers JOB-10's two submission switches: --skill is
// repeatable and rides the request in order, --no-skills sets the kill switch, and
// giving both keeps BOTH on the wire (the CLI reports what it was told; resolving
// the precedence is the server's job).
func TestJobRunSkillFlags(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv(config.EnvRunMode, "server")
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	jobRunOpts.skill, jobRunOpts.noSkills = nil, false
	t.Cleanup(func() { jobRunOpts.skill, jobRunOpts.noSkills = nil, false })

	var (
		mu     sync.Mutex
		bodies []job.JobRequest
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var req job.JobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode job request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, req)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-1", Status: job.StatusQueued})
	}))
	defer ts.Close()

	// run submits one job with the given skill flags and returns the body the server
	// received. The bound vars are reset first: gcli APPENDS to a gcli.Strings, so a
	// previous case's flags would otherwise leak into the next one.
	run := func(extra ...string) job.JobRequest {
		t.Helper()
		jobRunOpts.skill, jobRunOpts.noSkills = nil, false
		args := append([]string{"job", "run", "-p", "self", "-a", "exec"}, extra...)
		args = append(args, "--server", ts.URL, "--", "go", "version")
		if code := NewApp("test").Run(args); code != 0 {
			t.Fatalf("app.Run(%v) exit code=%d", extra, code)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(bodies) == 0 {
			t.Fatal("no job request was submitted")
		}
		return bodies[len(bodies)-1]
	}

	got := run("--skill", "a", "--skill", "b")
	if strings.Join(got.Skills, ",") != "a,b" {
		t.Fatalf("submitted skills = %v, want [a b] in order", got.Skills)
	}
	if got.NoSkills {
		t.Fatal("no_skills must stay false when --no-skills is absent")
	}

	got = run("--no-skills")
	if !got.NoSkills {
		t.Fatal("submitted no_skills = false, want true")
	}
	if len(got.Skills) != 0 {
		t.Fatalf("submitted skills = %v, want none", got.Skills)
	}

	// Both flags at once: neither is dropped.
	got = run("--skill", "a", "--skill", "b", "--no-skills")
	if strings.Join(got.Skills, ",") != "a,b" || !got.NoSkills {
		t.Fatalf("both flags together lost one: skills=%v no_skills=%v", got.Skills, got.NoSkills)
	}
}
