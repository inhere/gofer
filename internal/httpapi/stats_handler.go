package httpapi

import (
	"net/http"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
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
	Usage              statsUsage     `json:"usage"`
	EscalationsPending int            `json:"escalations_pending"`
	Projects           int            `json:"projects"`
	ServerTime         int64          `json:"server_time"`
	// ServerTZOffsetSec is the server's local UTC offset in seconds (bd
	// h-aii-tnua): the CLI renders schedule/wakeup times with it, so a stamp means
	// the same clock the server acted on. Times stay unix seconds on the wire.
	ServerTZOffsetSec int    `json:"server_tz_offset_sec"`
	Version           string `json:"version,omitempty"`
	UptimeSec         int64  `json:"uptime_sec"`
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

// statsUsage is the usage block (SUP-01 E): what each agent burned, per window. A
// window missing from the map was NOT computed (the budget ran out — see Partial);
// callers must not read a missing window as "zero usage".
type statsUsage struct {
	Windows map[string]statsUsageWindow `json:"windows"`
	Partial bool                        `json:"partial"`
}

// statsUsageWindow is one window: the per-agent tallies keyed by agent, plus their sum.
type statsUsageWindow struct {
	ByAgent map[string]statsUsageAgent `json:"by_agent"`
	Total   statsUsageAgent            `json:"total"`
}

// statsUsageAgent is one agent's tally in a window: Jobs counts every job it ran,
// the sums only the jobs that reported usage.
type statsUsageAgent struct {
	Jobs         int     `json:"jobs"`
	TotalTokens  int64   `json:"total_tokens"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// statsUsageWindows are the windows /v1/stats reports usage for — the dashboard's
// 24h/7d toggle and `agent status` read exactly these keys.
var statsUsageWindows = []time.Duration{24 * time.Hour, 7 * 24 * time.Hour}

// statsUsageBudget caps the usage pass, mirroring statsDBBudget: the two blocks are
// independent reads, so pinning one in a test does not degrade the other.
var statsUsageBudget = 200 * time.Millisecond

func (s *Server) handleStats(c *rux.Context) {
	byStatus, err := s.jobs.Meta().CountJobsByStatus()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "count jobs failed", err.Error())
		return
	}
	// JOB-11: `waiting_dir`（等在同一个目录锁上）对订阅者是"排队中"，与 queued 同组计数——
	// by_status 只报这个桶，不新增一个没人认识的键（详情/列表仍按真实状态过滤）。
	if waiting := byStatus[job.StatusWaitingDir]; waiting > 0 {
		byStatus[job.StatusQueued] += waiting
		delete(byStatus, job.StatusWaitingDir)
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
	usageStats, err := s.jobs.Meta().UsageStats(time.UnixMilli(nowMillis()).Unix(), statsUsageWindows, statsUsageBudget)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read usage stats failed", err.Error())
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
		Usage:              statsUsageFromStore(usageStats),
		EscalationsPending: escalationsPending,
		Projects:           len(s.projects.List()),
		ServerTime:         nowMillis(),
		ServerTZOffsetSec:  serverTZOffsetSec(),
		Version:            s.build.DisplayVersion(),
		UptimeSec:          s.uptimeSec(),
	})
}

// serverTZOffsetSec is the server's current local UTC offset in seconds. It is
// computed per request (a DST switch changes it without a restart) and is what the
// CLI/web render timestamps with (bd h-aii-tnua) — the wire times themselves stay
// unix seconds.
func serverTZOffsetSec() int {
	_, off := time.Now().Zone()
	return off
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

// statsUsageFromStore maps the store's usage aggregate onto the wire shape. A
// window the store did not compute stays absent (Partial says why) and an agent with
// no entry in a window is simply absent from by_agent — the card renders both as
// "no data" rather than as zeros.
func statsUsageFromStore(st jobstore.UsageStats) statsUsage {
	windows := make(map[string]statsUsageWindow, len(st.Windows))
	for label, w := range st.Windows {
		byAgent := make(map[string]statsUsageAgent, len(w.ByAgent))
		for agent, a := range w.ByAgent {
			byAgent[agent] = usageAgentToWire(a)
		}
		windows[label] = statsUsageWindow{ByAgent: byAgent, Total: usageAgentToWire(w.Total)}
	}
	return statsUsage{Windows: windows, Partial: st.Partial}
}

// usageAgentToWire flattens one agent's tally onto the wire struct.
func usageAgentToWire(a jobstore.UsageAgent) statsUsageAgent {
	return statsUsageAgent{
		Jobs:         a.Jobs,
		TotalTokens:  a.TotalTokens,
		InputTokens:  a.InputTokens,
		OutputTokens: a.OutputTokens,
		CostUSD:      a.CostUSD,
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
