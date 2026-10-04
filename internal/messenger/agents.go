package messenger

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// ListAgentsPrompt is what the resident process is asked to run. The listing is
// read from the ListAgents tool_result, so the wording only has to make the
// model call the tool once.
const ListAgentsPrompt = "调用 ListAgents 并原样输出结果。不要调用其他工具，不要添加解释。"

// Agent is one session ListAgents reports. Claude's listing carries a name,
// a short id, a kind, a status and a relative start time; it has NO directory
// or absolute timestamp, so Cwd/LastActivity stay empty unless a caller fills
// them from another source (the gofer session registry).
type Agent struct {
	Name         string `json:"name"`
	ShortID      string `json:"short_id,omitempty"`
	Kind         string `json:"kind,omitempty"`   // e.g. interactive
	Status       string `json:"status,omitempty"` // idle | busy | ...
	Started      string `json:"started,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	LastActivity string `json:"last_activity,omitempty"`
}

// AgentList is the parsed ListAgents result. RawOutput is always the text the
// parser worked from, so nothing is lost when the format changes.
type AgentList struct {
	Self      string  `json:"self,omitempty"` // the messenger's own session name
	Agents    []Agent `json:"agents"`
	RawOutput string  `json:"raw_output"`
}

// ListAgents asks the resident process for the sessions Claude Code can message.
// command only supplies the binary (command[0]); the prompt is fixed.
func (m *Manager) ListAgents(ctx context.Context, runner, cwd string, command []string) (AgentList, error) {
	// G043: "server" is the built-in runner's other spelling; state is keyed by the
	// canonical key either way.
	runner = config.NormalizeRunnerName(strings.ToLower(strings.TrimSpace(runner)))
	if runner != config.BuiltinLocalRunner {
		return AgentList{}, errors.New("resident messenger only supports the local runner")
	}
	if len(command) == 0 && m.command != "" {
		command = []string{m.command}
	}
	done := m.begin(runner, "list_agents", "", "")
	ev, err := m.roundTrip(ctx, runner, cwd, command, ListAgentsPrompt)
	done(err)
	if err != nil {
		return AgentList{}, err
	}
	raw := ev.listing
	if strings.TrimSpace(raw) == "" {
		raw = ev.output
	}
	return ParseAgents(raw), nil
}

var (
	selfLineRe  = regexp.MustCompile(`^This session is (.+?)(?: \[[0-9a-fA-F]+\])?\s+—`)
	agentLineRe = regexp.MustCompile(`^(.+?)\s+\[([0-9A-Za-z]+)\]\s*$`)
)

// ParseAgents turns the ListAgents listing into rows. It is deliberately
// tolerant: code fences are stripped, unknown "·" segments are ignored, and
// lines that do not look like a session row are skipped. RawOutput keeps the
// original text.
func ParseAgents(raw string) AgentList {
	out := AgentList{Agents: []Agent{}, RawOutput: raw}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}
		if m := selfLineRe.FindStringSubmatch(line); m != nil {
			out.Self = strings.TrimSpace(m[1])
			continue
		}
		parts := strings.Split(line, "·")
		if len(parts) < 2 {
			continue
		}
		head := agentLineRe.FindStringSubmatch(strings.TrimSpace(parts[0]))
		if head == nil {
			continue
		}
		a := Agent{Name: strings.TrimSpace(head[1]), ShortID: head[2]}
		rest := make([]string, 0, len(parts)-1)
		for _, p := range parts[1:] {
			if p = strings.TrimSpace(p); p != "" {
				rest = append(rest, p)
			}
		}
		for i, p := range rest {
			switch {
			case strings.HasPrefix(p, "started "):
				a.Started = strings.TrimPrefix(p, "started ")
			case i == 0:
				a.Kind = p
			case i == 1:
				a.Status = p
			}
		}
		out.Agents = append(out.Agents, a)
	}
	return out
}
