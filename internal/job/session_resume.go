package job

import (
	"strings"

	"github.com/inhere/gofer/internal/agent"
)

// CanLoadSession reports whether the agent declares loadSession: an agent that cannot load its
// own past session starts a fresh one when a session job is started again.
func CanLoadSession(agents *agent.Registry, agentKey string) bool {
	if agents == nil {
		return false
	}
	ac, ok := agents.Get(agentKey)
	if !ok {
		return false
	}
	return agent.ResumeCapabilities(agentKey, ac).LoadSession
}

// StampSessionResume makes the session job req continue an earlier ACP conversation: a
// continuation LOADS the conversation, and ResumedFrom + SessionID is exactly what the
// executor reads (resumeLoadSessionID). It stamps nothing, and reports false, when there is no
// session id or source job to continue or the agent cannot load a session. It is the one
// restart rule shared by every server-side starter of a resident session (the mailbox's
// resident agents and the seat roster).
func StampSessionResume(req *JobRequest, loadSession bool, sessionID, fromJobID string) bool {
	sid := strings.TrimSpace(sessionID)
	if sid == "" || strings.TrimSpace(fromJobID) == "" || !loadSession {
		return false
	}
	req.SessionID = sid
	req.ResumedFrom = fromJobID
	return true
}
