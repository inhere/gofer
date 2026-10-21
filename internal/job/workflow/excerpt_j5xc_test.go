package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// writeLog writes name into dir and returns a JobRecord pointing at dir.
func writeLog(t *testing.T, name, body string) jobstore.JobRecord {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return jobstore.JobRecord{ResultDir: dir}
}

// TestFailureExcerptPrefersStrongBlocks pins gofer-j5xc: a go test log in which every
// validation case prints an EXPECTED `ERROR: …` line must not bury the real failure —
// the excerpt shows the `--- FAIL:` block (plus its detail lines and the package
// summary) and no `ERROR:` line at all.
func TestFailureExcerptPrefersStrongBlocks(t *testing.T) {
	t.Parallel()
	var stdout strings.Builder
	for i := 0; i < 40; i++ {
		stdout.WriteString("ERROR: unknown flag --case-" + stringRune(i) + "\n")
		stdout.WriteString("error: config validate failed\n")
	}
	stdout.WriteString("=== RUN   TestValidate\n")
	stdout.WriteString("    validate_test.go:88: want an error, got nil\n")
	stdout.WriteString("--- FAIL: TestValidate (0.01s)\n")
	stdout.WriteString("    validate_test.go:90: second detail line\n")
	stdout.WriteString("FAIL\n")
	stdout.WriteString("exit status 1\n")
	stdout.WriteString("FAIL\tgithub.com/inhere/gofer/internal/config\t0.02s\n")
	rec := writeLog(t, "stdout.log", stdout.String())

	got := failureExcerpt(rec)
	lines := strings.Split(got, "\n")
	if len(lines) > maxExcerptLines {
		t.Fatalf("excerpt has %d lines, want at most %d", len(lines), maxExcerptLines)
	}
	if lines[0] != "--- FAIL: TestValidate (0.01s)" {
		t.Fatalf("first line = %q (excerpt %q), want the go test failure header", lines[0], got)
	}
	for _, want := range []string{
		"    validate_test.go:90: second detail line",
		"FAIL\tgithub.com/inhere/gofer/internal/config\t0.02s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("excerpt %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "ERROR:") || strings.Contains(got, "error: config") {
		t.Fatalf("excerpt kept an expected ERROR line: %q", got)
	}
	if strings.Contains(got, "=== RUN") {
		t.Fatalf("excerpt kept the section opener: %q", got)
	}
}

// TestFailureExcerptWeakFallback: with no strong failure block (a non-Go tool that only
// prints `error: …`) the weak markers still produce an excerpt.
func TestFailureExcerptWeakFallback(t *testing.T) {
	t.Parallel()
	rec := writeLog(t, "stderr.log", "warning: unrelated\nERROR: dial tcp: refused\n✗ build failed\n")
	got := failureExcerpt(rec)
	if !strings.Contains(got, "ERROR: dial tcp: refused") || !strings.Contains(got, "✗ build failed") {
		t.Fatalf("weak excerpt = %q, want the error / cross lines", got)
	}
	if strings.Contains(got, "warning: unrelated") {
		t.Fatalf("weak excerpt kept a non-failure line: %q", got)
	}
}

// TestFailureExcerptPanicAndRace: a panic banner and the race detector banner are strong
// signals, and the lines right after them are part of the block.
func TestFailureExcerptPanicAndRace(t *testing.T) {
	t.Parallel()
	panicLog := writeLog(t, "stdout.log", strings.Join([]string{
		"ERROR: unrelated validation line",
		"panic: boom",
		"",
		"goroutine 1 [running]:",
		"main.main()",
		"\t/tmp/x.go:6 +0x18",
		"exit status 2",
	}, "\n"))
	got := failureExcerpt(panicLog)
	if !strings.HasPrefix(got, "panic: boom") {
		t.Fatalf("panic excerpt = %q, want the panic banner first", got)
	}
	if !strings.Contains(got, "goroutine 1 [running]:") || !strings.Contains(got, "/tmp/x.go:6") {
		t.Fatalf("panic excerpt = %q, want the stack context", got)
	}
	if strings.Contains(got, "ERROR:") {
		t.Fatalf("panic excerpt kept a weak line: %q", got)
	}

	raceLog := writeLog(t, "stdout.log", strings.Join([]string{
		"ERROR: unrelated",
		"==================",
		"WARNING: DATA RACE",
		"Read at 0x00c0000ba018 by goroutine 7:",
		"  main.func1()",
		"      /tmp/y.go:10 +0x2c",
	}, "\n"))
	raceGot := failureExcerpt(raceLog)
	if !strings.Contains(raceGot, "WARNING: DATA RACE") {
		t.Fatalf("race excerpt = %q, want the race banner", raceGot)
	}
	if !strings.Contains(raceGot, "Read at 0x00c0000ba018 by goroutine 7:") {
		t.Fatalf("race excerpt = %q, want the access line as context", raceGot)
	}
	if strings.Contains(raceGot, "ERROR:") {
		t.Fatalf("race excerpt kept a weak line: %q", raceGot)
	}
}
