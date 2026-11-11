package acp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// A granted secret echoed anywhere in an acp.jsonl event (prompt, message, nested
// tool content, a value that needs JSON escaping) is written masked.
func TestEventWriterMasksSecretsBeforeEncoding(t *testing.T) {
	const secret = "tok\"en\nvalue-123"
	dir := t.TempDir()
	w, err := openEventWriter(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	w.setMask(func(s string) string { return strings.ReplaceAll(s, secret, "***") })
	w.write(map[string]any{"t": "prompt", "text": "use " + secret})
	w.write(map[string]any{"t": "tool_call", "content": []any{map[string]any{"type": "content", "text": secret}}, "n": 7})
	_ = w.Close()
	b, err := os.ReadFile(filepath.Join(dir, "artifacts", ACPFileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "value-123") || strings.Contains(got, `tok\"en`) {
		t.Fatalf("acp.jsonl leaked the secret: %s", got)
	}
	if !strings.Contains(got, `"text":"use ***"`) || !strings.Contains(got, `"n":7`) {
		t.Fatalf("acp.jsonl = %s", got)
	}
}

// End to end through the runner: the prompt carries the secret, Request.Mask hides it.
func TestACPRunnerMasksSecretsInEventStream(t *testing.T) {
	const secret = "SECRET-PROMPT-VALUE-42"
	resultDir := t.TempDir()
	res := (&Runner{}).Run(context.Background(), runner.Request{
		JobID:   "job-mask",
		Command: testcmd.Path(t),
		Args:    acptest.CmdArgs(acptest.Options{}),
		WorkDir: t.TempDir(),
		Stdout:  io.Discard,
		Stderr:  io.Discard,
		Mask:    func(s string) string { return strings.ReplaceAll(s, secret, "***") },
		ACP:     &runner.ACPRequest{Prompt: "please use " + secret, ResultDir: resultDir},
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: exit=%d err=%v", res.ExitCode, res.Err)
	}
	b, err := os.ReadFile(filepath.Join(resultDir, "artifacts", ACPFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), "please use ***") {
		t.Fatalf("acp.jsonl = %s", b)
	}
}
