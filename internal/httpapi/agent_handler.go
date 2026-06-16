package httpapi

import (
	"net/http"
	"sort"

	"github.com/gookit/rux/v2"
)

// agentView is one agent's listing entry: its config type plus a synchronous
// availability probe. A missing CLI yields available=false with the captured
// error — never an HTTP error (plan §9-P3, §11).
type agentView struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleListAgents lists every configured/built-in agent with its detect status.
// Detect is run synchronously per agent; each probe is independently bounded
// (internal/agent.detectTimeout), so a hung CLI cannot stall the whole response
// indefinitely.
func (s *Server) handleListAgents(c *rux.Context) {
	list := s.agents.List()
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	views := make([]agentView, 0, len(keys))
	for _, k := range keys {
		ac := list[k]
		det := s.agents.Detect(k)
		views = append(views, agentView{
			Key:       k,
			Type:      ac.Type,
			Available: det.Available,
			Version:   det.Version,
			Error:     det.Error,
		})
	}
	c.JSON(http.StatusOK, rux.M{"agents": views})
}
