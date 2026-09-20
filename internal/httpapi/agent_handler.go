package httpapi

import (
	"log/slog"
	"net/http"
	"sort"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// agentView is one agent's listing entry: its config type plus its availability.
// A missing CLI yields available=false with the captured error — never an HTTP
// error (plan §9-P3, §11). health (SUP-01 P3) adds the recent-job picture, so a
// provider that is failing right now is visible before the next job is sent to it.
type agentView struct {
	Key       string           `json:"key"`
	Type      string           `json:"type"`
	Available bool             `json:"available"`
	Version   string           `json:"version,omitempty"`
	Error     string           `json:"error,omitempty"`
	Health    *agentHealthView `json:"health,omitempty"`
	// Injected marks an agent the operator did NOT declare: it was materialized at
	// runtime from a BUILT-IN template because its CLI is on this host (see
	// internal/agent/templates.go). Without the flag the console cannot explain why
	// an agent that appears in no config.yaml is listed here; it is display-only and
	// never gates execution (a template agent is an ordinary agent once injected).
	Injected bool `json:"injected,omitempty"`
}

// agentHealthView is an agent's health (SUP-01 P3) as a reader consumes it: the
// classified state PLUS the evidence it was computed from, so a badge can say why
// ("3 provider errors in the last hour") instead of only asserting a colour. Absent
// only when the evidence could not be read at all.
type agentHealthView struct {
	State           string `json:"state"`
	WindowSec       int    `json:"window_sec"`
	Jobs            int    `json:"jobs"`
	OK              int    `json:"ok"`
	TransientFail   int    `json:"transient_fail"`
	LastTransientAt int64  `json:"last_transient_at,omitempty"`
	LastOKAt        int64  `json:"last_ok_at,omitempty"`
}

// handleListAgents lists every configured/built-in agent with its detect status and
// its recent-job health. A key the operator did not declare carries injected=true,
// so the console can say it came from a built-in template (agent.Resolve's
// detect-gated injection) instead of leaving an unexplained entry in the list.
//
// Availability is READ FROM THE CACHE (agent.Registry.Availability): the detect pass
// ran once when the config was resolved (core.Build / core.ReloadWith) and turns over
// with it. This endpoint used to spawn one child process PER AGENT PER REQUEST, so a
// browser refresh cost N `--version` processes — and P2's agent templates only grow N.
// A CLI installed after boot appears after the next reload (SIGHUP).
//
// Health is ONE aggregate query over the jobs table (never one query per agent), and
// it is best-effort: an unreadable aggregate is reported as an absent block rather
// than failing the listing a reader uses for everything else.
func (s *Server) handleListAgents(c *rux.Context) {
	list := s.agents.List()
	avail := s.agents.Availability()
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	hc := s.jobs.Config().EffectiveAgentHealth()
	var agg map[string]jobstore.AgentHealth
	if all, err := s.jobs.Meta().AgentHealthAll(s.jobs.Now().Unix() - int64(hc.WindowSec)); err != nil {
		slog.Warn("agent health aggregate failed", "err", err)
	} else {
		agg = all
	}

	views := make([]agentView, 0, len(keys))
	injected := s.agents.Injected()
	for _, k := range keys {
		ac := list[k]
		det := avail[k]
		views = append(views, agentView{
			Key:       k,
			Type:      ac.Type,
			Available: det.Available,
			Version:   det.Version,
			Error:     det.Error,
			Health:    healthView(agg[k], hc),
			Injected:  injected[k],
		})
	}
	c.JSON(http.StatusOK, rux.M{"agents": views})
}

// healthView projects one agent's aggregate into the wire shape. A zero record (an
// agent with no job in the window) reads as `unknown` — never as healthy, which would
// claim evidence nobody has.
func healthView(h jobstore.AgentHealth, hc config.AgentHealthConfig) *agentHealthView {
	return &agentHealthView{
		State:           agent.HealthState(h, hc),
		WindowSec:       hc.WindowSec,
		Jobs:            h.Jobs,
		OK:              h.OK,
		TransientFail:   h.TransientFail,
		LastTransientAt: h.LastTransientAt,
		LastOKAt:        h.LastOKAt,
	}
}
