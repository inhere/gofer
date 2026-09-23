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
)

// jobCaller is the resolved identity of a job credential, read out of the request
// context by the handlers that need a target-level rule.
type jobCaller struct {
	JobID  string
	Kind   string // jobstore.JobCredentialMember | JobCredentialLeader
	PlanID string
}

// isLeader reports whether the credential is a leader job's, i.e. whether the leader
// half of the permission table applies.
func (jc jobCaller) isLeader() bool { return jc.Kind == jobstore.JobCredentialLeader }

// describe names the credential kind the way the 403 body does.
func (jc jobCaller) describe() string {
	if jc.isLeader() {
		return "leader job"
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
	"v1": true, "jobs": true, "plans": true, "todos": true, "comments": true,
	"wakeups": true, "decisions": true, "workflows": true, "skills": true, "xfer": true,
	"sessions": true, "schedules": true, "retries": true, "config": true, "agents": true,
	"projects": true, "workers": true, "messages": true, "meta": true, "stats": true,
	"runners": true, "tunnels": true, "interactions": true, "accept": true, "reject": true,
	"cancel": true, "resume": true, "rebuild": true, "worktree": true, "run": true,
	"pause": true, "dispatch": true, "answer": true, "punt": true, "content": true,
	"import": true, "update": true, "validate": true, "reload": true, "enable": true,
	"disable": true, "run-now": true, "rotate-token": true, "probe": true, "server": true,
	"attach-ticket": true, "register": true, "deregister": true, "inbox": true, "poll": true,
	"precheck": true, "events": true, "deliveries": true, "artifacts": true, "diff": true,
	"request": true, "stream": true, "logs": true, "stdout": true, "stderr": true,
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
	"POST /v1/plans/*/todos/*/comments": true,
	"POST /v1/todos/*/comments":         true,
	// ask_human: a job that needs a decision raises one (Part C §C3).
	"POST /v1/decisions": true,
	// A wakeup on the job ITSELF: "continue me when this fires". "Own" is enforced in
	// the handler.
	"POST /v1/jobs/*/wakeups": true,
	// plan set-todo: leader-only, own plan, ready|skipped — all three need the body and
	// the item's plan, so they are checked in the handler.
	"PATCH /v1/todos/*": true,
	// Submit: member-only, and only when the asking job's agent/role opened can_submit.
	"POST /v1/jobs": true,
}

// jobCallerActions names a refused operation for the 403 body. The message is part of
// the contract (`job credential may not <action> (member job)`): an agent reading its
// own error must be able to tell "this credential is too narrow" from "the server is
// broken".
var jobCallerActions = map[string]string{
	"POST /v1/jobs/*/cancel":                "cancel a job",
	"POST /v1/jobs/*/accept":                "accept a delivery",
	"POST /v1/jobs/*/reject":                "reject a delivery",
	"POST /v1/jobs/*/resume":                "resume a job",
	"POST /v1/jobs/*/rebuild":               "rebuild a job",
	"DELETE /v1/jobs/*/worktree":            "manage a worktree",
	"POST /v1/jobs/*/attach-ticket":         "attach to a job",
	"POST /v1/jobs/*/interactions":          "open an interaction",
	"POST /v1/jobs/*/interactions/*/answer": "answer an interaction",
	"POST /v1/jobs/*/interactions/*/punt":   "punt an interaction",
	"POST /v1/workflows":                    "submit a workflow",
	"POST /v1/workflows/*/cancel":           "cancel a workflow",
	"POST /v1/plans":                        "create a plan",
	"PATCH /v1/plans/*":                     "change a plan",
	"POST /v1/plans/*/jobs":                 "attach a job to a plan",
	"POST /v1/plans/*/todos":                "add a checklist item",
	"POST /v1/plans/*/run":                  "run a plan",
	"POST /v1/plans/*/pause":                "pause a plan",
	"POST /v1/plans/*/resume":               "resume a plan",
	"POST /v1/todos/*/dispatch":             "dispatch a checklist item",
	"POST /v1/decisions/*/answer":           "answer a decision",
	"POST /v1/xfer":                         "create a transfer",
	"POST /v1/xfer/precheck":                "check a transfer",
	"DELETE /v1/xfer/*":                     "delete a transfer",
	"PUT /v1/xfer/*/content":                "move transfer bytes",
	"POST /v1/skills/import":                "import a skill",
	"DELETE /v1/skills/*":                   "delete a skill",
	"POST /v1/skills/*/update":              "update a skill",
	"PUT /v1/config/agents/*":               "write agent config",
	"DELETE /v1/config/agents/*":            "delete agent config",
	"PUT /v1/config/server":                 "write server config",
	"POST /v1/config/validate":              "validate config",
	"POST /v1/config/reload":                "reload config",
	"POST /v1/projects":                     "create a project",
	"PUT /v1/projects/*":                    "write project config",
	"DELETE /v1/projects/*":                 "delete a project",
	"POST /v1/agents/*/probe":               "probe an agent",
	"POST /v1/workers/*/reload":             "reload a worker",
	"POST /v1/schedules":                    "create a schedule",
	"DELETE /v1/schedules/*":                "delete a schedule",
	"POST /v1/schedules/*/enable":           "enable a schedule",
	"POST /v1/schedules/*/disable":          "disable a schedule",
	"POST /v1/schedules/*/run-now":          "run a schedule",
	"POST /v1/schedules/*/rotate-token":     "rotate a schedule token",
	"POST /v1/sessions":                     "register a session",
	"DELETE /v1/sessions/*":                 "change a session",
	"POST /v1/sessions/*/heartbeat":         "heartbeat a session",
	"POST /v1/sessions/*/relay":             "change a session",
	"POST /v1/sessions/*/turns":             "open a session turn",
	"POST /v1/sessions/*/turns/*/release":   "release a session turn",
	"POST /v1/sessions/*/say":               "speak into a session",
	"POST /v1/sessions/*/deliver":           "deliver into a session",
	"POST /v1/sessions/*/release-takeover":  "release a takeover",
	"POST /v1/messages":                     "send a message",
	"DELETE /v1/retries/*":                  "cancel a retry",
	"POST /v1/agents/register":              "register an agent",
	"POST /v1/agents/*/deregister":          "deregister an agent",
	"POST /v1/agents/*/inbox/poll":          "poll an inbox",
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
	if jobCallerMayRead(c.Req.Method) || jobWriteAllowlist[key] {
		c.Next()
		return
	}
	jc, _ := jobCallerFromCtx(c)
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
