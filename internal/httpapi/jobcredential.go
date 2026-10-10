package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// SEC-01: the `job` caller kind and its permission table.
//
// A job process authenticates with the credential gofer minted for it
// (GOFER_JOB_TOKEN), not with the operator's bearer token — the token is gone from
// its environment entirely. The credential is deliberately narrow, and the table
// below is the whole of it:
//
//	                        member job          leader job
//	read (every GET)        ✓                   ✓
//	comment                 ✓ (records, never   ✓ (may @-dispatch from its plan's
//	                          dispatches)         threads, throttled + allowlisted)
//	ask a human (decisions) ✓                   ✓
//	wakeup on ITS OWN job   ✓                   ✓
//	plan set-todo           ✗                   ✓ — its own plan, ready|skipped only
//	submit a job            ✗                   ✗ (member only, and only when the
//	                          asking agent/role sets can_submit)
//	accept/reject/cancel,   ✗                   ✗
//	config writes, skills,
//	xfer, everything else
//
// W2b adds a third credential kind, the STEWARD's (jobstore.JobCredentialSteward). Unlike the
// member / leader credentials — which read the whole GET surface — it is default-DENY on
// reads too: only the routes in stewardReadAllow / stewardWriteAllow pass (the work items
// and their journal / requests, session reads and the read-only transcript tail, job
// states, the steward's own status and notes). Everything else — submitting a job, config,
// done / dropped, merge / split, deletes — is a 403 before any handler runs; the handlers
// then apply the target-level rules (work.StewardUpdate).
//
// The default is DENY: jobCredentialMiddleware refuses any write route that is not
// on the allowlist before the handler runs, so a newly registered endpoint is closed
// to job callers until someone opens it here on purpose. Reads pass — a job that
// cannot read its own plan or a thread back is useless, and every read route is
// already open to any authenticated caller.
//
// The allowed routes then apply their TARGET-level rule (own job, own plan, allowed
// agent): the middleware knows the method and the route, not the body.

// ctxJobID / ctxJobKind / ctxPlanID are the extra rux context keys a job caller gets
// on top of ctxCallerID/ctxCallerKind: which job the credential belongs to, which
// half of the table applies (member|leader) and the plan a leader is scoped to.
const (
	ctxJobID   = "job_id"
	ctxJobKind = "job_kind"
	ctxPlanID  = "plan_id"
	// ctxStewardAgent is the agent of a steward credential's job (the speaker label of
	// everything it writes is steward(<agent>)).
	ctxStewardAgent = "steward_agent"
)

// jobCaller is the resolved identity of a job credential, read out of the request
// context by the handlers that need a target-level rule.
type jobCaller struct {
	JobID  string
	Kind   string // jobstore.JobCredentialMember | JobCredentialLeader | JobCredentialSteward
	PlanID string
	// Agent is the steward job's agent ("" for the other kinds).
	Agent string
}

// isLeader reports whether the credential is a leader job's, i.e. whether the leader
// half of the permission table applies.
func (jc jobCaller) isLeader() bool { return jc.Kind == jobstore.JobCredentialLeader }

// isSteward reports whether the credential is the steward's.
func (jc jobCaller) isSteward() bool { return jc.Kind == jobstore.JobCredentialSteward }

// describe names the credential kind the way the 403 body does.
func (jc jobCaller) describe() string {
	switch {
	case jc.isLeader():
		return "leader job"
	case jc.isSteward():
		return "steward"
	}
	return "member job"
}

// jobCallerFromCtx returns the job credential identity stored by authMiddleware, and
// false for every other caller kind (a user, a worker, an empty-token passthrough).
func jobCallerFromCtx(c *rux.Context) (jobCaller, bool) {
	if callerKindFromCtx(c) != callerKindJob {
		return jobCaller{}, false
	}
	jc := jobCaller{}
	if v, ok := c.Get(ctxJobID); ok {
		jc.JobID, _ = v.(string)
	}
	if v, ok := c.Get(ctxJobKind); ok {
		jc.Kind, _ = v.(string)
	}
	if v, ok := c.Get(ctxPlanID); ok {
		jc.PlanID, _ = v.(string)
	}
	if v, ok := c.Get(ctxStewardAgent); ok {
		jc.Agent, _ = v.(string)
	}
	return jc, true
}

// jobReadRoutes pass for a job caller without further checks: the whole GET surface.
// It is stated as a predicate rather than a list because a read route added later
// must not silently become invisible to jobs — reads leak nothing a job's own
// credential could not already reach, and the alternative (a new endpoint 403s a job
// for no reason) is the kind of failure nobody notices until someone debugs it.
func jobCallerMayRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// jobRouteWords are the literal path segments the /v1 surface uses. A segment that is
// NOT one of them is a parameter (a job/plan/todo/agent id), which is what lets the
// tables below be written once, in a stable `<METHOD> /v1/jobs/*/cancel` form.
//
// The path is matched rather than rux's route template on purpose: this middleware runs
// BEFORE the handler chain, and rux publishes the matched route only around it (the
// metrics middleware reads it after c.Next() for exactly that reason). Matching the
// request line keeps the gate independent of when the router settles.
var jobRouteWords = map[string]bool{
	"v1": true, "jobs": true, "plans": true, "todos": true, "comments": true, "handoff": true,
	"wakeups": true, "decisions": true, "workflows": true, "skills": true, "xfer": true,
	"sessions": true, "watches": true, "schedules": true, "retries": true, "config": true, "agents": true,
	"projects": true, "workers": true, "messages": true, "meta": true, "stats": true,
	"runners": true, "tunnels": true, "interactions": true, "accept": true, "reject": true,
	"cancel": true, "resume": true, "rebuild": true, "worktree": true, "run": true,
	"pause": true, "dispatch": true, "answer": true, "punt": true, "content": true,
	"import": true, "update": true, "validate": true, "reload": true, "enable": true,
	"disable": true, "run-now": true, "rotate-token": true, "probe": true, "server": true,
	"attach-ticket": true, "register": true, "deregister": true, "inbox": true, "poll": true,
	"precheck": true, "events": true, "deliveries": true, "artifacts": true, "diff": true,
	"request": true, "stream": true, "logs": true, "stdout": true, "stderr": true,
	"workbench": true, "threads": true, "turn": true, "review": true, "seen-all": true, "layout": true,
	"say": true, "end": true, "ws-ticket": true,
	"work-items": true, "delete": true, "journal": true, "merge": true, "split": true, "report": true, "report-request": true,
	"links": true, "digest": true, "requests": true, "summarize": true, "suggestions": true, "summarizer": true,
	// W2b: the steward surface, the session tail and the merge suggestions.
	"steward": true, "notes": true, "start": true, "stop": true, "restart": true, "ask": true,
	"review-summary": true, "tail": true, "merge-suggestions": true, "session-ask": true, "issues": true,
	"tracker": true, "sync": true, "repos": true, "rename": true,
	// N2 §E: session nudges. Writes stay default-denied for every job credential
	// (a steward does not set timers on sessions: it proposes, the person decides).
	"nudges": true,
	// Terminal permission prompts: every write is a person's (default-denied for jobs).
	"permissions": true, "resolve": true,
	// N3: the home page (reads only for job credentials; the action audit is a person's).
	"today": true, "actions": true, "handled": true,
	// N3 T3: 「稍后」 (snooze / put back are a person's; the list is a read).
	"snooze": true, "snoozed": true,
	// N3 T4: the steward's advice on a card (steward / person only).
	"advice": true,
	// P4 memory hygiene: findings / suggestions (adopt / dismiss stay a person's).
	"memory-findings": true, "memory-suggestions": true, "adopt": true, "dismiss": true,
	// Tunnel surface literals: without them every tunnel write collapsed to
	// `/v1/tunnels/*` and its refusal lost the action wording below.
	"forwarders": true, "hosted": true, "presets": true, "local-presets": true,
	// Scoped memories: a job may flag one it found out of date (gofer-3nxa.1).
	"memories": true, "flag": true,
}

// jobRouteKey reduces a request to the `<METHOD> <collapsed path>` key the SEC-01 tables
// use: every non-literal segment becomes `*`.
func jobRouteKey(method, path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segments {
		if !jobRouteWords[seg] {
			segments[i] = "*"
		}
	}
	return method + " /" + strings.Join(segments, "/")
}

// jobWriteAllowlist is every non-read route a job credential may reach AT ALL. Each one
// then applies its own target-level rule in the handler. A route absent from this map is
// refused before the handler runs, so a newly registered endpoint is closed to job
// callers until someone opens it here deliberately.
var jobWriteAllowlist = map[string]bool{
	// Comment threads: a job's agent reports progress and answers a human. The author
	// comes from the credential and whether the comment may dispatch work is decided in
	// the job layer from the credential's KIND — a member's comment is recorded, a
	// leader's may hand out work inside its own plan (MCP-05 阶段 B, unchanged).
	"POST /v1/jobs/*/comments":          true,
	"POST /v1/plans/*/comments":         true,
	"PUT /v1/plans/*/handoff":           true,
	"POST /v1/plans/*/todos/*/comments": true,
	"POST /v1/todos/*/comments":         true,
	// ask_human: a job that needs a decision raises one (Part C §C3).
	"POST /v1/decisions": true,
	// A wakeup on the job ITSELF: "continue me when this fires". "Own" is enforced in
	// the handler.
	"POST /v1/jobs/*/wakeups":     true,
	"POST /v1/jobs/*/say":         true,
	"POST /v1/jobs/*/end":         true,
	"POST /v1/sessions/*/watches": true,
	// W1: a job's agent may report its work item's progress (an append-only report line plus
	// the descriptive fields; the status is still subject to the human-priority rule).
	"POST /v1/work-items/*/report": true,
	// plan set-todo: leader-only, own plan, ready|skipped — all three need the body and
	// the item's plan, so they are checked in the handler.
	"PATCH /v1/todos/*": true,
	"PATCH /v1/jobs/*":  true,
	// Submit: member-only, and only when the asking job's agent/role opened can_submit.
	"POST /v1/jobs": true,
	// TRK-05: pushing a tracker snapshot. The handler narrows it to the tracker the job
	// itself is associated with (a server-dispatched tracker-sync job).
	"POST /v1/tracker/sync": true,
	// Legacy-id rename (DEPRECATED(v0.126): remove in v0.129): the handler narrows it to
	// the tracker the job is associated with.
	"POST /v1/tracker/repos/*/rename": true,
	// memory flag: the agent that hit a stale scoped memory reports it; the server
	// records the flag under the job's own id. Clearing it (DELETE) stays a person's.
	"POST /v1/memories/*/*/*/flag": true,
}

// jobCallerActions names a refused operation for the 403 body. The message is part of
// the contract (`job credential may not <action> (member job)`): an agent reading its
// own error must be able to tell "this credential is too narrow" from "the server is
// broken".
var jobCallerActions = map[string]string{
	"POST /v1/jobs/*/cancel":                          "cancel a job",
	"POST /v1/jobs/*/accept":                          "accept a delivery",
	"POST /v1/jobs/*/reject":                          "reject a delivery",
	"POST /v1/jobs/*/resume":                          "resume a job",
	"POST /v1/jobs/*/rebuild":                         "rebuild a job",
	"DELETE /v1/jobs/*/worktree":                      "manage a worktree",
	"POST /v1/jobs/*/attach-ticket":                   "attach to a job",
	"POST /v1/ws-ticket":                              "open the browser push channel",
	"POST /v1/jobs/*/interactions":                    "open an interaction",
	"POST /v1/jobs/*/interactions/*/answer":           "answer an interaction",
	"POST /v1/jobs/*/interactions/*/punt":             "punt an interaction",
	"PATCH /v1/workbench/threads/*":                   "change workbench thread preferences",
	"POST /v1/workbench/threads/seen-all":             "mark all workbench threads seen",
	"POST /v1/workbench/threads/*/turn":               "continue a workbench thread",
	"POST /v1/workbench/threads/*/review":             "review a workbench thread",
	"PUT /v1/workbench/layout":                        "change workbench layout",
	"POST /v1/today/actions":                          "record a home-page action",
	"POST /v1/today/snooze":                           "snooze a home-page card",
	"DELETE /v1/today/snooze/*":                       "put back a snoozed card",
	"POST /v1/today/advice":                           "write home-page advice",
	"POST /v1/push/subscriptions":                     "register a push subscription",
	"DELETE /v1/push/subscriptions":                   "remove a push subscription",
	"POST /v1/push/test":                              "send a test push",
	"POST /v1/workflows":                              "submit a workflow",
	"POST /v1/workflows/*/cancel":                     "cancel a workflow",
	"POST /v1/plans":                                  "create a plan",
	"PATCH /v1/plans/*":                               "change a plan",
	"POST /v1/plans/*/jobs":                           "attach a job to a plan",
	"POST /v1/plans/*/todos":                          "add a checklist item",
	"POST /v1/plans/*/run":                            "run a plan",
	"POST /v1/plans/*/pause":                          "pause a plan",
	"POST /v1/plans/*/resume":                         "resume a plan",
	"POST /v1/todos/*/dispatch":                       "dispatch a checklist item",
	"POST /v1/decisions/*/answer":                     "answer a decision",
	"POST /v1/xfer":                                   "create a transfer",
	"POST /v1/xfer/precheck":                          "check a transfer",
	"DELETE /v1/xfer/*":                               "delete a transfer",
	"PUT /v1/xfer/*/content":                          "move transfer bytes",
	"POST /v1/skills/import":                          "import a skill",
	"DELETE /v1/skills/*":                             "delete a skill",
	"POST /v1/skills/*/update":                        "update a skill",
	"PUT /v1/config/agents/*":                         "write agent config",
	"DELETE /v1/config/agents/*":                      "delete agent config",
	"PUT /v1/config/server":                           "write server config",
	"POST /v1/config/validate":                        "validate config",
	"POST /v1/config/reload":                          "reload config",
	"POST /v1/projects":                               "create a project",
	"PUT /v1/projects/*":                              "write project config",
	"DELETE /v1/projects/*":                           "delete a project",
	"POST /v1/agents/*/probe":                         "probe an agent",
	"POST /v1/workers/*/reload":                       "reload a worker",
	"POST /v1/workers":                                "register a worker",
	"DELETE /v1/workers/*":                            "remove a worker",
	"POST /v1/schedules":                              "create a schedule",
	"DELETE /v1/schedules/*":                          "delete a schedule",
	"POST /v1/schedules/*/enable":                     "enable a schedule",
	"POST /v1/schedules/*/disable":                    "disable a schedule",
	"POST /v1/schedules/*/run-now":                    "run a schedule",
	"POST /v1/schedules/*/rotate-token":               "rotate a schedule token",
	"POST /v1/sessions":                               "register a session",
	"DELETE /v1/sessions/*":                           "change a session",
	"POST /v1/sessions/*/heartbeat":                   "heartbeat a session",
	"POST /v1/sessions/*/relay":                       "change a session",
	"POST /v1/sessions/*/turns":                       "open a session turn",
	"POST /v1/sessions/*/turns/*/release":             "release a session turn",
	"POST /v1/sessions/*/say":                         "speak into a session",
	"POST /v1/sessions/*/deliver":                     "deliver into a session",
	"POST /v1/sessions/*/release-takeover":            "release a takeover",
	"POST /v1/sessions/*/nudges":                      "set a session nudge",
	"POST /v1/sessions/*/permissions":                 "open a permission prompt",
	"POST /v1/sessions/*/permissions/resolve":         "resolve a permission prompt",
	"POST /v1/sessions/*/permissions/*/answer":        "answer a permission prompt",
	"PATCH /v1/nudges/*":                              "change a session nudge",
	"DELETE /v1/nudges/*":                             "delete a session nudge",
	"POST /v1/messages":                               "send a message",
	"POST /v1/work-items":                             "create a work item",
	"PATCH /v1/work-items/*":                          "change a work item",
	"DELETE /v1/work-items/*":                         "delete a work item",
	"POST /v1/work-items/delete":                      "delete work items",
	"POST /v1/work-items/*/journal":                   "write a work item note",
	"POST /v1/work-items/*/sessions":                  "attach a session to a work item",
	"DELETE /v1/work-items/*/sessions/*":              "detach a session from a work item",
	"POST /v1/work-items/*/links":                     "link a work item",
	"DELETE /v1/work-items/*/links":                   "unlink a work item",
	"POST /v1/work-items/*/merge":                     "merge work items",
	"POST /v1/work-items/*/split":                     "split a work item",
	"POST /v1/work-items/*/report-request":            "ask a session to report",
	"POST /v1/work-items/digest":                      "send the work digest",
	"POST /v1/work-items/*/summarize":                 "tidy a work item up",
	"POST /v1/work-items/*/suggestions/*/accept":      "adopt a work suggestion",
	"POST /v1/work-items/*/suggestions/*/dismiss":     "dismiss a work suggestion",
	"POST /v1/work-items/merge-suggestions/*/accept":  "accept a merge suggestion",
	"POST /v1/work-items/merge-suggestions/*/dismiss": "dismiss a merge suggestion",
	"POST /v1/memory-suggestions/*/adopt":             "adopt a memory suggestion",
	"POST /v1/memory-suggestions/*/dismiss":           "dismiss a memory suggestion",
	"POST /v1/steward/start":                          "start the steward",
	"POST /v1/steward/stop":                           "stop the steward",
	"POST /v1/steward/restart":                        "restart the steward",
	"POST /v1/steward/ask":                            "ask the steward",
	"POST /v1/steward/review":                         "run a steward review",
	"PUT /v1/config/steward":                          "write steward config",
	"DELETE /v1/retries/*":                            "cancel a retry",
	"POST /v1/agents/register":                        "register an agent",
	"POST /v1/agents/*/deregister":                    "deregister an agent",
	"POST /v1/agents/*/inbox/poll":                    "poll an inbox",
	// TUN-03: the forwarder registry and the preset store are display/configuration
	// surfaces — a job neither listens on a port nor keeps an operator's presets.
	"POST /v1/tunnels/forwarders":        "register a tunnel forwarder",
	"PUT /v1/tunnels/forwarders/*":       "renew a tunnel forwarder",
	"DELETE /v1/tunnels/forwarders/*":    "remove a tunnel forwarder",
	"POST /v1/tunnels/forwarders/*/stop": "stop a tunnel forwarder",
	"POST /v1/tunnels/hosted/*":          "start a hosted tunnel forwarder",
	"DELETE /v1/tunnels/hosted/*":        "stop a hosted tunnel forwarder",
	"POST /v1/tunnels/local-presets/*":   "import a local tunnel preset",
	"PUT /v1/tunnels/presets/*":          "write a tunnel preset",
	"DELETE /v1/tunnels/presets/*":       "delete a tunnel preset",
}

// jobCredentialMiddleware is SEC-01's gate. It runs AFTER authMiddleware (it needs the
// resolved caller kind) and refuses every request a job credential may not make, so no
// handler has to remember to check. A caller that is not a job credential is untouched:
// the user/worker rules are exactly what they were.
func (s *Server) jobCredentialMiddleware(c *rux.Context) {
	if callerKindFromCtx(c) != callerKindJob {
		c.Next()
		return
	}
	key := jobRouteKey(c.Req.Method, c.Req.URL.Path)
	jc, _ := jobCallerFromCtx(c)
	if jc.isSteward() {
		// The steward: default-deny both ways (see the table above).
		if stewardRouteAllowed(key) {
			c.Next()
			return
		}
		writeError(c, http.StatusForbidden,
			"steward credential may not "+jobCallerAction(key),
			"the steward only schedules and tidies work items: it may read the work surface and write notes, reminders, merge suggestions and report requests — never a job, a config change or a final status")
		c.Abort()
		return
	}
	if jobCallerMayRead(c.Req.Method) || jobWriteAllowlist[key] {
		c.Next()
		return
	}
	writeError(c, http.StatusForbidden,
		"job credential may not "+jobCallerAction(key),
		"a "+jc.describe()+" may not perform this operation: its credential is scoped to reading, commenting, asking a human, and (a leader) moving its own plan's checklist")
	c.Abort()
}

// jobCallerAction is the human phrase for a refused route: the table above, else the
// route key itself (an endpoint nobody wrote a phrase for still gets a truthful error).
func jobCallerAction(key string) string {
	if action, ok := jobCallerActions[key]; ok {
		return action
	}
	return strings.ToLower(key)
}

// jobMayWakeJob reports whether the caller may register/manage a wakeup on jobID: a
// job's credential only reaches its OWN job. Attempting it on another job is refused
// with the same shape of message as any other denied operation.
func (s *Server) jobMayWakeJob(c *rux.Context, jobID string) bool {
	jc, ok := jobCallerFromCtx(c)
	if !ok {
		return true // not a job caller: the ordinary rules apply
	}
	if jc.JobID == jobID {
		return true
	}
	writeError(c, http.StatusForbidden, "job credential may not wake another job",
		"a "+jc.describe()+" may only register wakeups on itself (job "+jc.JobID+")")
	return false
}

// jobMayWatchJob keeps a job credential from registering a different job in a
// session. User callers retain the ordinary session-owner checks in the handler.
func (s *Server) jobMayWatchJob(c *rux.Context, jobID string) bool {
	jc, ok := jobCallerFromCtx(c)
	if !ok {
		return true
	}
	if jc.JobID == jobID {
		return true
	}
	writeError(c, http.StatusForbidden, "job credential may not watch another job",
		"a "+jc.describe()+" may only watch itself (job "+jc.JobID+")")
	return false
}

// jobMaySetTodo reports whether the caller may move todoID's status to status. A
// member job may not move checklist items at all; a leader job may move only ITS OWN
// plan's items, and only to `ready` or `skipped` — the two moves the design gives the
// leader round (release a blocked item, or take it out of the plan). Everything else
// (pending/doing/done, a note, an assignee) is a human's or the chain's decision.
func (s *Server) jobMaySetTodo(c *rux.Context, todoID, status string) bool {
	jc, ok := jobCallerFromCtx(c)
	if !ok {
		return true // not a job caller: the ordinary rules apply
	}
	if !jc.isLeader() {
		writeError(c, http.StatusForbidden, "job credential may not move a checklist item",
			"a member job may not move a plan's checklist item: only a leader job, and only within its own plan")
		return false
	}
	if status != jobstore.TodoReady && status != jobstore.TodoSkipped {
		writeError(c, http.StatusForbidden, "job credential may not set this status",
			"a leader job may only set ready|skipped (asked for "+status+")")
		return false
	}
	t, ok, err := s.jobs.Meta().GetTodo(todoID)
	if err != nil || !ok {
		writeError(c, http.StatusNotFound, "unknown todo", "no todo with id "+todoID)
		return false
	}
	if t.PlanID != jc.PlanID || jc.PlanID == "" {
		writeError(c, http.StatusForbidden, "job credential may not move another plan's item",
			"the item belongs to plan "+t.PlanID+", this credential leads "+jc.PlanID)
		return false
	}
	return true
}

// jobMaySubmit reports whether the caller may submit req, and prepares the request
// for it (provenance tag). The rule (design §一.3, decision 3):
//
//   - only a MEMBER job may submit — a leader round decides and delegates through
//     comments, it does not start work itself;
//   - the asking job's agent (or role) must set can_submit — closed by default;
//   - the new job must stay in the asking job's own project;
//   - its agent must be in the asking definition's submit_agents (default [exec]).
//
// On success it stamps `submitted_by_job:<id>` onto the request's tags, so the
// provenance is on the job row rather than only in a log line.
func (s *Server) jobMaySubmit(c *rux.Context, req *job.JobRequest) bool {
	jc, ok := jobCallerFromCtx(c)
	if !ok {
		return true // not a job caller: the ordinary rules apply
	}
	deny := func(action, detail string) bool {
		writeError(c, http.StatusForbidden, "job credential may not "+action, detail)
		return false
	}
	if jc.isLeader() {
		return deny("submit a job", "a leader job decides and delegates through comments; it does not start work itself")
	}
	asking, ok := s.jobs.Get(jc.JobID)
	if !ok {
		return deny("submit a job", "the credential's job "+jc.JobID+" is gone")
	}
	if req.ProjectKey != asking.ProjectKey {
		return deny("submit outside its project",
			"this credential belongs to project "+asking.ProjectKey+", the request targets "+req.ProjectKey)
	}
	canSubmit, submitAgents := s.submitGrant(asking)
	if !canSubmit {
		return deny("submit a job",
			"agent "+asking.Agent+" has not been granted can_submit; an operator must set it on the agent (or its role) first")
	}
	if !slicesContains(submitAgents, req.Agent) {
		return deny("submit agent "+req.Agent,
			"agent "+asking.Agent+" may only submit "+strings.Join(submitAgents, "|")+" jobs")
	}
	req.Tags = append(req.Tags, submittedByJobTag(jc.JobID))
	return true
}

// submitGrant resolves the asking job's submit permission: the job's ROLE and its
// AGENT each carry can_submit/submit_agents, and either opening the gate is enough
// (the role is the natural unit for "this preset is a supervisor", while the agent
// covers a plain `--agent` job). The allowlist is the union of the two so a role that
// names extra agents does not silently drop the agent-level default.
func (s *Server) submitGrant(asking job.JobResult) (bool, []string) {
	cfg := s.projects.Config()
	if cfg == nil {
		return false, nil
	}
	var (
		can    bool
		agents []string
	)
	if ac, ok := cfg.Agents[asking.Agent]; ok {
		can = can || ac.CanSubmit
		if len(ac.SubmitAgents) > 0 {
			agents = append(agents, ac.SubmitAgents...)
		}
	}
	if asking.Role != "" {
		if rc, ok := cfg.Roles[asking.Role]; ok {
			can = can || rc.CanSubmit
			if len(rc.SubmitAgents) > 0 {
				agents = append(agents, rc.SubmitAgents...)
			}
		}
	}
	if len(agents) == 0 {
		agents = config.EffectiveSubmitAgents(nil)
	}
	return can, agents
}

// submittedByJobTag is the provenance tag a job-started job carries: the tag
// vocabulary is a flat `key:value` list, so the submitting job's id rides in the
// value half and `job list --tag submitted_by_job:<id>` finds the work a job started.
func submittedByJobTag(jobID string) string { return "submitted_by_job:" + jobID }

// slicesContains is a tiny membership test used by the submit rule (the allowlists
// here are a handful of agent names).
func slicesContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// stewardReadAllow / stewardWriteAllow are the WHOLE of what the steward's credential may
// call (W2b, design §14.4). A route absent from both is refused before its handler runs.
var stewardReadAllow = map[string]bool{
	"GET /v1/work-items":                   true,
	"GET /v1/work-items/*":                 true,
	"GET /v1/work-items/*/journal":         true,
	"GET /v1/work-items/*/sessions":        true,
	"GET /v1/work-items/*/links":           true,
	"GET /v1/work-items/*/requests":        true,
	"GET /v1/work-items/requests":          true,
	"GET /v1/work-items/merge-suggestions": true,
	"GET /v1/sessions":                     true,
	"GET /v1/sessions/*":                   true,
	"GET /v1/sessions/*/tail":              true,
	"GET /v1/jobs":                         true,
	"GET /v1/jobs/*":                       true,
	"GET /v1/steward":                      true,
	"GET /v1/steward/notes":                true,
	// X2: read-only issues over the server's tracker mirror.
	"GET /v1/issues":   true,
	"GET /v1/issues/*": true,
	// N3 T4: the 「今天」 queue it advises on.
	"GET /v1/today": true,
	// P4: the server-side memory doctor it proposes cleanup from, and its suggestions.
	"GET /v1/memory-findings":    true,
	"GET /v1/memory-suggestions": true,
}

var stewardWriteAllow = map[string]bool{
	// Descriptive / scheduling fields only; the handler refuses a final status and applies
	// the person's-status-wins rule (work.StewardUpdate).
	"PATCH /v1/work-items/*": true,
	// A journal line (always recorded as the steward's own).
	"POST /v1/work-items/*/journal": true,
	// Asks go through the request ledger; a tidy-up is a one-shot read-only job.
	"POST /v1/work-items/*/report-request": true,
	"POST /v1/work-items/*/summarize":      true,
	// Only a recorded suggestion: nothing merges until a person accepts it.
	"POST /v1/work-items/*/merge-suggestions": true,
	// X2 带话: a message for a running session (refused 409 when it is not online);
	// logged on its work item as the steward's own.
	"POST /v1/session-ask": true,
	// Its own long-term notes and the review's point of view.
	"PUT /v1/steward/notes":           true,
	"POST /v1/steward/review-summary": true,
	// N3 T4: advice on a 「今天」 card — only advice; the person clicks 「按建议」.
	"POST /v1/today/advice": true,
	// P4: a memory cleanup PROPOSAL only — adopt / dismiss are a person's.
	"POST /v1/memory-suggestions": true,
}

func stewardRouteAllowed(key string) bool { return stewardReadAllow[key] || stewardWriteAllow[key] }
