package hookrelay

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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
}

var (
	submittedJobRE = regexp.MustCompile(`(?m)\bjob\s+([A-Za-z0-9][A-Za-z0-9._:-]*)\s+submitted\b`)
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

func formatWatchedJobCompletion(job WatchedJob) string {
	d := job.Duration
	if d <= 0 && job.StartedAt > 0 && job.EndedAt >= job.StartedAt {
		d = time.Duration(job.EndedAt-job.StartedAt) * time.Second
	}
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("[gofer job 完成] %s %s status=%s exit=%d 耗时%s", job.ID,
		strings.TrimSpace(job.Title), job.Status, job.ExitCode, formatDuration(d))
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

func mergeWatchedTerminals(jobs []WatchedJob) string {
	if len(jobs) == 0 {
		return ""
	}
	ordered := append([]WatchedJob(nil), jobs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	parts := make([]string, 0, len(ordered))
	for _, job := range ordered {
		parts = append(parts, formatWatchedJobCompletion(job))
	}
	return strings.Join(parts, "\n")
}
