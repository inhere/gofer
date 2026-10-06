package work

import "strings"

// Speaker labels (W2a, design §14.1.5). Every work-item field change and journal line
// carries who made it, in ONE canonical spelling, so the card can show "谁写的、何时" and
// a reader can tell a person's word from a session's report or a model's tidy-up:
//
//	human:<caller>            a person (web / CLI with that caller id)
//	session:<sid>(<agent>)    a terminal session reporting about itself
//	steward(<agent>)          the steward (W2b)
//	summarizer(<agent>)       the passive tidy-up job
//	job:<id> / system         a job credential / gofer's own bookkeeping
const (
	ActorHuman      = "human"
	ActorSession    = "session"
	ActorSteward    = "steward"
	ActorSummarizer = "summarizer"
	ActorJob        = "job"
	ActorSystem     = "system"
	ActorOther      = "other"
)

// HumanBy is the speaker label of a person.
func HumanBy(caller string) string {
	if caller = strings.TrimSpace(caller); caller != "" {
		return "human:" + caller
	}
	return "human"
}

// SessionBy is the speaker label of a terminal session; agent may be empty.
func SessionBy(sid, agent string) string {
	sid = strings.TrimSpace(sid)
	if agent = strings.TrimSpace(agent); agent != "" {
		return "session:" + sid + "(" + agent + ")"
	}
	return "session:" + sid
}

// StewardBy is the speaker label of the steward agent.
func StewardBy(agent string) string { return withAgent(ActorSteward, agent) }

// SummarizerBy is the speaker label of the summarizer agent.
func SummarizerBy(agent string) string { return withAgent(ActorSummarizer, agent) }

func withAgent(kind, agent string) string {
	if agent = strings.TrimSpace(agent); agent != "" {
		return kind + "(" + agent + ")"
	}
	return kind
}

// ActorKind classifies a speaker label (the web colours the timeline by it).
func ActorKind(by string) string {
	by = strings.TrimSpace(by)
	switch {
	case by == "" || by == ActorSystem:
		return ActorSystem
	case by == ActorHuman || strings.HasPrefix(by, "human:"):
		return ActorHuman
	case by == ActorSession || strings.HasPrefix(by, "session:"):
		return ActorSession
	case by == ActorSteward || strings.HasPrefix(by, "steward("):
		return ActorSteward
	case by == ActorSummarizer || strings.HasPrefix(by, "summarizer("):
		return ActorSummarizer
	case strings.HasPrefix(by, "job:"):
		return ActorJob
	}
	return ActorOther
}

// SessionOf extracts the session id and agent from a session speaker label.
func SessionOf(by string) (sid, agent string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(by), "session:")
	if !found || rest == "" {
		return "", "", false
	}
	if i := strings.IndexByte(rest, '('); i > 0 && strings.HasSuffix(rest, ")") {
		return rest[:i], rest[i+1 : len(rest)-1], true
	}
	return rest, "", true
}

// normalizeBy completes a bare "session:<sid>" label with the session's agent, so every
// entry point (HTTP, MCP, CLI) ends up with the same spelling without each of them
// looking the agent up.
func (s *Service) normalizeBy(by string) string {
	sid, agent, ok := SessionOf(by)
	if !ok || agent != "" || s == nil || s.store == nil {
		return by
	}
	if a, found, err := s.store.GetAgentSession(sid); err == nil && found {
		return SessionBy(sid, a.Agent)
	}
	return by
}

// SessionBy returns the canonical speaker label of a registered session.
func (s *Service) SessionBy(sid string) string {
	return s.normalizeBy(SessionBy(sid, ""))
}
