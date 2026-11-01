package job

import (
	"encoding/json"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// Submission provenance: who started a job. The server stamps these markers at the entry
// (httpapi) and the 「等我」 / inbox judgement reads them back, so the vocabulary lives here,
// below both.
const (
	// TagSubmittedByJob marks a job started with another job's credential; the value is that
	// job's id (`submitted_by_job:<id>`, SEC-01).
	TagSubmittedByJob = "submitted_by_job:"
	// TagSubmittedBySession marks a job submitted by a terminal agent session: the request
	// carried the session header (X-Gofer-Session-Id), which the CLI attaches whenever the
	// environment names an agent session. The value is that session id.
	TagSubmittedBySession = "submitted_by_session:"
)

// Submission channels (JobRequest.Channel) a person uses. Every other channel (cron, webhook,
// plan, comment, leader, steward, a messenger's resident channel, ...) is gofer or an agent
// acting on somebody's behalf.
const (
	ChannelCLI = "cli"
	ChannelWeb = "web"
	ChannelMCP = "mcp"
)

// HumanSubmitted reports whether a person submitted the job themselves: through the CLI, the
// web console or MCP under a user identity, and not on a job credential, not from an agent
// session, not by the system (a schedule, a plan dispatch, an automatic continuation).
//
// It reads only structured, server-stamped facts of the row, never a model's words:
//
//   - Channel must be cli / web / mcp (empty = legacy / unknown is NOT human: when in doubt the
//     job stays out of a person's attention);
//   - no `submitted_by_job:` tag (a job credential; its caller id is a job id);
//   - no `submitted_by_session:` tag and no SourceSessionID (an agent session submitted it);
//   - request_json names no notify_session (the job reports back to an agent session);
//   - it is not an automatic continuation (AutoResumeAttempt) or a failover takeover
//     (FellBackFrom).
func HumanSubmitted(rec jobstore.JobRecord) bool {
	switch rec.Channel {
	case ChannelCLI, ChannelWeb, ChannelMCP:
	default:
		return false
	}
	if rec.SourceSessionID != "" || rec.AutoResumeAttempt > 0 || rec.FellBackFrom != "" {
		return false
	}
	for _, tag := range unmarshalTags(rec.TagsJSON) {
		if strings.HasPrefix(tag, TagSubmittedByJob) || strings.HasPrefix(tag, TagSubmittedBySession) {
			return false
		}
	}
	if rec.RequestJSON != "" {
		var req struct {
			NotifySession string `json:"notify_session"`
		}
		if json.Unmarshal([]byte(rec.RequestJSON), &req) == nil && strings.TrimSpace(req.NotifySession) != "" {
			return false
		}
	}
	return true
}
