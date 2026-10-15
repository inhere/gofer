package commands

import (
	"strings"
	"testing"
)

// isolateNotifyEnv clears the agent-session env keys so the test machine's own
// terminal (Claude Code / Codex export theirs) cannot satisfy an auto-detected
// notify target.
func isolateNotifyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GOFER_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID"} {
		t.Setenv(k, "")
	}
}

// TestJobRunNotifyFlagsMapToRequest: `--notify` / `--notify-session` (gofer-3db4)
// bind onto the run flags and resolve into the request's Notify + NotifySession,
// with a clear usage error when there is nobody to tell.
func TestJobRunNotifyFlagsMapToRequest(t *testing.T) {
	isolateNotifyEnv(t)

	t.Run("neither flag means no notification, byte for byte", func(t *testing.T) {
		jobRunOpts = jobRunFlags{}
		t.Cleanup(func() { jobRunOpts = jobRunFlags{} })
		_, _, _, _, _, _, _ = parseRun(t, []string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "version"})
		notify, sid, err := resolveJobRunNotify(nil)
		if err != nil || notify || sid != "" {
			t.Fatalf("notify=%v sid=%q err=%v, want off", notify, sid, err)
		}
	})

	t.Run("--notify-session names the target and implies the switch", func(t *testing.T) {
		jobRunOpts = jobRunFlags{}
		t.Cleanup(func() { jobRunOpts = jobRunFlags{} })
		_, _, _, _, _, _, _ = parseRun(t, []string{"job", "run", "-p", "self", "-a", "exec", "--notify-session", "sid-target", "--", "go", "version"})
		if jobRunOpts.notifySession != "sid-target" {
			t.Fatalf("--notify-session bound %q", jobRunOpts.notifySession)
		}
		notify, sid, err := resolveJobRunNotify(nil)
		if err != nil || !notify || sid != "sid-target" {
			t.Fatalf("notify=%v sid=%q err=%v, want sid-target", notify, sid, err)
		}
	})

	t.Run("--notify falls back to the source session", func(t *testing.T) {
		jobRunOpts = jobRunFlags{}
		t.Cleanup(func() { jobRunOpts = jobRunFlags{} })
		_, _, _, _, _, _, _ = parseRun(t, []string{"job", "run", "-p", "self", "-a", "exec", "--notify", "--source-session-id", "sid-src", "--", "go", "version"})
		if !jobRunOpts.notify {
			t.Fatal("--notify did not bind")
		}
		notify, sid, err := resolveJobRunNotify(nil)
		if err != nil || !notify || sid != "sid-src" {
			t.Fatalf("notify=%v sid=%q err=%v, want the source session", notify, sid, err)
		}
	})

	t.Run("--notify without any session is a usage error", func(t *testing.T) {
		jobRunOpts = jobRunFlags{}
		t.Cleanup(func() { jobRunOpts = jobRunFlags{} })
		_, _, _, _, _, _, _ = parseRun(t, []string{"job", "run", "-p", "self", "-a", "exec", "--notify", "--", "go", "version"})
		_, _, err := resolveJobRunNotify(nil)
		if err == nil {
			t.Fatal("--notify with no session must be refused, not silently dropped")
		}
		for _, want := range []string{"--notify-session", "GOFER_SESSION_ID"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q must name %s", err, want)
			}
		}
	})
}
