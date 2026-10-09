package job

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// maxFromSessionLen bounds a --from-session id; real ids are uuids / short slugs.
const maxFromSessionLen = 200

// CheckFromSession validates the SHAPE of a requested source session (gofer-f4z8). It
// is rendered as ONE argv element into from_session_args, so the hazards are an id that
// reads as a flag (leading "-") or carries whitespace / control characters. Empty =
// unset = fine.
func CheckFromSession(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > maxFromSessionLen {
		return fmt.Errorf("from_session is too long (max %d bytes)", maxFromSessionLen)
	}
	if strings.HasPrefix(id, "-") {
		return fmt.Errorf("from_session %q must not start with '-'", id)
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("from_session %q must not contain whitespace or control characters", id)
		}
	}
	return nil
}

// checkFromSessionRequest is the request-level part of the from_session admission:
// the id's shape, and that it is not mixed with a continuation. A continuation keeps
// the SAME session (session_id / resumed_from / the resume carrier marker), while
// from_session opens a NEW one — asking for both is contradictory.
func checkFromSessionRequest(req JobRequest) error {
	if req.FromSession == "" {
		return nil
	}
	if err := CheckFromSession(req.FromSession); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if req.SessionID != "" || req.ResumedFrom != "" || req.ResumeSourceAgent != "" {
		return fmt.Errorf("%w: from_session opens a NEW session and cannot be combined with a resume / session_id (use `job resume` to continue the same session)", ErrInvalidRequest)
	}
	if req.Session {
		return fmt.Errorf("%w: from_session is not supported for a persistent ACP session (--session)", ErrInvalidRequest)
	}
	return nil
}

// checkFromSessionAgent is the agent-level part, run where the agent is resolved on
// THIS machine (validate's !remote block): the agent must be a cli-agent with
// from_session_args, and — best effort — the source session must belong to the same
// session family when gofer knows who started it.
func (s *Service) checkFromSessionAgent(cfg *config.Config, req JobRequest, agentKey string, ac config.AgentConfig) error {
	if req.FromSession == "" {
		return nil
	}
	if ac.Type != agent.TypeCLIAgent || len(ac.FromSessionArgs) == 0 {
		return fmt.Errorf("%w: agent %q cannot take from_session (set agents.%s.from_session_args with {{from_session}}; only a cli-agent supports it)", ErrInvalidRequest, agentKey, agentKey)
	}
	srcKey := s.fromSessionOwner(req.FromSession)
	if srcKey == "" || srcKey == agentKey {
		return nil
	}
	srcCfg, ok := agent.ResolveAgent(cfg, srcKey)
	if !ok {
		// The source agent is no longer configured here; nothing to compare against.
		return nil
	}
	if !agent.SessionCompatible(srcKey, srcCfg, agentKey, ac) {
		return fmt.Errorf("%w: session %q was started by agent %q, which is not in the same session family as %q", ErrInvalidRequest, req.FromSession, srcKey, agentKey)
	}
	return nil
}

// fromSessionOwner returns the agent gofer recorded for session id, or "" when it does
// not know the session (an id gofer never saw is passed through: the agent CLI itself
// reports a missing session). It looks at the newest job bound to that session id
// first (its real CLI agent: the resume display agent of an exec carrier), then at the
// registered agent sessions.
func (s *Service) fromSessionOwner(id string) string {
	if s.meta == nil {
		return ""
	}
	if recs, err := s.meta.ListJobs(jobstore.ListQuery{Session: id, Limit: 1}); err == nil && len(recs) > 0 {
		key := recs[0].Agent
		if recs[0].ResumeAgent != "" {
			key = recs[0].ResumeAgent
		}
		if key != agent.ExecAgentKey {
			return key
		}
	}
	if sess, ok, err := s.meta.GetAgentSession(id); err == nil && ok {
		return sess.Agent
	}
	return ""
}
