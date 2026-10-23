package commands

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// gofer-02fu: an AskUserQuestion PreToolUse needs the session hub, so it must not
// take the hub-less command-memory path (which ran it with a nil API and crashed).
func TestPreToolUseFastPathExcludesAskUserQuestion(t *testing.T) {
	if !preToolUseFastPath(hookrelay.Payload{Event: "PreToolUse", ToolName: "Bash"}) {
		t.Fatal("a shell PreToolUse keeps the fast path")
	}
	if preToolUseFastPath(hookrelay.Payload{Event: "PreToolUse", ToolName: hookrelay.AskUserQuestionTool}) {
		t.Fatal("AskUserQuestion must take the full hook path")
	}
}

// TestPreToolUseFastPathDeliversUrgentMail: the hub-less PreToolUse path must still reach
// the hub for urgent mail. It ran hookrelay with a nil API, which silently disabled the
// urgent take — found in the first live test of the mailbox, not by the hookrelay unit
// tests (they inject a fake API).
func TestPreToolUseFastPathDeliversUrgentMail(t *testing.T) {
	// The production bound is 300ms; a loaded Windows host has missed it against the stub
	// hub (claims=0). Raise it here so the test checks delivery, not latency (G052).
	prev := preToolUseMailDeadline
	preToolUseMailDeadline = wait.Timeout(t, 10*time.Second)
	t.Cleanup(func() { preToolUseMailDeadline = prev })
	var claims int
	stubAuthServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/s1/mail/claim":
			claims++
			_, _ = w.Write([]byte(`{"mails":[{"id":7,"thread_id":"t7","from":{"display":"会话 a"},"urgent":true,"body":"stop the build"}],"remaining":0}`))
		case "/v1/sessions/s1/mail/confirm":
			_, _ = w.Write([]byte(`{"confirmed":[7]}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = stdout })
	runErr := runPreToolUseHook(hookrelay.Payload{Agent: "claude", Event: "PreToolUse", ToolName: "Bash",
		SessionID: "s1", Cwd: t.TempDir()}, time.Now())
	_ = w.Close()
	os.Stdout = stdout
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("pre tool use: %v", runErr)
	}
	if claims != 1 || !strings.Contains(string(out), `gofer-mail id=\"7\"`) || !strings.Contains(string(out), "stop the build") {
		t.Fatalf("claims=%d, want the urgent letter injected; output:\n%s", claims, out)
	}
}
