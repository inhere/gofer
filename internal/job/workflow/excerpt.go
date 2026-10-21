package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

// maxExcerptLines caps how many lines a step/fan failure excerpt may carry (design
// §一·6). A log tail with hundreds of matching lines is reduced to the first few
// that carry the reason, so the detail view stays readable.
const maxExcerptLines = 20

// excerptTailBytes is how much of each log file is read (from the end). Failures
// print near the end of a run; a bounded window keeps the detail view cheap for a
// long build log.
const excerptTailBytes = 64 << 10

// excerptBlockContext is how many lines after a strong failure line the excerpt keeps
// as the failure block's context (the test's own log lines, a panic stack, the
// `Write at …` / `Read at …` lines of the race detector).
const excerptBlockContext = 5

// excerptWeakMarkers are the substrings that only mark a line as a failure when the
// log carries no strong signal at all (design §一·6): jest/mocha crosses, runtime and
// tool errors. They are lower case because the match is case-insensitive, so `FAIL`,
// `Fail` and `failed` all hit the same marker. `fail` also covers `FAILED` / `failures`.
// gofer-j5xc: `error:` is deliberately WEAK — a test suite that prints hundreds of
// expected `ERROR: …` lines (argument-validation cases) drowned the real failure.
var excerptWeakMarkers = []string{"fail", "✗", "error:", "panic:"}

// strongFailureMarker is a failure line of the go toolchain: the per-test header, the
// per-package summary, a panic banner and the race detector banner. An excerpt prefers
// these blocks over any weak `ERROR:` line.
var strongFailureMarker = regexp.MustCompile(`(?i)^(--- FAIL:|FAIL\t|FAIL$|panic:|WARNING: DATA RACE)`)

// failureExcerpt returns the failure excerpt shown for a step/fan row: the strong
// failure blocks of the job's log tail (stdout first, then stderr), de-duplicated, in
// log order, capped at maxExcerptLines. Only when no strong failure block exists (a
// non-Go tool, an agent that printed one `error: …` line) does it fall back to the
// weak `error:` / `fail` lines, and a job that wrote neither (a built-in step, or an
// agent that only wrote a one-line error) falls back to the job's recorded error
// message, so a failed row is never silent.
func failureExcerpt(rec jobstore.JobRecord) string {
	logs := make([][]string, 0, 2)
	for _, name := range []string{store.StdoutFile, store.StderrFile} {
		logs = append(logs, tailLines(filepath.Join(rec.ResultDir, name)))
	}
	seen := make(map[string]struct{})
	lines := strongExcerptLines(logs, seen)
	if len(lines) == 0 {
		lines = weakExcerptLines(logs, seen)
	}
	if len(lines) == 0 && rec.Error != "" {
		// A built-in (uses:) step records its reason on the job row; an agent may
		// too. Keep it to the same line cap.
		for _, line := range strings.Split(rec.Error, "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			lines = append(lines, line)
			if len(lines) >= maxExcerptLines {
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// strongExcerptLines collects the strong failure blocks of the given logs: each
// strong failure line plus up to excerptBlockContext following context lines, in log
// order, de-duplicated through seen, capped at maxExcerptLines.
func strongExcerptLines(logs [][]string, seen map[string]struct{}) []string {
	out := make([]string, 0, maxExcerptLines)
	for _, lines := range logs {
		for i := 0; i < len(lines) && len(out) < maxExcerptLines; i++ {
			if !isStrongFailureLine(lines[i]) {
				continue
			}
			out = appendExcerptLine(out, lines[i], seen)
			j := i + 1
			for ; j < len(lines) && j <= i+excerptBlockContext && len(out) < maxExcerptLines; j++ {
				if isStrongFailureLine(lines[j]) || !isExcerptContextLine(lines[j]) {
					break
				}
				out = appendExcerptLine(out, lines[j], seen)
			}
			if j > i+1 {
				// The block's context lines are consumed: do not rescan them.
				i = j - 1
			}
		}
	}
	return out
}

// weakExcerptLines collects the weak failure lines of the given logs, in log order,
// de-duplicated, capped at maxExcerptLines. It is the fallback for a log with no
// strong failure block.
func weakExcerptLines(logs [][]string, seen map[string]struct{}) []string {
	out := make([]string, 0, maxExcerptLines)
	for _, lines := range logs {
		for _, line := range lines {
			if !isWeakFailureLine(line) {
				continue
			}
			out = appendExcerptLine(out, line, seen)
			if len(out) >= maxExcerptLines {
				return out
			}
		}
	}
	return out
}

// appendExcerptLine appends line unless it is a duplicate or the cap is reached.
func appendExcerptLine(out []string, line string, seen map[string]struct{}) []string {
	if len(out) >= maxExcerptLines {
		return out
	}
	if _, ok := seen[line]; ok {
		return out
	}
	seen[line] = struct{}{}
	return append(out, line)
}

// isStrongFailureLine reports whether a log line is a hard failure signal: a go test
// per-test header (`--- FAIL: TestX`), the per-package summary (`FAIL\tpkg`), a bare
// `FAIL`, a `panic:` banner or the race detector's `WARNING: DATA RACE`.
func isStrongFailureLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return strongFailureMarker.MatchString(trimmed)
}

// isExcerptContextLine reports whether a line belongs to the failure block above it:
// an indented detail line (go test prints `    file_test.go:12: boom`), a stack frame
// or goroutine header after a panic / race banner. A line that opens a new test or
// package section ends the block.
func isExcerptContextLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimLeft(line, " \t"))
	for _, open := range []string{"=== run", "=== pa", "ok\t", "ok  ", "exit status", "=== name"} {
		if strings.HasPrefix(lower, open) {
			return false
		}
	}
	return true
}

// isWeakFailureLine reports whether a log line looks like a failure reason when the log
// carries no strong failure block.
func isWeakFailureLine(line string) bool {
	lower := strings.ToLower(line)
	for _, m := range excerptWeakMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// tailLines reads at most excerptTailBytes from the end of path and returns its
// lines (a truncated first line is dropped so a half-read line never shows up). A
// missing / unreadable file yields nil: an excerpt is best-effort and must never
// fail the detail view.
func tailLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := st.Size() - excerptTailBytes
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil
	}
	text := string(buf)
	if off > 0 {
		// The window started mid-line: drop the partial first line.
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		} else {
			text = ""
		}
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}
