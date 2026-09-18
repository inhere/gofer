package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobRunVerifyFlags: `job run` takes ONE verify command as a shell-words string
// (--verify 'go test ./...') plus its own timeout, and --no-verify turns a project
// default off for this job. All three map onto the wire request the server admits.
func TestJobRunVerifyFlags(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	app := NewApp("test")
	var got job.JobRequest
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, nil)
		if err != nil {
			return err
		}
		got = req
		return nil
	}

	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec",
		"--verify", `bash -lc 'go test ./...'`, "--verify-timeout", "120", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	want := []string{"bash", "-lc", "go test ./..."}
	if len(got.Verify) != len(want) {
		t.Fatalf("JobRequest.Verify = %#v, want %#v", got.Verify, want)
	}
	for i := range want {
		if got.Verify[i] != want[i] {
			t.Fatalf("JobRequest.Verify[%d] = %q, want %q (full: %#v)", i, got.Verify[i], want[i], got.Verify)
		}
	}
	if got.VerifyTimeoutSec != 120 {
		t.Fatalf("JobRequest.VerifyTimeoutSec = %d, want 120", got.VerifyTimeoutSec)
	}
	if got.NoVerify {
		t.Fatalf("--no-verify must stay off when only --verify was given: %+v", got)
	}

	jobRunOpts = jobRunFlags{} // gcli does not clear an unset flag: start clean
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--no-verify", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if !got.NoVerify {
		t.Fatalf("--no-verify did not reach the request: %+v", got)
	}
	if len(got.Verify) != 0 {
		t.Fatalf("--no-verify alone must not invent a verify argv: %#v", got.Verify)
	}

	// An unbalanced quote is a usage error: the CLI must refuse to guess an argv
	// rather than sending a command nobody wrote.
	jobRunOpts = jobRunFlags{}
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		_, err := buildJobRunRequest(c, nil)
		return err
	}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--verify", `bash -lc 'go test`, "--", "go", "version"}); code == 0 {
		t.Fatal("an unbalanced quote in --verify must fail the run, not be silently accepted")
	}
}

// TestJobShowPrintsVerify: `job show` reports the verify step's outcome in one line —
// status plus the detail a reader acts on (the exit code for a failure, the duration
// for a pass, the reason for a skip) — and prints nothing when the job had no step.
func TestJobShowPrintsVerify(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	cases := []struct {
		name   string
		verify *job.VerifyResult
		want   string
	}{
		{"passed", &job.VerifyResult{Status: job.VerifyPassed, DurationMs: 3200}, "passed (3.2s)"},
		{"failed", &job.VerifyResult{Status: job.VerifyFailed, ExitCode: 1, DurationMs: 12300}, "failed (exit 1, 12.3s)"},
		{"timeout", &job.VerifyResult{Status: job.VerifyTimeout, ExitCode: -1, DurationMs: 600000}, "timeout (exit -1, 600.0s)"},
		{"skipped", &job.VerifyResult{Status: job.VerifySkipped, Reason: "agent failed"}, "skipped (agent failed)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(job.JobResult{
					ID: "job-verify", ProjectKey: "self", Status: job.StatusFailed, Verify: tc.verify,
				})
			}))
			defer srv.Close()

			out := captureOutput(t, func() {
				if code := NewApp("test").Run([]string{"job", "show", "job-verify", "--server", srv.URL}); code != 0 {
					t.Fatalf("app.Run exit code=%d", code)
				}
			})
			if !strings.Contains(out, "verify:") {
				t.Fatalf("job show must report a verify line:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("job show verify line missing %q:\n%s", tc.want, out)
			}
		})
	}

	// A job without a verify step prints no verify line at all.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-plain", ProjectKey: "self", Status: job.StatusDone})
	}))
	defer srv.Close()
	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "show", "job-plain", "--server", srv.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if strings.Contains(out, "verify:") {
		t.Fatalf("a job without a verify step must not print a verify line:\n%s", out)
	}
}
