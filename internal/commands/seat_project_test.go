package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeatAddResolvesARelativeCwd: `seat add --cwd tmp/x` (no -p) names a directory beside the
// current one; it has to be resolved to an absolute path before any project root can match it.
func TestSeatAddResolvesARelativeCwd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tmp", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	stub := &seatStub{projects: []map[string]any{{"key": "p1", "host_path": root, "container_path": root}}}
	seatTestStub(t, stub)
	prevCwd, prevProject := seatAddOpts.cwd, seatAddOpts.project
	t.Cleanup(func() { seatAddOpts.cwd, seatAddOpts.project = prevCwd, prevProject })
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "add", "desk", "--cwd", "tmp/x"})
	})
	if code != 0 {
		t.Fatalf("add --cwd tmp/x exit=%d out=%s paths=%v", code, out, stub.paths)
	}
	if len(stub.seats) != 1 {
		t.Fatalf("seats = %+v paths=%v", stub.seats, stub.paths)
	}
	if got := stub.seats[0]["rel_cwd"]; got != "tmp/x" {
		t.Fatalf("rel_cwd = %v, want the directory of the relative --cwd (body %+v)", got, stub.seats[0])
	}
	if got := stub.seats[0]["project_key"]; got != "p1" {
		t.Fatalf("project_key = %v, want the project the directory belongs to", got)
	}
}

// TestSeatRmTakesTheProjectFlag: the same seat name exists in two projects, so `-p` decides which
// one is archived — without it the name is ambiguous.
func TestSeatRmTakesTheProjectFlag(t *testing.T) {
	stub := &seatStub{seats: []map[string]any{
		{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none"},
		{"id": "seat-2", "name": "desk", "project_key": "p2", "kind": "terminal", "state": "none"},
	}}
	seatTestStub(t, stub)
	prev := seatRefOpts.project
	t.Cleanup(func() { seatRefOpts.project = prev })
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "rm", "desk", "-p", "p2"})
	})
	if code != 0 {
		t.Fatalf("rm -p p2 exit=%d out=%s", code, out)
	}
	if len(stub.patched) != 1 || stub.patched[0] != "/v1/seats/seat-2" {
		t.Fatalf("patched = %v, want the seat of project p2", stub.patched)
	}
}

// TestSeatShowTakesTheProjectFlag: `-p` also resolves a bare name for the reading commands.
func TestSeatShowTakesTheProjectFlag(t *testing.T) {
	stub := &seatStub{seats: []map[string]any{
		{"id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "none", "purpose": "of p1"},
		{"id": "seat-2", "name": "desk", "project_key": "p2", "kind": "terminal", "state": "none", "purpose": "of p2"},
	}}
	seatTestStub(t, stub)
	prev := seatRefOpts.project
	t.Cleanup(func() { seatRefOpts.project = prev })
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"seat", "show", "desk", "-p", "p2"})
	})
	if code != 0 || !strings.Contains(out, "of p2") || strings.Contains(out, "of p1") {
		t.Fatalf("show -p p2 exit=%d out=%s", code, out)
	}
}
