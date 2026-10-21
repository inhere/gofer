package workflow

import (
	"os"
	"path/filepath"
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

// excerptMarkers are the substrings that mark a log line as a failure line (design
// §一·6): go test failure headers, jest/mocha crosses, runtime and tool errors. They are
// lower case because the match is case-insensitive, so `FAIL`, `Fail` and `failed` all
// hit the same marker. `fail` also covers `FAILED` / `failures`.
var excerptMarkers = []string{"--- fail:", "fail", "✗", "error:", "panic:"}

// failureExcerpt returns the failure excerpt shown for a step/fan row: the failure
// looking lines of the job's log tail (stdout first, then stderr), de-duplicated,
// in log order, capped at maxExcerptLines. A job that failed without such a line
// (a built-in step, or an agent that only wrote a one-line error) falls back to the
// job's recorded error message, so a failed row is never silent.
func failureExcerpt(rec jobstore.JobRecord) string {
	lines := make([]string, 0, maxExcerptLines)
	seen := make(map[string]struct{})
	for _, name := range []string{store.StdoutFile, store.StderrFile} {
		for _, line := range tailLines(filepath.Join(rec.ResultDir, name)) {
			if !isFailureLine(line) {
				continue
			}
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			lines = append(lines, line)
			if len(lines) >= maxExcerptLines {
				break
			}
		}
		if len(lines) >= maxExcerptLines {
			break
		}
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

// isFailureLine reports whether a log line looks like a failure reason.
func isFailureLine(line string) bool {
	lower := strings.ToLower(line)
	for _, m := range excerptMarkers {
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
