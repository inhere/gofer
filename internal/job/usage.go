package job

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
)

// maxUsageTailBytes caps how much stderr the codex token sniff reads: the count is
// the run's LAST output, so the tail is where it lives (the same 8KB window the
// transient-error scan uses).
const maxUsageTailBytes = 8 * 1024

// codexTokensRe matches codex's own run summary on stderr:
//
//	tokens used
//	19,802
//
// (multiline: the label and the counted number are two lines). The capture is the
// number only; the thousands separators codex prints are stripped by the caller.
var codexTokensRe = regexp.MustCompile(`(?m)^tokens used[ \t]*\n[ \t]*([\d,]+)[ \t]*$`)

// captureCodexUsage reads codex's token count out of the job's stderr tail (SUP-01
// E). codex `exec` reports its usage as the last thing it prints, on stderr, and no
// structured stream carries it, so the terminal capture sniffs the log — but ONLY
// for the codex agent: the same two lines from any other command are content, and
// turning them into a token count would invent a number the job never reported.
//
// Best-effort like every other capture: no codex agent, no file, no match → no usage.
func (s *Service) captureCodexUsage(entry *jobEntry, resultDir string) {
	entry.mu.Lock()
	jobID := entry.result.ID
	agentKey := entry.result.Agent
	have := entry.result.Usage != nil
	entry.mu.Unlock()
	if have || resultDir == "" {
		return
	}
	ac, ok := s.agents.Get(agentKey)
	if !ok || !isCodexAgent(agentKey, ac.Command) {
		return
	}
	b, err := os.ReadFile(filepath.Join(resultDir, store.StderrFile))
	if err != nil || len(b) == 0 {
		return
	}
	if len(b) > maxUsageTailBytes {
		b = b[len(b)-maxUsageTailBytes:]
	}
	m := codexTokensRe.FindSubmatch(b)
	if len(m) < 2 {
		return
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(string(m[1]), ",", ""), 10, 64)
	if err != nil || n <= 0 {
		return
	}
	entry.mu.Lock()
	entry.result.Usage = &Usage{TotalTokens: n, Source: runner.UsageSourceCodexStderr}
	entry.mu.Unlock()
	slog.Info("job.usage_captured", "job_id", jobID, "source", runner.UsageSourceCodexStderr, "total_tokens", n)
}

// isCodexAgent reports whether a job's agent is codex — by agent key or by the base
// name of its command ("codex", "codex.exe"). The built-in agent is keyed codex, but
// an operator may also point a differently-named agent at the binary, and the
// capture must follow the CLI that prints the line.
func isCodexAgent(agentKey, command string) bool {
	if strings.EqualFold(agentKey, "codex") {
		return true
	}
	base := filepath.Base(command)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return strings.EqualFold(base, "codex")
}

// FormatUsage renders a job's token/cost accounting as the ONE line `job show`
// prints (and the web detail mirrors):
//
//	in 12.3k / out 3.8k / cache 289k / total 305k / $0.0032 (ndjson:omp)
//
// A counter the agent never reported is omitted rather than printed as 0, the two
// cache counters are one `cache` figure (a reader does not care which side of the
// cache the tokens were on), and the source trails the line in parentheses — it is
// what makes the numbers auditable. nil (or a tally with no numbers at all) renders
// as "", so a caller can print the line unconditionally.
func FormatUsage(u *Usage) string {
	if u == nil {
		return ""
	}
	parts := make([]string, 0, 5)
	if u.InputTokens > 0 {
		parts = append(parts, "in "+formatTokens(u.InputTokens))
	}
	if u.OutputTokens > 0 {
		parts = append(parts, "out "+formatTokens(u.OutputTokens))
	}
	if cache := u.CacheReadTokens + u.CacheWriteTokens; cache > 0 {
		parts = append(parts, "cache "+formatTokens(cache))
	}
	if u.TotalTokens > 0 {
		parts = append(parts, "total "+formatTokens(u.TotalTokens))
	}
	if u.CostUSD > 0 {
		parts = append(parts, "$"+strconv.FormatFloat(u.CostUSD, 'f', 4, 64))
	}
	if len(parts) == 0 {
		return ""
	}
	if u.Source != "" {
		parts[len(parts)-1] += " (" + u.Source + ")"
	}
	return strings.Join(parts, " / ")
}

// formatTokens renders a token count at a glance: three significant digits behind a
// k/M suffix once it is large enough to need one (12.3k, 305k, 1.2M), the exact
// number below a thousand.
func formatTokens(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return strconv.FormatFloat(float64(n)/1000, 'g', 3, 64) + "k"
	default:
		return strconv.FormatFloat(float64(n)/1_000_000, 'g', 3, 64) + "M"
	}
}
