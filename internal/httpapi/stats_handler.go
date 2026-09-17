package httpapi

import (
	"net/http"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

type statsResp struct {
	Jobs               statsJobs      `json:"jobs"`
	Workflows          statsWorkflows `json:"workflows"`
	Schedules          statsSchedules `json:"schedules"`
	Runners            statsRunners   `json:"runners"`
	Drivers            statsDrivers   `json:"drivers"`
	DB                 statsDB        `json:"db"`
	Sessions           statsSessions  `json:"sessions"`
	EscalationsPending int            `json:"escalations_pending"`
	Projects           int            `json:"projects"`
	ServerTime         int64          `json:"server_time"`
	Version            string         `json:"version,omitempty"`
	UptimeSec          int64          `json:"uptime_sec"`
}

// statsDBBudget caps the row-count pass of the db block (see jobstore.DBStats): a
// dashboard poll must stay cheap on a big or cold db, so the counts degrade to
// `partial: true` instead of stalling the request. Package var so tests can pin it.
var statsDBBudget = 200 * time.Millisecond

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

// statsDB is the Server-DB card: the metadata db file (path/sizes), its SQLite page
// geometry, and a row count per reported table. Tables lists only tables present in
// the live schema; partial=true means the row-count budget ran out (the file/page
// fields are always complete).
type statsDB struct {
	Path         string           `json:"path"`
	SizeBytes    int64            `json:"size_bytes"`
	WALSizeBytes int64            `json:"wal_size_bytes"`
	PageSize     int64            `json:"page_size"`
	PageCount    int64            `json:"page_count"`
	Tables       map[string]int64 `json:"tables"`
	Partial      bool             `json:"partial"`
}

// statsSessions is the Sessions card: the agent-session totals split by state and by
// relay mode (both zero-filled for the known values so the shape is stable), the
// relay turns still waiting for a human answer, and the sessions seen in the last hour.
type statsSessions struct {
	Total        int            `json:"total"`
	ByState      map[string]int `json:"by_state"`
	ByRelayMode  map[string]int `json:"by_relay_mode"`
	WaitingTurns int            `json:"waiting_turns"`
	SeenWithin1h int            `json:"seen_within_1h"`
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

	dbStats, err := s.jobs.Meta().DBStats(statsDBBudget)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read db stats failed", err.Error())
		return
	}
	sessStats, err := s.jobs.Meta().SessionStats(time.UnixMilli(nowMillis()).Unix())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read session stats failed", err.Error())
		return
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
		DB:                 statsDBFromStore(dbStats),
		Sessions:           statsSessionsFromStore(sessStats),
		EscalationsPending: escalationsPending,
		Projects:           len(s.projects.List()),
		ServerTime:         nowMillis(),
		Version:            s.build.DisplayVersion(),
		UptimeSec:          s.uptimeSec(),
	})
}

// statsDBFromStore maps the store's db picture onto the wire shape (jobstore stays
// wire-free: the JSON keys live here, next to the rest of /v1/stats).
func statsDBFromStore(st jobstore.DBStats) statsDB {
	tables := st.Tables
	if tables == nil {
		tables = map[string]int64{}
	}
	return statsDB{
		Path:         st.Path,
		SizeBytes:    st.SizeBytes,
		WALSizeBytes: st.WALSizeBytes,
		PageSize:     st.PageSize,
		PageCount:    st.PageCount,
		Tables:       tables,
		Partial:      st.Partial,
	}
}

func statsSessionsFromStore(st jobstore.SessionStats) statsSessions {
	byState := st.ByState
	if byState == nil {
		byState = map[string]int{}
	}
	byMode := st.ByRelayMode
	if byMode == nil {
		byMode = map[string]int{}
	}
	return statsSessions{
		Total:        st.Total,
		ByState:      byState,
		ByRelayMode:  byMode,
		WaitingTurns: st.WaitingTurns,
		SeenWithin1h: st.SeenWithin1h,
	}
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
