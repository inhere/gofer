package job

import (
	"log/slog"
	"strings"
	"unicode/utf8"
)

// logErrorSummaryRunes caps the error text a log line carries. A job error can quote
// an agent's own stderr (and through it parts of the prompt), so the log keeps only
// the beginning and marks that it dropped the rest; the whole text stays on the job
// row and in the event log, which is where a reader goes for the full story (SR1301).
const logErrorSummaryRunes = 300

// logJobSubmitted records that a job became a fact on this machine.
//
// It is emitted where the job.submitted lifecycle event is recorded, so the line and
// the event log agree: once per job, on the machine that OWNS it — the server for a
// local job, the worker for one it re-submits itself (that path re-enters submit with
// runner=local). A caller reading two machines' logs can therefore tell "accepted but
// never dispatched" from "accepted and sent to a worker".
func logJobSubmitted(jobID string, req JobRequest) {
	attrs := []any{"event", "job.submitted", "job_id", jobID,
		"agent", req.Agent, "runner", req.Runner, "project", req.ProjectKey,
		"caller", req.CallerID, "session", req.Session}
	if req.PlanID != "" {
		attrs = append(attrs, "plan_id", req.PlanID)
	}
	slog.Info("job.submitted", attrs...)
}

// logJobSay records that a caller queued another prompt on a resident session.
// Only the SIZE is logged — the text is a user's message and never belongs in a
// log file (SR1301); the turn itself is in the job's event timeline.
func logJobSay(jobID, message string, images int) {
	slog.Info("job.say", "event", "job.say", "job_id", jobID,
		"chars", utf8.RuneCountInString(message), "images", images)
}

// logJobTerminal records how a job ended, next to every place that records the
// job.terminal lifecycle event: the normal finish (local and worker-executed jobs both
// end in this process), a takeover that could not be submitted, a retry that could not
// be scheduled, and the paths of a job that never ran (hold decided, review verdict,
// update release refused, session recovery abandoned).
func logJobTerminal(snap JobResult) {
	attrs := []any{"event", "job.terminal", "job_id", snap.ID, "status", snap.Status,
		"exit_code", snap.ExitCode, "duration_ms", jobDurationMs(snap)}
	if summary := logErrorSummary(snap.Error); summary != "" {
		attrs = append(attrs, "error", summary)
	}
	slog.Info("job.terminal", attrs...)
}

// jobDurationMs is how long the job took: run time (StartedAt→EndedAt) when it
// actually ran, otherwise the whole wait (QueuedAt→EndedAt) — a job that never
// started still has to say how long it was parked. A missing timestamp or a negative
// span (a clock step) reads as 0 instead of a nonsense duration.
func jobDurationMs(snap JobResult) int64 {
	if snap.EndedAt <= 0 {
		return 0
	}
	from := snap.StartedAt
	if from <= 0 {
		from = snap.QueuedAt
	}
	if from <= 0 || snap.EndedAt < from {
		return 0
	}
	return (snap.EndedAt - from) * 1000
}

// logErrorSummary trims an error for a log line: one line, bounded length, with the
// dropped part marked rather than silently cut.
func logErrorSummary(msg string) string {
	msg = strings.TrimSpace(strings.ReplaceAll(msg, "\n", " "))
	runes := []rune(msg)
	if len(runes) <= logErrorSummaryRunes {
		return msg
	}
	return string(runes[:logErrorSummaryRunes]) + "...(truncated)"
}
