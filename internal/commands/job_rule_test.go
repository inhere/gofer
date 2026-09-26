package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestAgentRuleCommands drives the `agent rule` surface in LOCAL mode — no server, no
// HTTP — over a temp config dir: set a rule from a file, list it, show its text, then
// remove it and watch it disappear. It also pins the failing exits (bad name, missing
// -f, unknown rule) that make the command usable in a script.
func TestAgentRuleCommands(t *testing.T) {
	isolateConfigEnv(t)
	// The local path is what this test covers; a client-mode env in the shell must not
	// silently route it to a server.
	t.Setenv(config.EnvRunMode, "server")

	cfgPath := writeRawConfig(t, "# gofer rule CLI test\n")
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
	agentRuleOpts.local, agentRuleOpts.file = false, ""
	t.Cleanup(func() { agentRuleOpts.local, agentRuleOpts.file = false, "" })

	const body = "NEVER push; apply_patch only.\n"
	src := filepath.Join(t.TempDir(), "house.md")
	if err := os.WriteFile(src, []byte("---\ndescription: house discipline\n---\n\n"+body), 0o644); err != nil {
		t.Fatalf("write rule source: %v", err)
	}

	// run drives the real gcli app; -c is bound per subcommand, so it may sit after
	// the command name.
	run := func(args ...string) (string, int) {
		t.Helper()
		// gcli keeps the bound struct between invocations: a flag left over from the
		// previous `run` would silently satisfy the next one, so each call starts clean.
		agentRuleOpts.file = ""
		args = append(args, "-c", cfgPath)
		var code int
		out := captureOutput(t, func() { code = NewApp("test").Run(args) })
		return out, code
	}

	if out, code := run("agent", "rule", "set", "house-rules", "-f", src); code != 0 {
		t.Fatalf("set exit code=%d, out=%s", code, out)
	} else if !strings.Contains(out, "wrote rule house-rules") {
		t.Fatalf("set output should name the rule, got: %s", out)
	}

	out, code := run("agent", "rule", "ls")
	if code != 0 {
		t.Fatalf("ls exit code=%d, out=%s", code, out)
	}
	if !strings.Contains(out, "house-rules") || !strings.Contains(out, "house discipline") {
		t.Fatalf("ls should list the rule with its description, got: %s", out)
	}

	out, code = run("agent", "rule", "show", "house-rules")
	if code != 0 {
		t.Fatalf("show exit code=%d, out=%s", code, out)
	}
	if !strings.Contains(out, body) {
		t.Fatalf("show should print the rule text, got: %s", out)
	}
	if !strings.Contains(out, "sha256:") {
		t.Fatalf("show should print the digest the job row records, got: %s", out)
	}

	// A rule name must obey the grammar (lower-case letters, digits, '-'): a bad one
	// is refused rather than stored under a name no binding could ever use.
	if out, code := run("agent", "rule", "set", "House Rules", "-f", src); code == 0 {
		t.Fatalf("set with an invalid name should fail, out=%s", out)
	}
	// -f is required: a rule has no useful default text.
	if out, code := run("agent", "rule", "set", "no-source"); code == 0 {
		t.Fatalf("set without -f should fail, out=%s", out)
	}
	// An unknown rule is a failing read, not an empty print.
	if out, code := run("agent", "rule", "show", "ghost"); code == 0 {
		t.Fatalf("show of an unknown rule should exit non-zero, out=%s", out)
	}

	if out, code := run("agent", "rule", "rm", "house-rules"); code != 0 {
		t.Fatalf("rm exit code=%d, out=%s", code, out)
	}
	out, code = run("agent", "rule", "ls")
	if code != 0 {
		t.Fatalf("ls after rm exit code=%d, out=%s", code, out)
	}
	if strings.Contains(out, "house-rules") || !strings.Contains(out, "(no rules)") {
		t.Fatalf("ls after rm should be empty, got: %s", out)
	}
}

// TestJobRunEnvFlag covers F-f: `--env K=V` is repeatable and rides the request in
// order, a value without '=' is a usage error (no job is submitted at all), and the
// help text warns that the value is stored with the job.
func TestJobRunEnvFlag(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv(config.EnvRunMode, "server")
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	jobRunOpts.env = nil
	t.Cleanup(func() { jobRunOpts.env = nil })

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

	run := func(extra ...string) (int, string) {
		t.Helper()
		jobRunOpts.env = nil
		args := append([]string{"job", "run", "-p", "self", "-a", "exec"}, extra...)
		args = append(args, "--server", ts.URL, "--", "go", "version")
		var code int
		out := captureOutput(t, func() { code = NewApp("test").Run(args) })
		return code, out
	}

	if code, out := run("--env", "A=1", "--env", "B=2"); code != 0 {
		t.Fatalf("run with --env exit code=%d, out=%s", code, out)
	}
	mu.Lock()
	got := bodies[len(bodies)-1]
	mu.Unlock()
	if len(got.Env) != 2 || got.Env["A"] != "1" || got.Env["B"] != "2" {
		t.Fatalf("submitted env = %v, want A=1 B=2", got.Env)
	}

	// An assignment without '=' is refused locally: nothing is submitted, so a typo
	// can never run a job without the variable the caller expected.
	before := len(bodies)
	if code, _ := run("--env", "NOPE"); code == 0 {
		t.Fatal("--env without '=' must be a usage error")
	}
	if len(bodies) != before {
		t.Fatalf("a rejected --env still submitted a job (%d → %d)", before, len(bodies))
	}
	if code, _ := run("--env", "=1"); code == 0 {
		t.Fatal("--env with an empty key must be a usage error")
	}

	// The help has to say where the value ends up: request_json is persisted, so a
	// secret must not be passed this way (JOB-06② will bring a reference syntax).
	out := captureOutput(t, func() { _ = NewApp("test").Run([]string{"job", "run", "--help"}) })
	if !strings.Contains(out, "never pass secrets here") {
		t.Fatalf("--env help must warn about secrets, got:\n%s", out)
	}
}

// TestJobShowRulesLine pins the `rules:` line `job show` prints: each injected rule
// with the 12-char prefix of the sha256 that ran, and NO line at all when the job
// carried none (an empty list is not a rule set).
func TestJobShowRulesLine(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	got := formatJobRules([]job.RuleRef{{Name: "house-rules", SHA256: sha}, {Name: "gofer-repo"}})
	if got != "house-rules@abababababab, gofer-repo" {
		t.Fatalf("formatJobRules = %q, want house-rules@abababababab, gofer-repo", got)
	}
	if got := formatJobRules(nil); got != "" {
		t.Fatalf("formatJobRules(nil) = %q, want an empty line (nothing printed)", got)
	}
}
