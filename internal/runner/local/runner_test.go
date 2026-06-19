package local

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
)

func TestRunSuccess(t *testing.T) {
	var out bytes.Buffer
	r := New()
	res := r.Run(context.Background(), runner.Request{
		Command: "go",
		Args:    []string{"version"},
		WorkDir: t.TempDir(),
		Stdout:  &out,
		Stderr:  &bytes.Buffer{},
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("expected success, got code=%d err=%v", res.ExitCode, res.Err)
	}
	if !strings.Contains(out.String(), "go version") {
		t.Fatalf("stdout missing version output: %q", out.String())
	}
}

func TestRunNonZeroExit(t *testing.T) {
	r := New()
	res := r.Run(context.Background(), runner.Request{
		Command: "sh",
		Args:    []string{"-c", "exit 3"},
		WorkDir: t.TempDir(),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
	})
	if res.Err != nil {
		t.Fatalf("non-zero exit should not be a runner error, got %v", res.Err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", res.ExitCode)
	}
}

func TestRunTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r := New()
	res := r.Run(ctx, runner.Request{
		Command: "sleep",
		Args:    []string{"5"},
		WorkDir: t.TempDir(),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
	})
	if res.Err == nil {
		t.Fatalf("expected ctx error on timeout")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", ctx.Err())
	}
}

func TestRunEnvAndCwd(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	r := New()
	res := r.Run(context.Background(), runner.Request{
		Command: "sh",
		Args:    []string{"-c", "echo $DAB_TEST_VAR; pwd"},
		WorkDir: dir,
		Env:     map[string]string{"DAB_TEST_VAR": "hello-env"},
		Stdout:  &out,
		Stderr:  &bytes.Buffer{},
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: code=%d err=%v", res.ExitCode, res.Err)
	}
	got := out.String()
	if !strings.Contains(got, "hello-env") {
		t.Fatalf("env var not propagated: %q", got)
	}
	// pwd should resolve to the work dir (allow for symlink-resolved /tmp paths).
	if !strings.Contains(got, dir) && !strings.Contains(got, strings.TrimPrefix(dir, "/private")) {
		t.Logf("work dir %q, pwd output %q", dir, got)
	}
}

func TestRunCommandNotFound(t *testing.T) {
	r := New()
	res := r.Run(context.Background(), runner.Request{
		Command: "this-command-does-not-exist-xyz",
		WorkDir: t.TempDir(),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
	})
	if res.Err == nil {
		t.Fatalf("expected start error for missing command")
	}
	if res.ExitCode != -1 {
		t.Fatalf("expected synthetic -1 exit code, got %d", res.ExitCode)
	}
}
