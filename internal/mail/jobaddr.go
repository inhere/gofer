package mail

import (
	"encoding/json"
	"strings"

	"github.com/inhere/gofer/internal/job"
)

// Which `job:` addresses can actually take a letter (gofer-0n6r). The deliverer hands a letter to
// a job only when it is waiting for input, which only an ACP session job (`session: true`) ever
// is: a one-off run reads its prompt once and ends, so a letter addressed to it had no path from
// the start. The mailbox learns that from the job service (SessionJobCheck) — it does not read the
// jobs table itself, because "was this a session job" is the job layer's own knowledge.

// SessionJobLookup adapts the job service's lookup to a SessionJobCheck: the snapshot's Session
// flag, or — for a job whose session state is not recorded yet (a session job queued when the
// server restarted) — the original request.
func SessionJobLookup(get func(id string) (job.JobResult, bool)) SessionJobCheck {
	return func(id string) (session, found bool) {
		rec, ok := get(id)
		if !ok {
			return false, false
		}
		if rec.Session {
			return true, true
		}
		return requestAsksSession(rec.RequestJSON), true
	}
}

// requestAsksSession reads `session: true` out of a stored job request. An unreadable payload is
// no evidence of a one-off job, so it answers false and the caller keeps the live snapshot's word.
func requestAsksSession(requestJSON string) bool {
	if strings.TrimSpace(requestJSON) == "" {
		return false
	}
	var req struct {
		Session bool `json:"session"`
	}
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return false
	}
	return req.Session
}
