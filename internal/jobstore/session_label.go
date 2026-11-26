package jobstore

import "strings"

// SessionLabel renders a session id for a journal line: a gofer job id (job- prefix) is
// kept whole so it stays recognisable and can be pasted into `job show`; any other id
// (an agent's own session UUID, a terminal session id) is cut to its first 8 chars.
func SessionLabel(id string) string {
	if strings.HasPrefix(id, "job-") {
		return id
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
