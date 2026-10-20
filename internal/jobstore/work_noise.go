package jobstore

import (
	"fmt"
	"strings"
)

// Noise reduction (docs/design/2026-10-20-work-noise-and-db-settings-design.md §总体方案 1):
// a test / very short session must not become a work-item draft, and the summarizer and the
// steward must not spend a run on one. Both rules read the same two numbers off the session
// row (human_prompts, started_at / ended_at) and the thresholds from the extension settings.

// IncrementSessionHumanPrompts counts one human prompt on the session and returns the new
// count. It is the ONLY writer of agent_sessions.human_prompts.
//
// The relay calls it from the prompt beat, i.e. the one place where a beat that landed is a
// prompt the person actually typed (an injected prompt carries no human flag, and the
// register call the hook makes after a 404 carries no title). A retried beat for the SAME
// prompt is dropped by the caller (work.Service.OnHumanPrompt) before it gets here.
//
// A nil error with count 0 means the session row is gone (deleted while the beat was in
// flight); the caller then reads "no prompts yet" rather than failing.
func (s *Store) IncrementSessionHumanPrompts(sid string) (int, error) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE agent_sessions SET human_prompts = COALESCE(human_prompts,0) + 1 WHERE session_id=?`, sid)
	if err != nil {
		return 0, fmt.Errorf("jobstore: count session human prompt: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, nil
	}
	var count int
	if err := s.db.QueryRow(`SELECT COALESCE(human_prompts,0) FROM agent_sessions WHERE session_id=?`, sid).Scan(&count); err != nil {
		return 0, fmt.Errorf("jobstore: read session human prompts: %w", err)
	}
	return count, nil
}

// ShortSession reports whether a session is a short-lived one — the sessions the
// summarizer and the steward skip (design §总体方案 1 / 「关键流程」): it has ended (or went
// offline) and lived less than maxMinutes, and the human asked fewer than maxPrompts
// things in it. It is a pure function over the session and the thresholds so the work
// service and the steward judge identically.
//
// A threshold of 0 disables its half of the rule (nothing is ever "short"), which is what
// the default 0 on a setting means: the operator turned it off.
//
// The reason (second result) is the one-line Chinese explanation the summarizer records in
// its run log; it is empty when the session is not short.
func ShortSession(a AgentSession, maxMinutes, maxPrompts int, now int64) (string, bool) {
	if maxMinutes <= 0 || maxPrompts <= 0 {
		return "", false
	}
	if a.State != SessionOffline && a.State != SessionEnded {
		return "", false
	}
	end := a.EndedAt
	if end <= 0 {
		end = a.LastSeenAt
	}
	if a.StartedAt <= 0 || end <= 0 {
		return "", false // nothing to measure the lifetime against
	}
	lived := end - a.StartedAt
	if lived < 0 {
		lived = 0
	}
	if lived >= int64(maxMinutes)*60 || a.HumanPrompts >= maxPrompts {
		return "", false
	}
	return fmt.Sprintf("短命会话：存活 %d 分钟 < %d 分钟，人工提问 %d < %d 次",
		lived/60, maxMinutes, a.HumanPrompts, maxPrompts), true
}
