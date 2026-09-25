package acp

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// F14 (2026-09-25): an acp-agent may start with the `env` block of claude's user
// settings file layered in. These tests drive the REAL runner against the repo's fake
// ACP agent and read what the child process actually received (acptest's --env-print),
// because "the agent could authenticate" is a property of the launched process, not of
// a helper's return value.
//
// The names are the real ones — the host's claude keeps exactly ANTHROPIC_API_KEY /
// ANTHROPIC_BASE_URL in ~/.claude/settings.json — and the value is a sentinel no
// environment would carry by accident.
const (
	claudeKey      = "ANTHROPIC_API_KEY"
	claudeBaseURL  = "ANTHROPIC_BASE_URL"
	claudeKeyValue = "sk-ant-f14-settings-value"
	claudeURLValue = "https://relay.f14.invalid"
)

// runClaudeACPJob runs one prompt turn against the fake ACP agent with the F14 switch
// ON, and returns the run result plus everything the agent wrote to stderr (its
// environment report mixed with its own logs and the runner's compact event lines).
//
// The deny/allow pair is the job service's own default (job.DefaultJobEnvDeny plus an
// empty project allow list), so the environment here is the one a real job gets.
func runClaudeACPJob(t *testing.T, jobEnv map[string]string, envPrint []string) (runner.Result, string) {
	t.Helper()
	stderr := &syncBuffer{}
	res := (&Runner{}).Run(context.Background(), runner.Request{
		JobID:   "job-f14",
		Command: testcmd.Path(t),
		Args:    acptest.CmdArgs(acptest.Options{EnvPrint: envPrint}),
		WorkDir: t.TempDir(),
		Env:     jobEnv,
		EnvDeny: []string{"GOFER_TOKEN", "GOFER_SERVER_TOKEN", "GOFER_WORKER_TOKEN", config.EnvConfigDir},
		Stdout:  io.Discard,
		Stderr:  stderr,
		ACP: &runner.ACPRequest{
			Prompt:            "hello",
			ResultDir:         t.TempDir(),
			ClaudeSettingsEnv: true,
		},
	})
	return res, stderr.String()
}

// writeClaudeSettings puts a settings.json with the given `env` block into dir and
// points CLAUDE_CONFIG_DIR at it — the F14 resolution path, and the reason no test ever
// reads the developer's real ~/.claude (the home fallback is never reached).
func writeClaudeSettings(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, claudeSettingsFile), []byte(contents), 0o600); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	t.Setenv(claudeConfigDirEnv, dir)
}

// agentEnv reads one `acptest: env KEY=VALUE` line the fake agent printed at start; ok
// is false when the key was never reported at all (an absent line is a broken script,
// an empty value is the "not set" answer).
func agentEnv(t *testing.T, stderr, key string) (string, bool) {
	t.Helper()
	prefix := "acptest: env " + key + "="
	for _, line := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return rest, true
		}
	}
	return "", false
}

// TestACPClaudeSettingsEnvInjected is the F14 headline: keys that exist only in
// claude's settings file reach the agent process, which is what makes claude-acp
// authenticate without a second copy of the key in gofer's config.
func TestACPClaudeSettingsEnvInjected(t *testing.T) {
	unsetEnv(t, claudeKey, claudeBaseURL)
	writeClaudeSettings(t, `{"env":{"`+claudeKey+`":"`+claudeKeyValue+`","`+claudeBaseURL+`":"`+claudeURLValue+`"}}`)

	res, stderr := runClaudeACPJob(t, nil, []string{claudeKey, claudeBaseURL})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: exit=%d err=%v", res.ExitCode, res.Err)
	}
	if res.StopReason != acp.StopEndTurn {
		t.Fatalf("stop_reason = %q, want %q", res.StopReason, acp.StopEndTurn)
	}
	for key, want := range map[string]string{claudeKey: claudeKeyValue, claudeBaseURL: claudeURLValue} {
		got, ok := agentEnv(t, stderr, key)
		if !ok {
			t.Fatalf("the agent never reported %s:\n%s", key, stderr)
		}
		if got != want {
			t.Errorf("agent env %s = %q, want the settings value %q", key, got, want)
		}
	}
}

// TestACPClaudeSettingsEnvDoesNotOverride: the settings file is a FALLBACK. A key the
// job's own env defines stays, and so does one only the gofer process carries — the
// operator who exported a variable meant it to win over a file they may not remember.
func TestACPClaudeSettingsEnvDoesNotOverride(t *testing.T) {
	unsetEnv(t, claudeKey, claudeBaseURL)
	writeClaudeSettings(t, `{"env":{"`+claudeKey+`":"`+claudeKeyValue+`","`+claudeBaseURL+`":"`+claudeURLValue+`"}}`)
	t.Setenv(claudeBaseURL, "https://from-process.invalid")

	const fromJobEnv = "sk-ant-from-job-env"
	res, stderr := runClaudeACPJob(t, map[string]string{claudeKey: fromJobEnv}, []string{claudeKey, claudeBaseURL})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: exit=%d err=%v", res.ExitCode, res.Err)
	}
	if got, _ := agentEnv(t, stderr, claudeKey); got != fromJobEnv {
		t.Errorf("agent env %s = %q, want the job env value %q (the settings file must not override it)", claudeKey, got, fromJobEnv)
	}
	if got, _ := agentEnv(t, stderr, claudeBaseURL); got != "https://from-process.invalid" {
		t.Errorf("agent env %s = %q, want the process environment value (the settings file must not override it)", claudeBaseURL, got)
	}
}

// TestACPClaudeSettingsEnvMissingFileIsHarmless: every way the file can be unusable —
// absent, malformed, or without an env block — is a warning and an ordinary run. The
// switch is on by default for claude-acp, so a host that authenticates the adapter
// some other way must not see its jobs fail over a file it never wrote.
func TestACPClaudeSettingsEnvMissingFileIsHarmless(t *testing.T) {
	unsetEnv(t, claudeKey)

	cases := []struct {
		name     string
		contents string // "" = write no file at all
	}{
		{name: "missing_file"},
		{name: "malformed_json", contents: "{ this is not json"},
		{name: "no_env_block", contents: `{"model":"opus"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh CLAUDE_CONFIG_DIR per case, so "no file" really has none (the
			// previous subtest's cleanup restored the variable, not the directory).
			t.Setenv(claudeConfigDirEnv, t.TempDir())
			if tc.contents != "" {
				writeClaudeSettings(t, tc.contents)
			}

			res, stderr := runClaudeACPJob(t, nil, []string{claudeKey})
			if res.Err != nil || res.ExitCode != 0 {
				t.Fatalf("run failed: exit=%d err=%v (an unusable settings file must not fail the job)", res.ExitCode, res.Err)
			}
			if res.StopReason != acp.StopEndTurn {
				t.Fatalf("stop_reason = %q, want %q", res.StopReason, acp.StopEndTurn)
			}
			if got, ok := agentEnv(t, stderr, claudeKey); !ok || got != "" {
				t.Errorf("agent env %s = %q (reported=%v), want it unset", claudeKey, got, ok)
			}
		})
	}
}

// TestACPClaudeSettingsEnvRespectsDenylist: SEC-01 still decides what a job process may
// inherit. The settings file is not a side door for gofer's own credentials — a denied
// key is dropped even though it arrives through the "explicit" extra-env half, which
// would otherwise pass through verbatim.
func TestACPClaudeSettingsEnvRespectsDenylist(t *testing.T) {
	unsetEnv(t, claudeKey, "GOFER_TOKEN")
	writeClaudeSettings(t, `{"env":{"GOFER_TOKEN":"gjt-f14-must-not-travel","`+claudeKey+`":"`+claudeKeyValue+`"}}`)

	res, stderr := runClaudeACPJob(t, nil, []string{"GOFER_TOKEN", claudeKey})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: exit=%d err=%v", res.ExitCode, res.Err)
	}
	if got, ok := agentEnv(t, stderr, "GOFER_TOKEN"); !ok || got != "" {
		t.Errorf("agent env GOFER_TOKEN = %q (reported=%v), want it dropped by the deny list", got, ok)
	}
	if got, _ := agentEnv(t, stderr, claudeKey); got != claudeKeyValue {
		t.Errorf("agent env %s = %q, want %q — the deny list must only drop the denied key", claudeKey, got, claudeKeyValue)
	}
}

// TestACPClaudeSettingsEnvOffByDefault: an agent that does not turn the switch on gets
// nothing from the file, even when the file is there and readable. The inheritance is
// an opt-in (the built-in claude-acp template opts in), never a global behaviour change.
func TestACPClaudeSettingsEnvOffByDefault(t *testing.T) {
	unsetEnv(t, claudeKey)
	writeClaudeSettings(t, `{"env":{"`+claudeKey+`":"`+claudeKeyValue+`"}}`)

	stderr := &syncBuffer{}
	res := (&Runner{}).Run(context.Background(), runner.Request{
		JobID:   "job-f14-off",
		Command: testcmd.Path(t),
		Args:    acptest.CmdArgs(acptest.Options{EnvPrint: []string{claudeKey}}),
		WorkDir: t.TempDir(),
		Stdout:  io.Discard,
		Stderr:  stderr,
		ACP: &runner.ACPRequest{
			Prompt:    "hello",
			ResultDir: t.TempDir(),
		},
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: exit=%d err=%v", res.ExitCode, res.Err)
	}
	if got, ok := agentEnv(t, stderr.String(), claudeKey); !ok || got != "" {
		t.Errorf("agent env %s = %q (reported=%v), want unset without the switch", claudeKey, got, ok)
	}
}

// unsetEnv removes the named variables from THIS process's environment for the rest of
// the test. Without it these tests are not isolated: a developer machine with a real
// ANTHROPIC_API_KEY exported would make "the settings value reached the agent" fail (or
// pass against the wrong value), because presence in the process environment is exactly
// what the merge treats as "already set".
func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if old, ok := os.LookupEnv(name); ok {
			t.Cleanup(func() { _ = os.Setenv(name, old) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(name) })
		}
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

// syncBuffer is a bytes.Buffer safe to read while the agent process is still writing:
// os/exec copies the child's stderr on its own goroutine, and the runner writes its
// compact event lines into the same sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
