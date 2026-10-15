package httpapi

import (
	"strings"
	"time"

	"github.com/inhere/gofer/internal/job"
)

// job_notify.go is the thin projection between a finished job and the session
// relay's end-notification (gofer-3db4). The DECISION (whether a job notifies at
// all, and which session it may name) is made at submit time by internal/job and
// the delivery is sessionrelay's; the assembly layer only maps the job's own
// fields onto the relay's notice, exactly as it does for the takeover release
// above it in server.go.

// jobNotifyTarget is the session a finished job must tell, or "" when this job
// never asked for an end notification. An explicit NotifySession wins over the
// source session (the CLI's env-detected session, a supervisor somewhere else, a
// continuation whose source link was dropped).
func jobNotifyTarget(r job.JobResult) string {
	if !r.Notify && strings.TrimSpace(r.NotifySession) == "" {
		return ""
	}
	if sid := strings.TrimSpace(r.NotifySession); sid != "" {
		return sid
	}
	return strings.TrimSpace(r.SourceSessionID)
}

// jobNotifyDuration is the wall-clock run time the notice states: zero when the
// job never recorded a start (a job that ended without running), never negative.
func jobNotifyDuration(r job.JobResult) time.Duration {
	if r.StartedAt <= 0 || r.EndedAt < r.StartedAt {
		return 0
	}
	return time.Duration(r.EndedAt-r.StartedAt) * time.Second
}
