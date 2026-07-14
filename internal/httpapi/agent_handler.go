package httpapi

import (
	"net/http"
	"sort"

	"github.com/gookit/rux/v2"
)

// agentView is one agent's listing entry: its config type plus its availability.
// A missing CLI yields available=false with the captured error — never an HTTP
// error (plan §9-P3, §11).
type agentView struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleListAgents lists every configured/built-in agent with its detect status.
//
// Availability is READ FROM THE CACHE (agent.Registry.Availability): the detect pass
// ran once when the config was resolved (core.Build / core.ReloadWith) and turns over
// with it. This endpoint used to spawn one child process PER AGENT PER REQUEST, so a
// browser refresh cost N `--version` processes — and P2's agent templates only grow N.
// A CLI installed after boot appears after the next reload (SIGHUP).
func (s *Server) handleListAgents(c *rux.Context) {
	list := s.agents.List()
	avail := s.agents.Availability()
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	views := make([]agentView, 0, len(keys))
	for _, k := range keys {
		ac := list[k]
		det := avail[k]
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
