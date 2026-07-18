package core

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/answerguard"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/ptyrelay"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	peerhttprunner "github.com/inhere/gofer/internal/runner/peerhttp"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	workerrunner "github.com/inhere/gofer/internal/runner/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// Core bundles the runtime objects assembled from a loaded config: the
// project/agent registries, the runner set, the SQLite metadata store and the
// job service. Both `serve` (HTTP control plane) and `mcp` (stdio MCP server)
// wire the same Core so the MCP tools reuse the identical job.Service (plan §P8:
// "MCP 内部复用 job.Service", 不复制执行逻辑).
type Core struct {
	// snap holds the current (cfg, rev) generation behind a single atomic pointer
	// so any reader gets a self-consistent pair in ONE load (Snapshot/Config).
	// Replaces the old bare Cfg field (its unsynchronised concurrent reads were the
	// tools-cg4 data race). Written only under updateMu, in reloadLocked.
	snap atomic.Pointer[ConfigSnapshot]
	// updateMu serialises EVERY config write path (Update / Reload / ReloadWith):
	// clone → mut → save → reloadLocked all run under it so two concurrent writers
	// can never clone the same snapshot, lose an update, or mint two different
	// configs at the same Rev (B2). Network I/O (the policy broadcast) is
	// deliberately kept OUT of it — see flushPush (D-HIGH-5).
	updateMu sync.Mutex
	// cfgPath is the file config.Save writes to inside the write transaction
	// (empty ⇒ resolved lazily to the user-level path, like the old registry).
	cfgPath string
	// pendingPush records the snapshot a completed write transaction still needs to
	// broadcast. reloadLocked stores it under updateMu; flushPush swaps it out and
	// does the actual PushPolicyAll AFTER the lock is released (never holding
	// updateMu across a network write).
	pendingPush atomic.Pointer[ConfigSnapshot]
	// pushHook, when non-nil, REPLACES the Hub broadcast in flushPush. It exists
	// only so a test can spy on / count the policy-broadcast decision without a live
	// worker connection (verification 4). Production leaves it nil and broadcasts
	// through the Hub.
	pushHook func()
	// onCommit, when non-nil, is called at the END of reloadLocked (UNDER updateMu),
	// exactly once per committed generation in commit order. Test-only: it lets a
	// concurrency test record every (rev, content) pair the serial transaction
	// produced and assert Rev is unique + strictly increasing (verification 5).
	// Production leaves it nil.
	onCommit func(ConfigSnapshot)

	Projects *project.Registry
	Agents   *agent.Registry
	Runners  map[string]runner.Runner
	Store    *jobstore.Store
	Jobs     *job.Service
	// Presence is the E36 driver-agent identity/mailbox service over the same
	// metadata Store. serve injects it into httpapi (SetPresence) + drives its
	// prune sweeper; the mcp standalone path passes it to the local backend.
	Presence *presence.Service
	// workflowEngine is the job-chain workflow engine bound to Jobs (layering design
	// §13). It is the WorkflowAdvancer injected into Jobs (finish→Advance) and the
	// handle serve/httpapi consume via Workflow() to drive workflow
	// submit/advance/query. Always non-nil after Build.
	workflowEngine *workflow.Engine
	// Hub is the ws-worker hub singleton (serve mounts it on /v1/workers/connect;
	// every type=worker runner references this one instance). Always non-nil.
	Hub *wshub.Hub
	// RelayNonces and PtyRelays are live-only serve-side PTY relay state shared by
	// worker dispatch, worker pty-connect (T5) and browser attach (T7).
	RelayNonces *ptyrelay.NonceStore
	PtyRelays   *ptyrelay.Registry
	// detector is the agent.Detector this Core resolved its config with. It is kept
	// so ReloadWith re-gates the built-in agent templates through the SAME seam the
	// process started with (a test's fake detector must not silently become the real
	// PATH probe on reload).
	detector agent.Detector
}

// ConfigSnapshot is one atomic (config, revision) generation. Rev is the config
// generation counter — Build seeds it at 1 and every write transaction
// (reloadLocked) bumps it by 1, so a worker/consumer can drop any Policy whose
// Rev is not strictly newer than the one it already applied. A given Rev value
// therefore identifies exactly ONE config content (B2 verification 5).
type ConfigSnapshot struct {
	Cfg *config.Config
	Rev int64
}

// Snapshot returns the current (cfg, rev) generation in a SINGLE atomic load, so
// the caller never observes a cfg from one generation and a rev from another.
func (c *Core) Snapshot() ConfigSnapshot { return *c.snap.Load() }

// Config returns the current config pointer (= Snapshot().Cfg). It is the
// read-only accessor serve/etc use in place of the old bare Cfg field.
func (c *Core) Config() *config.Config { return c.snap.Load().Cfg }

// BuildOption customises Build.
type BuildOption func(*buildOptions)

type buildOptions struct {
	detector agent.Detector
	cfgPath  string
}

// WithConfigPath tells the Core which file its write transaction (Core.Update)
// persists to. serve passes the discovered config path; when empty the save path
// is resolved lazily to the user-level config (config.UserConfigPath), matching
// the old registry write-back behaviour.
func WithConfigPath(path string) BuildOption {
	return func(o *buildOptions) { o.cfgPath = path }
}

// WithAgentDetector injects the agent.Detector that gates materialization of the
// built-in agent templates (agent.Resolve).
//
// Production callers omit it and get agent.DefaultDetector(). TESTS must inject
// agent.NoopDetector{} (or a fake): with the real probe, the resolved agent set — and
// therefore cfg.Agents, the caps report and any agent-name assertion — would depend on
// which CLIs happen to be installed on the machine running `go test`.
//
// The detector is remembered on the Core and reused by ReloadWith.
func WithAgentDetector(d agent.Detector) BuildOption {
	return func(o *buildOptions) { o.detector = d }
}

// Workflow returns the Core's workflow engine (the handle serve/httpapi consume to
// drive job-chain workflows). Always non-nil after Build.
func (c *Core) Workflow() *workflow.Engine { return c.workflowEngine }

// Close releases the Core's owned resources — currently the SQLite metadata
// store. Callers (serve/mcp) defer it for graceful shutdown so WAL is
// checkpointed and the db handle closed cleanly (design §14).
func (c *Core) Close() error {
	if c == nil || c.Store == nil {
		return nil
	}
	return c.Store.Close()
}

// Build assembles the registries, runner set, metadata store and job service
// from cfg. It is the single wiring point shared by serve and mcp; peer-http
// runners declared in config are registered too (plan §11.1, P7). It opens the
// SQLite metadata db (design §11 ResolveDBPath) and returns an error if that
// fails, since the job service cannot operate without it.
func Build(cfg *config.Config, opts ...BuildOption) (*Core, error) {
	o := buildOptions{detector: agent.DefaultDetector()}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	// THE single merge point (P2 T0-B): this is the only place a serve/mcp/worker
	// process detects the host's agent CLIs and materializes the built-in templates.
	// Every registry below is then wired from this one resolved snapshot, so the job
	// service, the caps report and `gofer agent list` cannot drift apart. Callers must
	// NOT pre-merge (see commands.workerConfigToConfig): a second merge would read the
	// already-injected keys as operator declarations and silently promote them to
	// un-gated escape hatches. Resolve is in-place, so a caller that derives its
	// capability report from the very cfg it passed here observes exactly what was applied.
	// The detect results of that one pass are cached on the agent registry (T5): every
	// availability READER (GET /v1/agents, the MCP ListAgents tool) then serves them
	// instead of re-probing per request.
	cfg, detected := agent.Resolve(cfg, o.detector)
	// Assemble the Core shell first so the project applier can close over it: every
	// project write (Registry.Add/Remove) is routed through THE single serial write
	// transaction c.Update (B2), which clones under updateMu, mutates only the
	// Projects map, saves the副本 and republishes. c's other fields are filled in
	// below and the initial snapshot is stored before Build returns — the applier is
	// only ever invoked at runtime, long after that.
	c := &Core{cfgPath: o.cfgPath, detector: o.detector}
	projects := project.NewRegistry(cfg, o.cfgPath, project.WithProjectApplier(
		func(mut func(map[string]config.ProjectConfig) error) error {
			return c.Update(func(next *config.Config) error {
				if next.Projects == nil {
					next.Projects = map[string]config.ProjectConfig{}
				}
				return mut(next.Projects)
			})
		}))
	agents := agent.NewRegistryWith(cfg, detected)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	// WEB-03: register the pty runner variant ONLY when a pty backend is available
	// on this build/host. Its presence under key "pty" is the capability signal the
	// job service keys on to route interactive jobs (submit.go) — so job never
	// imports the pty packages (G022/G024). No pty backend ⇒ interactive jobs fall
	// through to their requested runner unchanged.
	if ptyrunner.Available() {
		runners[ptyrunner.Name] = ptyrunner.New()
	}

	// Build the ws-worker hub singleton ONCE (unlike peer-http, every worker
	// runner references the same hub instance). Its token→worker bindings come
	// from cfg.Server.Workers (review #1: worker_id is its own caller id).
	hub := wshub.New(workerBindings(cfg))
	relayNonces := ptyrelay.NewNonceStore()
	ptyRelays := ptyrelay.NewRegistry()

	for name, rc := range cfg.Runners {
		switch rc.Type {
		case "peer-http":
			token := ""
			if rc.TokenEnv != "" {
				token = os.Getenv(rc.TokenEnv)
			}
			runners[name] = peerhttprunner.New(name, rc.BaseURL, token)
		case "worker":
			runners[name] = workerrunner.New(name, rc.WorkerID, hub, workerrunner.WithPtyRelay(relayNonces, ptyRelays))
		}
	}
	store, err := jobstore.Open(cfg.ResolveDBPath())
	if err != nil {
		return nil, fmt.Errorf("open metadata store: %w", err)
	}
	// Label-based worker auto-selection (P2/D3) reads live candidates from the hub
	// registry; allowed is the config-registered worker set so only in-册 ids are
	// ever considered.
	sel := &hubWorkerSelector{hub: hub, allowed: cfg.Server.Workers}
	jobs := job.NewService(cfg, projects, agents, runners, store, sel)
	// Bind the workflow engine to the job service (layering design §13.4): the engine
	// reads/drives job-chain workflows over jobs, and is injected back as the
	// WorkflowAdvancer so finish() can advance a chain when a step-job reaches terminal.
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	// E36 presence/mailbox over the same metadata store (G022: presence -> jobstore only).
	pres := presence.NewService(store)
	// Optional config overrides for the online / message TTLs (<=0 keeps the defaults).
	pres.Configure(
		time.Duration(cfg.Presence.TTLSec)*time.Second,
		time.Duration(cfg.Presence.MessageTTLSec)*time.Second,
	)
	// 派生作答白名单闸 (监督分层升级路由 P3.1, design §8.5): gate an attributed driver answer at
	// the SINGLE job.Service chokepoint every answer entry (mcp local/client, http web/CLI)
	// funnels through, so a通用 sup cannot answer outside the whitelist. Wired unconditionally
	// (independent of supervisor.enabled — the poller toggle does not affect the answer闸); the
	// whitelist is the SAME source the L0 answerer reads (cfg.supervisor.allow_prompt_regex), so
	// L0 auto-answer and L2 sup派生作答 share one policy. Role lookup is presence.Role.
	jobs.SetAnswerGuard(answerguard.New(supervisorAllowRegex(cfg), pres))
	c.Projects = projects
	c.Agents = agents
	c.Runners = runners
	c.Store = store
	c.Jobs = jobs
	c.Presence = pres
	c.workflowEngine = eng
	c.Hub = hub
	c.RelayNonces = relayNonces
	c.PtyRelays = ptyRelays
	// Seed generation 1 (verification 5: Build=Rev 1, every write +1). This is the
	// only snap.Store outside reloadLocked; it never re-resolves (the registries above
	// were already built from this resolved cfg).
	c.snap.Store(&ConfigSnapshot{Cfg: cfg, Rev: 1})
	// Wire the per-worker Policy computation seam (T3). Until now (T1) it was nil, so
	// PushPolicyAll was a no-op; from here the hub pushes whatever corePolicySource
	// computes from the current snapshot. Set once at assemble time (single-threaded,
	// before any connection): SetPolicySource only stores the pointer — no PolicyFor
	// runs until a real broadcast, by when snap is already seeded above.
	hub.SetPolicySource(&corePolicySource{core: c})
	return c, nil
}

// supervisorAllowRegex returns the answer闸 / L0 prompt whitelist from cfg.supervisor
// (监督分层升级路由 P3.1). nil supervisor config ⇒ empty whitelist ⇒ a通用 sup can derive-answer
// nothing (maximally conservative); owner/human are still放行 by the guard regardless.
func supervisorAllowRegex(cfg *config.Config) []string {
	if cfg.Supervisor == nil {
		return nil
	}
	return cfg.Supervisor.AllowPromptRegex
}

// hubWorkerSelector adapts the ws-worker hub registry to job.WorkerSelector (P2):
// it walks the config-registered worker ids and emits a candidate for each one
// that currently has a live connection (hub.WorkerSnapshot ok), computing the
// heartbeat age from the snapshot's last-heartbeat timestamp. Offline workers are
// simply absent, so selectWorker only ever picks a connected worker.
type hubWorkerSelector struct {
	hub     *wshub.Hub
	allowed map[string]config.WorkerAuthConfig
}

// Candidates implements job.WorkerSelector.
func (h *hubWorkerSelector) Candidates() []job.WorkerCandidate {
	out := make([]job.WorkerCandidate, 0, len(h.allowed))
	now := time.Now().Unix()
	for id := range h.allowed {
		ws, ok := h.hub.WorkerSnapshot(id) // ok only when the worker is connected
		if !ok {
			continue
		}
		out = append(out, workerCandidate(ws, now))
	}
	return out
}

// Candidate implements job.WorkerSelector exact lookup for explicit/D4 worker
// admission checks.
func (h *hubWorkerSelector) Candidate(workerID string) (job.WorkerCandidate, bool) {
	if _, ok := h.allowed[workerID]; !ok {
		return job.WorkerCandidate{}, false
	}
	ws, ok := h.hub.WorkerSnapshot(workerID)
	if !ok {
		return job.WorkerCandidate{}, false
	}
	return workerCandidate(ws, time.Now().Unix()), true
}

// workerCandidate projects a hub snapshot onto the neutral job.WorkerCandidate
// both selector methods hand to the job layer. Projects/Agents are the worker's
// reported capability keys (federation P2): the job layer validates/filters on
// them, so they must not be dropped here. The snapshot's slices are already
// per-call defensive copies (wshub.WorkerRegistry.WorkerSnapshot), so handing
// them over shares nothing with the live registry.
func workerCandidate(ws wshub.WorkerSnapshot, now int64) job.WorkerCandidate {
	return job.WorkerCandidate{
		WorkerID:     ws.WorkerID,
		Labels:       ws.Labels,
		Projects:     ws.Projects,
		Agents:       ws.Agents,
		AgentCaps:    agentBriefsFromSnapshot(ws),
		InFlight:     ws.InFlight,
		PtyCapable:   ws.PtyCapable,
		HeartbeatAge: time.Duration(now-ws.LastHeartbeat) * time.Second,
		// P3 T4: carry the policy-push diagnostic state through to the job admission gate
		// so a not-yet-applied worker gets a clearer error (policy_pending) — never a reject.
		PolicyPending: ws.PolicyPending,
		PolicyRev:     ws.PolicyRev,
		AppliedRev:    ws.AppliedRev,
	}
}

// agentBriefsFromSnapshot converts the wshub snapshot's typed agent capabilities
// into job's local AgentBrief type (tools-c9v G2 interactive-only gate) — the same
// "range without naming wsproto.AgentBrief" trick serve.briefsFromSnapshot uses,
// so job stays free of any wshub/wsproto import (G022). Only Key/Interactive are
// carried: G2 gates on interactive-ness alone, unlike the display-only
// httpapi/serve copy which also keeps Type/Available/Version.
func agentBriefsFromSnapshot(ws wshub.WorkerSnapshot) []job.AgentBrief {
	if len(ws.AgentCaps) == 0 {
		return nil
	}
	out := make([]job.AgentBrief, 0, len(ws.AgentCaps))
	for _, c := range ws.AgentCaps {
		out = append(out, job.AgentBrief{Key: c.Key, Interactive: c.Interactive})
	}
	return out
}

// workerBindings builds the hub's worker_id → caller-id binding map from
// cfg.Server.Workers. The worker's own id IS its caller id (review #1): the
// presented token must resolve (via the server's caller table) to the same
// worker_id. A nil/empty map means no worker may register.
func workerBindings(cfg *config.Config) map[string]string {
	if len(cfg.Server.Workers) == 0 {
		return nil
	}
	out := make(map[string]string, len(cfg.Server.Workers))
	for workerID := range cfg.Server.Workers {
		out[workerID] = workerID
	}
	return out
}

// Update is THE single serial config write transaction (B2). It clones the
// current snapshot, applies mut to the clone (project whole-value add/remove
// ONLY, per the project applier contract), saves the副本 to disk, then swaps the
// new generation into every component and marks it for broadcast. The whole
// clone→mut→save→reload runs under updateMu, so two concurrent writers can never
// clone the same snapshot / lose an update / mint two configs at one Rev.
//
// Fail-safe: if mut or the save fails the OLD generation is untouched — nothing
// is swapped, disk is unchanged (save writes a副本, never the live config) and no
// broadcast happens. The policy broadcast is done by flushPush AFTER updateMu is
// released (never a network write under the lock, D-HIGH-5).
func (c *Core) Update(mut func(*config.Config) error) error {
	err := c.updateLocked(mut)
	c.flushPush() // no-op unless updateLocked published a new generation
	return err
}

// updateLocked runs the write transaction under updateMu and returns without
// broadcasting (the caller's flushPush does that off-lock). Split out precisely
// so flushPush is NOT deferred alongside the unlock: a `defer flushPush()` would,
// by LIFO, run before the deferred Unlock and drag the broadcast back under the
// lock (G-H2). Here the unlock is this function's defer; flushPush is the
// caller's next statement.
func (c *Core) updateLocked(mut func(*config.Config) error) error {
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	next := c.snap.Load().Cfg.Clone()
	if err := mut(next); err != nil {
		return err // old generation untouched: no save, no publish
	}
	if err := c.saveConfig(next); err != nil {
		return err // save failed: do NOT publish (disk/snapshot/registries stay old)
	}
	c.reloadLocked(next)
	return nil
}

// saveConfig persists cfg (a副本) to cfgPath, resolving the user-level path lazily
// (and caching it) when none was configured — mirroring the old registry save.
// Called only under updateMu.
func (c *Core) saveConfig(cfg *config.Config) error {
	if c.cfgPath == "" {
		p, err := config.UserConfigPath()
		if err != nil {
			return fmt.Errorf("resolve user config path: %w", err)
		}
		c.cfgPath = p
	}
	return config.Save(c.cfgPath, cfg)
}

// Reload re-loads the config from path and atomically swaps it into every
// component that holds a config snapshot — the project/agent registries and the
// job service (C3 SIGHUP hot-reload). It does NOT restart the process, touch
// in-flight jobs or reopen the jobstore.
//
// 🔴 It holds updateMu from the entry and reads the file INSIDE the lock
// (D-HIGH-4): a SIGHUP that read the file off-lock could load a stale file A,
// race a web Update that publishes B, then re-publish A as a newer Rev — disk B,
// memory A, the web edit lost. Locking the whole stat→load→overlay→reload closes
// that window.
//
// Fail-safe: if the new config fails to load/validate, the OLD generation is kept
// (nothing is swapped) and the error is returned so the caller can log and keep
// serving.
//
// LIMITATION: the runner instances in Core.Runners are built once at assemble
// time and are NOT rebuilt on reload. Swapping the config makes the peer-runner
// classification and every allowlist/validation observe the new config, but
// adding a brand-new runner TYPE (a new peer-http entry) still needs a restart to
// instantiate its runner. Reload covers adding/removing projects and agents and
// any config-derived validation.
func (c *Core) Reload(path string) error {
	err := c.reloadFromPathLocked(path)
	c.flushPush()
	return err
}

func (c *Core) reloadFromPathLocked(path string) error {
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	// Fail-safe for a deleted config file: config.Load returns a fresh EMPTY config
	// (no error) when the file is missing — fine on first run, but on a reload that
	// would silently wipe all projects/agents. When an explicit path was given and it
	// no longer exists, treat the reload as a failure and keep the old config
	// (path=="" is default-resolution mode and keeps prior behaviour).
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("reload config: %w", err)
		}
	}
	newCfg, _, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}
	// D6: reload merges overlays too. Fail-safe — overlay parse failures only warn
	// (returned slice), they never make the reload fail. On this runtime reload path
	// there is no console handle, so log each warn via slog instead of dropping it.
	for _, w := range config.ApplyProjectOverlays(newCfg) {
		slog.Warn("config reload: overlay warn", "detail", w)
	}
	c.reloadLocked(newCfg)
	return nil
}

// ReloadWith swaps an ALREADY-BUILT config into every component that holds a
// config snapshot. It is the apply half of Reload, split out so a caller that
// obtains its config from somewhere other than a server config file (a worker
// builds one from worker.yaml) reuses the exact same apply path instead of
// re-implementing it.
//
// Error semantics: the component Reloads are plain atomic.Store calls and CANNOT
// fail. The only error ReloadWith can return is the nil-config precondition.
// "A bad config keeps the old one" is achieved by BUILDING the new config
// completely (load/parse/validate) BEFORE calling this, so a bad config never
// reaches the apply stage.
//
// The apply is safe against concurrent Submit: every consumer that needs more
// than one config-derived fact takes ONE snapshot and uses it throughout
// (job.Service.Submit resolves policy AND the agent argv from that one snapshot),
// so a submit racing this swap sees either the old config or the new one, never a
// mix. See Reload for the runner-instance limitation.
func (c *Core) ReloadWith(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("reload config: nil config")
	}
	c.reloadWithLocked(cfg)
	c.flushPush()
	return nil
}

func (c *Core) reloadWithLocked(cfg *config.Config) {
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	c.reloadLocked(cfg)
}

// reloadLocked is the atomic换代: it ASSUMES updateMu is held. It runs the single
// agent resolve pass, bumps Rev by 1, stores the new (cfg, rev) snapshot in ONE
// atomic swap, repoints the component registries at the same cfg, then records
// the snapshot as pending-broadcast — it never touches the network (that is
// flushPush's job, off-lock, D-HIGH-5).
//
// The resolve pass is why a newly installed CLI shows up on SIGHUP / `worker
// reload` and an uninstalled one disappears; reusing c.detector keeps a test's
// fake from silently turning into the real PATH probe. Resolve is in-place on
// cfg, which — on the Update path — is a private Clone, so its delete/insert of
// injected agent keys never tears a running Submit's snapshot.
func (c *Core) reloadLocked(cfg *config.Config) {
	cfg, detected := agent.Resolve(cfg, c.detector)
	snap := &ConfigSnapshot{Cfg: cfg, Rev: c.snap.Load().Rev + 1}
	c.snap.Store(snap) // ★ one atomic换代
	c.Projects.Reload(cfg)
	c.Agents.ReloadWith(cfg, detected)
	c.Jobs.Reload(cfg)
	c.pendingPush.Store(snap) // ★ only mark for broadcast; do not write frames here
	if c.onCommit != nil {
		c.onCommit(*snap)
	}
}

// flushPush broadcasts the latest published generation to worker connections
// AFTER updateMu is released. It is a no-op when nothing was published (mut/save
// failed, or no write happened), and harmless when the hub has no policy source
// wired yet (T1: PushPolicyAll is a no-op until T3 sets one).
func (c *Core) flushPush() {
	if s := c.pendingPush.Swap(nil); s != nil {
		if c.pushHook != nil {
			c.pushHook()
			return
		}
		c.Hub.PushPolicyAll()
	}
}
