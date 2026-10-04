package httpapi

import (
	"net/http"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/messenger"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
	"github.com/inhere/gofer/internal/workerupgrade"
)

// nowMillis returns the current unix time in milliseconds. It is a package var so
// tests can pin the clock for deterministic heartbeat-age assertions.
var nowMillis = func() int64 { return time.Now().UnixMilli() }

// Runner kind constants used by the /v1/runners view and the type switch.
const (
	runnerTypeLocal    = "local"
	runnerTypePeerHTTP = "peer-http"
	runnerTypeWorker   = "worker"
)

// Runner status values (C6/P4 §5). `unknown` covers a peer-http runner with no
// probe result yet, a worker with no registry wired (P3 not enabled), and any
// future/unrecognised runner type.
const (
	statusUp           = "up"           // local (always) / peer-http probe 2xx
	statusDown         = "down"         // peer-http probe failed
	statusConnected    = "connected"    // worker has a live connection
	statusDisconnected = "disconnected" // worker offline / never connected
	statusUnknown      = "unknown"      // no signal yet
)

// AgentBrief is httpapi's local typed agent capability (key + type + interactive),
// the detail the web capability cascade needs beyond a bare agent key. It mirrors
// wsproto.AgentBrief but is redeclared here so httpapi never imports wshub/wsproto
// (the D2 boundary): the serve-side adapter converts the wshub snapshot into this
// type. It is exported because that adapter (package serve) constructs it. The JSON
// tags match wsproto/the web MetaAgent shape (type/interactive omitempty).
type AgentBrief struct {
	Key         string `json:"key"`
	Type        string `json:"type,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Batch       bool   `json:"batch"`
	// Available/Version are DISPLAY-ONLY, and Available is a *bool for a reason —
	// see wsproto.AgentBrief for the full rule. Short version: nil = the worker never
	// reported it (pre-P2 build), false = a probe did not find the CLI. NEITHER means
	// "cannot run": an operator-declared agent stays runnable whatever the probe says.
	// Nothing in this package (or the web console) may filter or grey out an agent on
	// this field; the agent list is the authority on what exists.
	Available *bool  `json:"available,omitempty"`
	Version   string `json:"version,omitempty"`
}

// WorkerStatus is the read-only worker view the handler renders (C6/P4). It is
// produced by the serve-side adapter from the wshub registry snapshot, so httpapi
// never imports wshub's internal types. Connected=false means offline / never
// connected (rendered as `disconnected`). The /v1/workers/{id} views serialize it
// directly, so it carries the API's snake_case field names.
type WorkerStatus struct {
	Connected     bool     `json:"connected"`
	LastHeartbeat int64    `json:"last_heartbeat"` // unix millis of the most recent inbound frame
	InFlight      int      `json:"in_flight"`
	Labels        []string `json:"labels,omitempty"`
	Projects      []string `json:"projects,omitempty"`
	// Agents stays the bare agent-key list (validation / selector, back-compat);
	// AgentCaps carries the typed detail (type/interactive) the UI cascade needs.
	Agents    []string     `json:"agents,omitempty"`
	AgentCaps []AgentBrief `json:"agent_caps,omitempty"`
	// Node info reported by the worker on register (P1), surfaced for the P4
	// runners observability panel.
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
	// Hostname is the worker's self-reported machine hostname; RemoteAddr is the
	// conn's remote address as the hub saw it (may be a NAT/bridge address —
	// hostname is the machine-identifying field).
	Hostname     string `json:"hostname,omitempty"`
	RemoteAddr   string `json:"remote_addr,omitempty"`
	GoferVersion string `json:"gofer_version,omitempty"`
	StartedAt    int64  `json:"started_at,omitempty"` // worker process start, unix seconds
	// ProtocolVersion is the wire version this worker registered with. Surfaced so
	// an operator can spot a too-old worker (reload/policy gated by
	// wsproto.SupportsReload/SupportsPolicy) before a reload/policy push 409s.
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	MessengerStatus string `json:"messenger_status,omitempty"`
	// MessengerDetail and Dirs come from the worker's heartbeat (protocol v16). nil =
	// the worker never reported them (an older build, or no heartbeat yet): the console
	// shows "unknown", not "stopped".
	MessengerDetail *messenger.Snapshot `json:"messenger_detail,omitempty"`
	Dirs            *DirsView           `json:"dirs,omitempty"`
	// Draining is true while the worker is upgrading itself and takes no new jobs.
	Draining       bool              `json:"draining,omitempty"`
	PolicyPending  bool              `json:"policy_pending,omitempty"`
	PolicyRev      int64             `json:"policy_rev,omitempty"`
	AppliedRev     int64             `json:"applied_rev,omitempty"`
	PolicyRejected []PolicyRejection `json:"policy_rejected,omitempty"`
	PolicyDegraded []PolicyDegrade   `json:"policy_degraded,omitempty"`
}

// DirsView is where a runner runs things: the machine's default workspace
// (GOFER_WORKSPACE or ~/.gofer/workspace), the roots mapping of a policy-mode
// worker and the directory every served project resolves to. Exists is judged on
// the runner's own machine at report time. A project that could not be resolved at
// all is not here — its reason is the worker's policy_rejected entry.
type DirsView struct {
	Workspace *DirEntry         `json:"workspace,omitempty"`
	Roots     []RootEntry       `json:"roots,omitempty"`
	Projects  []ProjectDirEntry `json:"projects,omitempty"`
}

// DirEntry is one directory and whether it exists.
type DirEntry struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

// RootEntry is one roots mapping (server-side prefix -> the worker's local path).
type RootEntry struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Exists bool   `json:"exists"`
}

// ProjectDirEntry is the directory a project resolves to on the runner.
type ProjectDirEntry struct {
	Key    string `json:"key"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

type PolicyRejection struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}
type PolicyDegrade struct {
	Key  string `json:"key"`
	Gate string `json:"gate"`
}

// runnerProber is the consumer-side narrow interface (D2) the handler reads the
// peer-http probe cache through. The serve command injects an implementation;
// nil means no probing is wired (every peer-http row is `unknown`). Snapshot must
// be a non-blocking read of the cached results (never a live probe).
type runnerProber interface {
	Snapshot() []runner.ProbeResult
}

// workerRegistry is the consumer-side narrow interface (D2) the handler reads
// worker connection state through. nil means the registry is not wired (every
// worker row is `unknown`). WorkerStatus returns ok=false when the worker_id is
// unknown / offline.
type workerRegistry interface {
	WorkerStatus(workerID string) (WorkerStatus, bool)
}

// runnerView is one row of the /v1/runners response (C6/P4 §5). Type-specific
// blocks (Probe / Worker / BaseURL / WorkerID) are omitted when empty so a local
// runner stays a clean object.
type runnerView struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Messenger string `json:"messenger,omitempty"`
	// MessengerDetail is the resident messenger's snapshot (local: read in-process;
	// worker: the last heartbeat, nil for a worker that never reported one).
	MessengerDetail *messenger.Snapshot `json:"messenger_detail,omitempty"`
	// Dirs is the runner's working-directory report (see DirsView).
	Dirs *DirsView `json:"dirs,omitempty"`

	// Capabilities is the projects + typed agents this runner can serve, so the web
	// can cascade project→agent per runner (P4 T4.3). It is present on the implicit
	// local row (synthesized from the server's own config) and on connected worker
	// rows (from the worker's register report) — uniform across runner types; a
	// peer-http row has no known capabilities and omits it.
	Capabilities *capsView `json:"capabilities,omitempty"`

	// peer-http
	BaseURL string     `json:"base_url,omitempty"`
	Probe   *probeView `json:"probe,omitempty"`

	// worker
	WorkerID string      `json:"worker_id,omitempty"`
	Worker   *workerView `json:"worker,omitempty"`
	// Upgrade is the worker's latest remote-upgrade record (worker rows only).
	Upgrade        *workerupgrade.Record  `json:"upgrade,omitempty"`
	UpgradeHistory []workerupgrade.Record `json:"upgrade_history,omitempty"`
}

// capsView is a runner's capability summary (projects + typed agents) the web reads
// to cascade project→agent for a chosen runner. On worker rows the same data also
// lives in .worker; on the local row it is synthesized from the server's config.
type capsView struct {
	Projects  []string     `json:"projects,omitempty"`
	AgentCaps []AgentBrief `json:"agent_caps,omitempty"`
}

// probeView is the peer-http probe detail (millis timestamp / latency / error).
type probeView struct {
	CheckedAt int64  `json:"checked_at"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// workerView is the worker connection detail. HeartbeatAgeMS is computed at read
// time from LastHeartbeat so the operator sees staleness directly. AgentCaps + node
// info (P4) enrich the observability panel with the worker's typed capabilities and
// runtime; Agents stays the bare-key list for back-compat.
type workerView struct {
	LastHeartbeat  int64        `json:"last_heartbeat"`
	HeartbeatAgeMS int64        `json:"heartbeat_age_ms"`
	InFlight       int          `json:"in_flight"`
	Labels         []string     `json:"labels,omitempty"`
	Projects       []string     `json:"projects,omitempty"`
	Agents         []string     `json:"agents,omitempty"`
	AgentCaps      []AgentBrief `json:"agent_caps,omitempty"`
	// Node info reported on register (P1).
	OS           string `json:"os,omitempty"`
	Arch         string `json:"arch,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	RemoteAddr   string `json:"remote_addr,omitempty"`
	GoferVersion string `json:"gofer_version,omitempty"`
	StartedAt    int64  `json:"started_at,omitempty"`
	// ProtocolVersion is the worker's wire version (see WorkerStatus.ProtocolVersion).
	ProtocolVersion int                 `json:"protocol_version,omitempty"`
	MessengerStatus string              `json:"messenger_status,omitempty"`
	MessengerDetail *messenger.Snapshot `json:"messenger_detail,omitempty"`
	Dirs            *DirsView           `json:"dirs,omitempty"`
	// Draining is true while the worker is upgrading itself and takes no new jobs.
	Draining       bool              `json:"draining,omitempty"`
	PolicyPending  bool              `json:"policy_pending,omitempty"`
	PolicyRev      int64             `json:"policy_rev,omitempty"`
	AppliedRev     int64             `json:"applied_rev,omitempty"`
	PolicyRejected []PolicyRejection `json:"policy_rejected,omitempty"`
	PolicyDegraded []PolicyDegrade   `json:"policy_degraded,omitempty"`
}

// handleListRunners returns the status of every configured runner plus the
// implicit `local` runner (C6/P4 §5). The list is always a non-nil array, so an
// empty runner set serialises as {"runners":[{local}]}. Rows are sorted by name
// for a stable response (local first). The handler only reads cached state (probe
// snapshot + registry snapshot) so it never blocks on a live probe.
func (s *Server) handleListRunners(c *rux.Context) {
	out := make([]runnerView, 0, util.CapSum(len(s.runners), 1))

	// The implicit local runner is always present and always up (in-process). Its
	// capabilities are synthesized from the server's own config so the web can
	// cascade project→agent on local just as it does for worker runners (P4 T4.3).
	out = append(out, runnerView{
		Name:         runnerTypeLocal,
		Type:         runnerTypeLocal,
		Status:       statusUp,
		Messenger:    s.residentMessengerStatus(runnerTypeLocal),
		Capabilities: s.localCapabilities(),
	})
	out[0].MessengerDetail = s.residentMessengerSnapshot(runnerTypeLocal)
	out[0].Dirs = s.localDirs()

	probes := s.probeIndex()
	for name, rc := range s.runners {
		// A runner explicitly named "local" in config would collide with the
		// implicit row; skip it (local is always rendered above).
		if rc.Type == runnerTypeLocal || name == runnerTypeLocal {
			continue
		}
		out = append(out, s.renderRunner(name, rc, probes))
	}

	sort.Slice(out, func(i, j int) bool {
		// local first, then by name — deterministic regardless of map order.
		if out[i].Type == runnerTypeLocal && out[j].Type != runnerTypeLocal {
			return true
		}
		if out[j].Type == runnerTypeLocal && out[i].Type != runnerTypeLocal {
			return false
		}
		return out[i].Name < out[j].Name
	})

	// server describes THIS server's platform and version: the console needs it to tell
	// whether the server's own binary can upgrade a worker (same os/arch only).
	c.JSON(http.StatusOK, map[string]any{
		"runners": out,
		"server":  map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "version": s.build.DisplayVersion()},
	})
}

func (s *Server) residentMessengerStatus(runner string) string {
	if s.residentMessenger == nil {
		return messenger.StatusStopped
	}
	return s.residentMessenger.Status(runner)
}

// residentMessengerSnapshot is the local runner's messenger view (nil when the
// server runs without a session relay).
func (s *Server) residentMessengerSnapshot(runner string) *messenger.Snapshot {
	if s.residentMessenger == nil {
		return nil
	}
	snap := s.residentMessenger.Snapshot(runner)
	return &snap
}

// localDirs reports the server's own working directories: the default workspace
// and the execution path (Config.ExecPath — the G002 view this process runs in) of
// every project.
func (s *Server) localDirs() *DirsView {
	out := &DirsView{}
	if ws, err := config.WorkspaceDir(""); err == nil && ws != "" {
		out.Workspace = &DirEntry{Path: ws, Exists: dirExists(ws)}
	}
	if s.projects != nil {
		if cfg := s.projects.Config(); cfg != nil {
			keys := make([]string, 0, len(cfg.Projects))
			for k := range cfg.Projects {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := cfg.ExecPath(cfg.Projects[k])
				out.Projects = append(out.Projects, ProjectDirEntry{Key: k, Path: p, Exists: dirExists(p)})
			}
		}
	}
	return out
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// probeIndex reads the prober snapshot (nil-safe) into a name→result map for O(1)
// lookup. A nil prober (no probing wired) yields an empty map so every peer-http
// row falls to `unknown`.
func (s *Server) probeIndex() map[string]runner.ProbeResult {
	idx := map[string]runner.ProbeResult{}
	if s.prober == nil {
		return idx
	}
	for _, pr := range s.prober.Snapshot() {
		idx[pr.Name] = pr
	}
	return idx
}

// renderRunner builds one runnerView from its config + the probe index. The
// per-type logic: local => up; peer-http => probe cache (up/down/unknown); worker
// => registry snapshot (connected/disconnected/unknown); unknown type => unknown.
func (s *Server) renderRunner(name string, rc config.RunnerConfig, probes map[string]runner.ProbeResult) runnerView {
	v := runnerView{Name: name, Type: rc.Type, Status: statusUnknown}
	switch rc.Type {
	case runnerTypeLocal:
		v.Status = statusUp
	case runnerTypePeerHTTP:
		v.BaseURL = rc.BaseURL
		if pr, ok := probes[name]; ok {
			v.Status = statusDown
			if pr.Up {
				v.Status = statusUp
			}
			v.Probe = &probeView{CheckedAt: pr.CheckedAt, LatencyMS: pr.LatencyMS, Error: pr.Err}
		}
		// no probe result yet (or no prober wired) => status stays `unknown`.
	case runnerTypeWorker:
		v.WorkerID = rc.WorkerID
		v.Status = s.renderWorkerStatus(rc.WorkerID, &v)
		v.Upgrade = s.latestUpgrade(rc.WorkerID)
		v.UpgradeHistory = s.upgradeHistory(rc.WorkerID)
	}
	return v
}

// renderWorkerStatus resolves a worker runner's status from the registry and, when
// connected, fills v.Worker. A nil registry (P3 not wired) => `unknown`; a missing
// / offline worker => `disconnected`.
func (s *Server) renderWorkerStatus(workerID string, v *runnerView) string {
	if s.workers == nil {
		return statusUnknown
	}
	ws, ok := s.workers.WorkerStatus(workerID)
	if !ok || !ws.Connected {
		return statusDisconnected
	}
	age := nowMillis() - ws.LastHeartbeat
	if age < 0 {
		age = 0
	}
	v.Worker = &workerView{
		LastHeartbeat:   ws.LastHeartbeat,
		HeartbeatAgeMS:  age,
		InFlight:        ws.InFlight,
		Labels:          ws.Labels,
		Projects:        ws.Projects,
		Agents:          ws.Agents,
		AgentCaps:       ws.AgentCaps,
		OS:              ws.OS,
		Arch:            ws.Arch,
		Hostname:        ws.Hostname,
		RemoteAddr:      ws.RemoteAddr,
		GoferVersion:    ws.GoferVersion,
		StartedAt:       ws.StartedAt,
		ProtocolVersion: ws.ProtocolVersion,
		MessengerStatus: ws.MessengerStatus,
		MessengerDetail: ws.MessengerDetail,
		Dirs:            ws.Dirs,
		Draining:        ws.Draining,
		PolicyPending:   ws.PolicyPending,
		PolicyRev:       ws.PolicyRev,
		AppliedRev:      ws.AppliedRev,
		PolicyRejected:  ws.PolicyRejected,
		PolicyDegraded:  ws.PolicyDegraded,
	}
	// Surface the same capability summary uniformly on the runner row so the web can
	// cascade project→agent for a worker runner exactly as it does for local (P4).
	v.Capabilities = &capsView{Projects: ws.Projects, AgentCaps: ws.AgentCaps}
	v.MessengerDetail, v.Dirs = ws.MessengerDetail, ws.Dirs
	// The status string follows the heartbeat when there is one; the register-time
	// value covers the first seconds (and workers that predate the heartbeat report).
	v.Messenger = ws.MessengerStatus
	if ws.MessengerDetail != nil {
		v.Messenger = ws.MessengerDetail.Status
	}
	return statusConnected
}

// localCapabilities synthesizes the implicit local runner's capability summary:
// every configured project key + the resolved agent set (built-in exec always
// present, with its true type), so the web can cascade project→agent for the local
// runner just as it does for worker runners (P4 T4.3).
func (s *Server) localCapabilities() *capsView {
	return &capsView{Projects: s.projects.List(), AgentCaps: s.localAgentCaps()}
}

// localAgentCaps builds the typed agent capability list for the local runner from
// the RESOLVED agent registry (agent.Registry.List includes the built-in exec with
// its normalised type — mirroring P1's worker report), sorted by key for a stable
// response. Reading the resolved registry (not the raw config map) is what keeps the
// local capability set from under-reporting exec.
//
// Available/Version are filled from the SAME cache handleListAgents reads
// (agent.Registry.Availability): a map-lookup / already-memoized read, not a live
// probe, so calling it on every /v1/runners poll (web polls at 4s) costs no extra
// child-process spawns — it turns over only on reload, exactly like /v1/agents.
// This keeps the two observability surfaces reporting the same fact instead of
// /v1/runners always showing agent_caps.available=null.
func (s *Server) localAgentCaps() []AgentBrief {
	list := s.agents.List()
	avail := s.agents.Availability()
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]AgentBrief, 0, len(keys))
	for _, k := range keys {
		det := avail[k]
		out = append(out, AgentBrief{
			Key:         k,
			Type:        list[k].Type,
			Interactive: list[k].Interactive,
			Available:   &det.Available,
			Version:     det.Version,
		})
	}
	return out
}
