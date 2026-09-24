//go:build windows

package pty

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestStartKeepsExtraEnvOverInherited pins the env layering Spec.Env's callers depend
// on: the caller's variables are appended AFTER the inherited environment, so for a
// duplicated name the caller's value must reach the child — exactly what os/exec does on
// unix and Windows (dedupEnvCase, "last occurrence wins").
//
// The ConPTY binding passes the block straight to CreateProcess, which keeps the FIRST
// occurrence, so pty.Start de-duplicates before handing it over. Without that, a pty job
// kept the serve's PATH instead of the job's own — the F10 fix (the job's gofer
// directory first on PATH) would have been silently inert for interactive jobs.
func TestStartKeepsExtraEnvOverInherited(t *testing.T) {
	if !IsAvailable() {
		t.Skip("pty backend not available")
	}
	p, err := Start(Spec{
		Command: testcmd.Path(t),
		Args:    []string{"env-print", "GOFER_PTY_DUP"},
		// The shape util.EnvironWithout produces: inherited first, the caller's own last.
		Env:  []string{"GOFER_PTY_DUP=inherited", "GOFER_PTY_DUP=extra"},
		Cols: 100,
		Rows: 30,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Close() }()

	out := &ptySink{}
	go func() { _, _ = io.Copy(out, p) }()
	got := waitForPTY(t, out, "GOFER_PTY_DUP=")
	if !strings.Contains(got, "GOFER_PTY_DUP=extra") {
		t.Fatalf("the child did not get the caller's value:\n%q", got)
	}
	if strings.Contains(got, "inherited") {
		t.Fatalf("the inherited value reached the child:\n%q", got)
	}
	if code, err := p.Wait(context.Background()); err != nil || code != 0 {
		t.Fatalf("Wait: code=%d err=%v", code, err)
	}
}

// ptySink is a concurrency-safe sink for the pty pump (the reader goroutine writes while
// the test polls).
type ptySink struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *ptySink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *ptySink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitForPTY polls the pump until the accumulated output carries needle. A ConPTY stream
// arrives in escape-sequence-wrapped chunks, so polling for the marker is the honest
// signal (there is no exit to wait on before the child has printed).
func waitForPTY(t *testing.T, out *ptySink, needle string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := out.String(); strings.Contains(got, needle) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %q in the child's output within the deadline: %q", needle, out.String())
	return ""
}
