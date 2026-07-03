package httpapi

import (
	"net/http"
	"time"

	"github.com/gookit/rux/v2"
)

type statsResp struct {
	Jobs               statsJobs      `json:"jobs"`
	Workflows          statsWorkflows `json:"workflows"`
	Schedules          statsSchedules `json:"schedules"`
	Runners            statsRunners   `json:"runners"`
	Drivers            statsDrivers   `json:"drivers"`
	EscalationsPending int            `json:"escalations_pending"`
	Projects           int            `json:"projects"`
	ServerTime         int64          `json:"server_time"`
	Version            string         `json:"version,omitempty"`
	UptimeSec          int64          `json:"uptime_sec"`
}

type statsJobs struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
}

type statsWorkflows struct {
	Running int `json:"running"`
	Total   int `json:"total"`
}

type statsSchedules struct {
	Total   int `json:"total"`
	Enabled int `json:"enabled"`
}

type statsRunners struct {
	WorkersConnected int `json:"workers_connected"`
	WorkersTotal     int `json:"workers_total"`
	PeersUp          int `json:"peers_up"`
}

type statsDrivers struct {
	Online      int `json:"online"`
	Supervisors int `json:"supervisors"`
}

func (s *Server) handleStats(c *rux.Context) {
	byStatus, err := s.jobs.Meta().CountJobsByStatus()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "count jobs failed", err.Error())
		return
	}
	jobTotal := 0
	for _, n := range byStatus {
		jobTotal += n
	}

	schedules, err := s.jobs.Meta().ListSchedules("", false)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list schedules failed", err.Error())
		return
	}
	enabledSchedules := 0
	for _, rec := range schedules {
		if rec.Enabled == 1 {
			enabledSchedules++
		}
	}

	drivers, err := s.statsDrivers()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list drivers failed", err.Error())
		return
	}

	pending, err := s.jobs.ListPendingInteractions()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list pending interactions failed", err.Error())
		return
	}
	escalationsPending := 0
	for _, it := range pending {
		if it.NeedsHuman == 1 {
			escalationsPending++
		}
	}

	c.JSON(http.StatusOK, statsResp{
		Jobs: statsJobs{
			Total:    jobTotal,
			ByStatus: byStatus,
		},
		// TODO: wire workflow counts when workflow.Engine exposes a cheap aggregate.
		Workflows:          statsWorkflows{Running: 0, Total: 0},
		Schedules:          statsSchedules{Total: len(schedules), Enabled: enabledSchedules},
		Runners:            s.statsRunners(),
		Drivers:            drivers,
		EscalationsPending: escalationsPending,
		Projects:           len(s.projects.List()),
		ServerTime:         nowMillis(),
		Version:            s.build.DisplayVersion(),
		UptimeSec:          s.uptimeSec(),
	})
}

func (s *Server) uptimeSec() int64 {
	if s.startedAt.IsZero() {
		return 0
	}
	sec := time.UnixMilli(nowMillis()).Sub(s.startedAt).Seconds()
	if sec < 0 {
		return 0
	}
	return int64(sec)
}

func (s *Server) statsDrivers() (statsDrivers, error) {
	if s.presence == nil {
		return statsDrivers{}, nil
	}
	list, err := s.presence.List("", "")
	if err != nil {
		return statsDrivers{}, err
	}
	out := statsDrivers{Online: len(list)}
	for _, agent := range list {
		if agent.Role == "supervisor" {
			out.Supervisors++
		}
	}
	return out, nil
}

func (s *Server) statsRunners() statsRunners {
	workers := s.workerConfigs()
	out := statsRunners{WorkersTotal: len(workers)}
	if s.workers != nil {
		for id := range workers {
			if ws, ok := s.workers.WorkerStatus(id); ok && ws.Connected {
				out.WorkersConnected++
			}
		}
	}

	probes := s.probeIndex()
	for name, rc := range s.runners {
		if rc.Type != runnerTypePeerHTTP {
			continue
		}
		if pr, ok := probes[name]; ok && pr.Up {
			out.PeersUp++
		}
	}
	return out
}
