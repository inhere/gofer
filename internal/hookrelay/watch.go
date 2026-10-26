package hookrelay

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/msgprefix"
)

// WatchedJob is the small job projection needed by the Stop hook.
type WatchedJob struct {
	ID        string
	Title     string
	Status    string
	ExitCode  int
	StartedAt int64
	EndedAt   int64
	Duration  time.Duration
	// Error is the job's error line; a cancelled job's notice carries it as the reason
	// (gofer-9b1b: "hold rejected by …" / "hold expired" — the job never ran).
	Error string
}

// watchReasonRunes caps the reason a completion notice quotes.
const watchReasonRunes = 200

var (
	// submittedJobRE only takes a real job id: the current job-YYYYMMDD-HHMMSS-<8 hex>
	// shape and the pre-prefix YYYYMMDD-HHMMSS-<8 hex> one old ids keep. Ordinary prose
	// such as "the job was submitted" must not register a watch for a job named "was".
	submittedJobRE = regexp.MustCompile(`(?m)\bjob\s+((?:job-)?\d{8}-\d{6}-[0-9a-f]{8})\s+submitted\b`)
	watchJobRE     = regexp.MustCompile(`(?m)\bgofer\s+job\s+watch\s+([A-Za-z0-9][A-Za-z0-9._:-]*)\b`)
	finishedJobRE  = regexp.MustCompile(`(?m)\bjob\s+([A-Za-z0-9][A-Za-z0-9._:-]*)\s+(?:finished|completed)\s*:`)
)

func isShellTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash", "shell", "shell_command", "exec_command", "command", "exec", "run_command":
		return true
	default:
		return false
	}
}

// extractJobWatchCandidates is local and cheap. A terminal line in the same
// tool output wins over a submitted/watch line.
func extractJobWatchCandidates(tool, output string) []string {
	if !isShellTool(tool) {
		return nil
	}
	terminal := make(map[string]struct{})
	for _, match := range finishedJobRE.FindAllStringSubmatch(output, -1) {
		terminal[match[1]] = struct{}{}
	}
	seen := make(map[string]struct{})
	for _, re := range []*regexp.Regexp{submittedJobRE, watchJobRE} {
		for _, match := range re.FindAllStringSubmatch(output, -1) {
			if _, done := terminal[match[1]]; !done {
				seen[match[1]] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// formatWatchedJobCompletion renders one watched-job notice. It opens with the
// job-done prefix (msgprefix.JobDone), the same prefix the messenger path uses
// (sessionrelay.FormatJobEndedNotice): gofer's own notice, never a person's
// "[来自 web，…]" line. Both the prefix and the body speak lang — the language the
// server names for the receiving session.
func formatWatchedJobCompletion(job WatchedJob, lang i18n.Lang) string {
	d := job.Duration
	if d <= 0 && job.StartedAt > 0 && job.EndedAt >= job.StartedAt {
		d = time.Duration(job.EndedAt-job.StartedAt) * time.Second
	}
	if d < 0 {
		d = 0
	}
	line := msgprefix.Open(lang, msgprefix.JobDone) + " " + i18n.T(lang, "hook.watch.job_done",
		"id", job.ID, "title", strings.TrimSpace(job.Title), "status", job.Status,
		"exit", job.ExitCode, "duration", formatDuration(d))
	if reason := watchReason(job); reason != "" {
		line += " reason=" + reason
	}
	if strings.EqualFold(job.Status, "needs_review") {
		line += i18n.T(lang, "hook.watch.review_hint", "id", job.ID)
	}
	return line
}

// watchReason is the reason a completion notice states: the error of a cancelled job
// (a rejected or expired hold, or a cancel), on one line and capped.
func watchReason(job WatchedJob) string {
	if !strings.EqualFold(job.Status, "cancelled") && !strings.EqualFold(job.Status, "canceled") {
		return ""
	}
	reason := strings.Join(strings.Fields(job.Error), " ")
	if r := []rune(reason); len(r) > watchReasonRunes {
		reason = string(r[:watchReasonRunes]) + "…"
	}
	return reason
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

func mergeWatchedTerminals(jobs []WatchedJob, lang i18n.Lang) string {
	if len(jobs) == 0 {
		return ""
	}
	ordered := append([]WatchedJob(nil), jobs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	parts := make([]string, 0, len(ordered))
	for _, job := range ordered {
		parts = append(parts, formatWatchedJobCompletion(job, lang))
	}
	return strings.Join(parts, "\n")
}
