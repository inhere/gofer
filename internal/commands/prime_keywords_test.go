package commands

import (
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

// TestPrimeKeywordsFromFlag (gofer-11tz): the explicit flag wins over
// GOFER_PRIME_KEYWORDS, which is only the default — the variable stays in the shell of
// whoever exported it, so on its own it would silently narrow the prime of every later
// job dispatched from that terminal.
func TestPrimeKeywordsFromFlag(t *testing.T) {
	t.Setenv(primeKeywordsEnv, "env-a,env-b")
	assert.Eq(t, []string{"flag-a", "flag-b"}, primeKeywordsFromFlag("flag-a, flag-b"))
	assert.Eq(t, []string{"env-a", "env-b"}, primeKeywordsFromFlag(""))

	t.Setenv(primeKeywordsEnv, "")
	assert.Nil(t, primeKeywordsFromFlag("  "))
}

// TestJobRunPrimeKeywordsFlag: the task keywords reach the submitted JobRequest, from
// the flag or, when it is absent, from the environment.
func TestJobRunPrimeKeywordsFlag(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })
	t.Setenv(primeKeywordsEnv, "env-a")

	app := NewApp("test")
	var got job.JobRequest
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, nil)
		got = req
		return err
	}
	run := func(extra ...string) {
		jobRunOpts = jobRunFlags{} // gcli does not clear an unset flag: start clean
		args := append([]string{"job", "run", "-p", "self", "-a", "claude", "--prompt", "x"}, extra...)
		if code := app.Run(args); code != 0 {
			t.Fatalf("exit %d", code)
		}
	}

	run()
	assert.Eq(t, []string{"env-a"}, got.PrimeKeywords)

	run("--prime-keywords", "job, rules")
	assert.Eq(t, []string{"job", "rules"}, got.PrimeKeywords)
}

// TestRepoPrimeKeywordsSelectMaterial (gofer-11tz): `repo prime --keywords` (and its
// GOFER_PRIME_KEYWORDS default) reaches the memory selection of the session opening —
// the memory whose when.keywords name the session's subsystem is marked （相关） and
// ranked before the rest.
func TestRepoPrimeKeywordsSelectMaterial(t *testing.T) {
	t.Setenv("GOFER_SERVER_ADDR", "") // the local tracker only, never this machine's server
	t.Setenv(primeKeywordsEnv, "")

	dir := t.TempDir()
	s, _, err := tracker.Init(dir, "prime", true)
	assert.NoErr(t, err)
	assert.NoErr(t, s.UpdateMemories(func([]tracker.Memory) ([]tracker.Memory, error) {
		return []tracker.Memory{
			{Key: "hit-note", Content: "hit body", UpdatedAt: "2026-10-01T00:00:00Z",
				MemoryMeta: tracker.MemoryMeta{Summary: "HIT-NOTE", When: &tracker.MemoryWhen{Keywords: []string{"prime-11tz"}}}},
			{Key: "other-note", Content: "other body", UpdatedAt: "2026-10-09T00:00:00Z",
				MemoryMeta: tracker.MemoryMeta{Summary: "OTHER-NOTE"}},
		}, nil
	}))
	t.Chdir(dir)

	app := NewApp("test")
	prime := func(args ...string) string {
		return captureOutput(t, func() {
			if code := app.Run(args); code != 0 {
				t.Fatalf("repo prime exit code=%d", code)
			}
		})
	}

	// Nothing named: the plain cwd-scoped prime says nothing about relevance.
	plain := prime("repo", "prime")
	assert.NotContains(t, plain, "（相关）")

	// The flag names the subsystem of the session.
	out := prime("repo", "prime", "--keywords", "prime-11tz")
	assert.StrContains(t, out, "hit-note（相关） · HIT-NOTE")
	assert.True(t, strings.Index(out, "hit-note") < strings.Index(out, "other-note"), out)

	// The environment variable is the default of the same flag.
	t.Setenv(primeKeywordsEnv, "prime-11tz")
	assert.Eq(t, out, prime("repo", "prime"))
}
