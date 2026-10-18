// Caller-scope ceilings at the HTTP layer (design
// docs/design/2026-10-18-caller-scope-ceilings-design.md「判定（httpapi）」, plan task S4b).
//
// A ceiling is a subtractive layer on top of the permission model: a user-class caller that
// has one reaches what its permissions allow AND its ceiling admits, never more. This file is
// the one place the HTTP layer asks that question:
//
//   - canFor is the three-state answer for "may this caller act on a resource of project P
//     with permissions X": allow / 403 (in scope but lacking the permission) / 404 (P is
//     outside the ceiling, which must look exactly like "no such resource").
//   - resourceKinds holds ONE resolver per Resource(kind) of the route declarations: the
//     request's id for that kind, and the project(s) the record belongs to. The route gate
//     (authzGate) runs it before the handler for every route whose id is in the path / query.
//   - requireProjectScope / requireResourceScope are what a handler calls for the ids that
//     only exist after the body is bound (BodyProject, and the few Resource routes whose id
//     is in the body).
//   - scopeFilter narrows a list to the ceiling's projects (Filter routes).
//
// A caller WITHOUT a ceiling (every caller by default) is never touched by anything here:
// ceilingOf answers nil and every function degrades to the pre-ceiling behavior. A worker
// token and a job credential are not user-class callers and are confined by their own route
// tables (job credentials are narrowed by task S5, not here).

package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
	"github.com/inhere/gofer/internal/workbench"
)

// ctxCeilingChecked is the rux context key a successful scope check leaves behind, so a test
// (and a reader of the code) can see that a handler-side check really ran.
const ctxCeilingChecked = "ceiling_scope_checked"

// scopeVerdict is the three-state answer of canFor.
type scopeVerdict uint8

const (
	// scopeAllow: in scope, and the caller holds the permissions.
	scopeAllow scopeVerdict = iota
	// scopeForbidden: in scope, but a permission is missing (403, the existing semantics).
	scopeForbidden
	// scopeNotFound: outside the ceiling; answered as "not found" (404), writes included.
	scopeNotFound
)

// ctxJobCeiling caches, per request, the ceiling a JOB credential inherits.
const ctxJobCeiling = "job_credential_ceiling"

// jobCeilingState is the cached answer of jobCredentialCeiling.
type jobCeilingState struct {
	ceil *config.CallerCeiling
	err  error
}

// denyAllCeiling is what ceilingOf answers for a job credential whose ceiling owner cannot
// be resolved (fail closed): it matches no project, no worker, and is read-only, so every
// scope check, filter and write refuses. The route gate refuses such a request outright
// (403) before any handler runs.
var denyAllCeiling = &config.CallerCeiling{
	Projects: []string{"\x00none"}, Workers: []string{"\x00none"}, ReadOnly: boolptrCeil(true),
}

func boolptrCeil(b bool) *bool { return &b }

// jobCredentialCeiling resolves the ceiling of a request authenticated with a JOB
// credential: the ceiling of the job's ceiling owner (job.Service.CredentialCeilingOwner,
// fail closed). An owner without a ceiling, or an empty owner, leaves the credential
// unrestricted (nil, nil). The answer is cached on the request context.
func (s *Server) jobCredentialCeiling(c *rux.Context) (*config.CallerCeiling, error) {
	if v, ok := c.Get(ctxJobCeiling); ok {
		if st, ok := v.(jobCeilingState); ok {
			return st.ceil, st.err
		}
	}
	var st jobCeilingState
	// A deployment with no restricted caller cannot have a restricted owner: skip the lookup
	// (and with it the fail-closed refusal, which guards against hiding a ceiling).
	if s.jobs != nil && len(s.serverCfg().CeilingCallers()) > 0 {
		owner, err := s.jobs.CredentialCeilingOwner(callerFromCtx(c))
		switch {
		case err != nil:
			st.err = err
		case owner != "":
			if ceil, ok := s.serverCfg().CallerCeiling(owner); ok {
				st.ceil = ceil
			}
		}
	}
	c.Set(ctxJobCeiling, st)
	return st.ceil, st.err
}

// ceilingOf returns the ceiling that governs the caller of this request, or nil when none
// does. A user-class caller has the ceiling of its own configuration entry; a job credential
// has the ceiling of its job's ceiling owner (an unresolvable owner is the deny-all
// ceiling); a worker token never has one.
func (s *Server) ceilingOf(c *rux.Context) *config.CallerCeiling {
	switch callerKindFromCtx(c) {
	case callerKindUser:
		id := callerFromCtx(c)
		if id == "" {
			return nil
		}
		ceil, ok := s.serverCfg().CallerCeiling(id)
		if !ok {
			return nil
		}
		return ceil
	case callerKindJob:
		ceil, err := s.jobCredentialCeiling(c)
		if err != nil {
			return denyAllCeiling
		}
		return ceil
	}
	return nil
}

// ceilingAllowsProject is AllowsProject with the one rule the HTTP layer adds: a record
// with NO project is outside a ceiling that restricts projects (fail closed). A ceiling that
// does not restrict projects admits everything, the empty project included.
func ceilingAllowsProject(ceil *config.CallerCeiling, project string) bool {
	if ceil == nil || len(ceil.Projects) == 0 {
		return true
	}
	if strings.TrimSpace(project) == "" {
		return false
	}
	return ceil.AllowsProject(project)
}

// ceilingAllowsAll reports whether every one of projects is inside the ceiling. A record
// that names no project at all is outside (see ceilingAllowsProject).
func ceilingAllowsAll(ceil *config.CallerCeiling, projects []string) bool {
	if ceil == nil || len(ceil.Projects) == 0 {
		return true
	}
	if len(projects) == 0 {
		return false
	}
	for _, p := range projects {
		if !ceilingAllowsProject(ceil, p) {
			return false
		}
	}
	return true
}

// canFor is the three-state decision for a resource of `project`: not in the caller's
// ceiling -> scopeNotFound (whatever the permissions), otherwise the plain permission check
// (scopeAllow / scopeForbidden). A caller without a ceiling gets exactly s.can.
func (s *Server) canFor(c *rux.Context, project string, perms ...string) scopeVerdict {
	if ceil := s.ceilingOf(c); ceil != nil && !ceilingAllowsProject(ceil, project) {
		return scopeNotFound
	}
	if s.can(c, perms...) {
		return scopeAllow
	}
	return scopeForbidden
}

// writeOutOfScope answers a request that names something outside the ceiling. It is the same
// body shape a missing resource gets (`{"error":"not found", ...}`), and deliberately says
// nothing about the ceiling.
func writeOutOfScope(c *rux.Context, kind, id string) {
	detail := kind + " not found"
	if id != "" {
		detail = kind + " " + id + " not found"
	}
	writeError(c, http.StatusNotFound, "not found", detail)
}

// requireProjectScope is what a BodyProject handler calls after binding: it answers 404 and
// returns false when `project` is outside the caller's ceiling (no project counts as
// outside). A caller without a ceiling always passes.
func (s *Server) requireProjectScope(c *rux.Context, project string) bool {
	c.Set(ctxCeilingChecked, true)
	ceil := s.ceilingOf(c)
	if ceilingAllowsProject(ceil, project) {
		return true
	}
	writeOutOfScope(c, "project", project)
	return false
}

// requireResourceScope is the handler-side form of the gate's Resource check, for a route
// whose id only exists in the body (or is not unique in the path): it resolves the record of
// `kind` and answers 404 when it is missing or outside the ceiling.
func (s *Server) requireResourceScope(c *rux.Context, kind, id string) bool {
	c.Set(ctxCeilingChecked, true)
	ceil := s.ceilingOf(c)
	if ceil == nil || (len(ceil.Projects) == 0 && kind != "worker") {
		// No ceiling, or one that does not restrict projects: nothing to judge.
		return true
	}
	return s.resourceInScope(c, ceil, kind, id)
}

// resourceInScope resolves (kind, id) and writes the 404 / 500 itself when it cannot say yes.
func (s *Server) resourceInScope(c *rux.Context, ceil *config.CallerCeiling, kind, id string) bool {
	rk, ok := resourceKinds[kind]
	if !ok {
		// An undeclared kind is a programming error; failing closed beats guessing.
		writeOutOfScope(c, kind, id)
		return false
	}
	in, found, err := rk.scope(s, ceil, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "scope check failed", err.Error())
		return false
	}
	if !found || !in {
		writeOutOfScope(c, kind, id)
		return false
	}
	return true
}

// scopeFilter returns the predicate a Filter route applies to the project of every row it
// would return: always-true for a caller without a ceiling.
func (s *Server) scopeFilter(c *rux.Context) func(project string) bool {
	ceil := s.ceilingOf(c)
	if ceil == nil || len(ceil.Projects) == 0 {
		return func(string) bool { return true }
	}
	return func(project string) bool { return ceilingAllowsProject(ceil, project) }
}

// ceilingActive reports whether the caller has a ceiling that restricts projects, i.e.
// whether a Filter route has anything to filter.
func (s *Server) ceilingActive(c *rux.Context) bool {
	ceil := s.ceilingOf(c)
	return ceil != nil && len(ceil.Projects) > 0
}

// resourceKind is the single resolver of one Resource(kind): where the request carries the
// id, and what project(s) the record has.
type resourceKind struct {
	// id extracts the record id from the request's path / query. "" means the route does not
	// carry it there (it is in the body): the handler calls requireResourceScope instead.
	id func(c *rux.Context) string
	// scope answers: is the record inside the ceiling (in), does it exist (found), and a
	// store error. found=false is reported as 404 exactly like out-of-scope.
	scope func(s *Server, ceil *config.CallerCeiling, id string) (in, found bool, err error)
}

func paramID(name string) func(c *rux.Context) string {
	return func(c *rux.Context) string { return strings.TrimSpace(c.Param(name)) }
}

// projectScope adapts a "project(s) of the record" lookup to the scope signature.
func projectScope(get func(s *Server, id string) (projects []string, found bool, err error)) func(*Server, *config.CallerCeiling, string) (bool, bool, error) {
	return func(s *Server, ceil *config.CallerCeiling, id string) (bool, bool, error) {
		projects, found, err := get(s, id)
		if err != nil || !found {
			return false, found, err
		}
		return ceilingAllowsAll(ceil, projects), true, nil
	}
}

// jobProjects: the project of a job (live or stored).
func jobProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || id == "" {
		return nil, false, nil
	}
	res, ok := s.jobs.Get(id)
	if !ok {
		return nil, false, nil
	}
	return []string{res.ProjectKey}, true, nil
}

func planProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	p, ok, err := s.jobs.Meta().GetPlan(id)
	if err != nil || !ok {
		return nil, false, err
	}
	return []string{p.ProjectKey}, true, nil
}

func todoProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	t, ok, err := s.jobs.Meta().GetTodo(id)
	if err != nil || !ok {
		return nil, false, err
	}
	if t.ProjectKey != "" {
		return []string{t.ProjectKey}, true, nil
	}
	return planProjects(s, t.PlanID)
}

func scheduleProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	rec, ok, err := s.jobs.Meta().GetSchedule(id)
	if err != nil || !ok {
		return nil, false, err
	}
	return []string{rec.ProjectKey}, true, nil
}

// workflowProjects: a workflow has no project of its own; its steps do. Every step's
// project must be inside the ceiling, and a workflow whose steps name none is outside.
func workflowProjects(s *Server, id string) ([]string, bool, error) {
	if s.workflow == nil || id == "" {
		return nil, false, nil
	}
	wf, ok, err := s.workflow.GetWorkflow(id)
	if err != nil || !ok {
		return nil, false, err
	}
	var spec workflow.Spec
	if json.Unmarshal([]byte(wf.SpecJSON), &spec) != nil {
		// An unreadable spec cannot be shown to be inside: outside.
		return nil, true, nil
	}
	var projects []string
	for _, st := range spec.Steps {
		projects = append(projects, st.ProjectKey)
	}
	return projects, true, nil
}

// sessionProjects: a terminal session (relay) or the agent session of jobs (an ACP / pty
// session job). The id is looked up as a terminal session first, then as the session id of
// jobs; every job of it must be inside.
func sessionProjects(s *Server, id string) ([]string, bool, error) {
	if id == "" {
		return nil, false, nil
	}
	if s.relay != nil {
		if a, err := s.relay.Session(id); err == nil {
			return []string{a.ProjectKey}, true, nil
		}
	}
	return sessionJobProjects(s, id)
}

func sessionJobProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	recs, err := s.jobs.Meta().ListJobs(jobstore.ListQuery{Session: id, Limit: 1000})
	if err != nil || len(recs) == 0 {
		return nil, false, err
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ProjectKey)
	}
	return out, true, nil
}

func workItemProjects(s *Server, id string) ([]string, bool, error) {
	if s.work == nil || s.work.Store() == nil || id == "" {
		return nil, false, nil
	}
	w, ok, err := s.work.Store().GetWorkItem(id)
	if err != nil || !ok {
		return nil, false, err
	}
	return []string{w.ProjectKey}, true, nil
}

// trackerRepoScope: a tracker repo belongs to a project; the reserved global memory tracker
// belongs to none and is inside only for a ceiling that allows global memory.
func trackerRepoScope(s *Server, ceil *config.CallerCeiling, id string) (bool, bool, error) {
	if id == "" {
		return false, false, nil
	}
	if jobstore.IsReservedTracker(id) {
		return ceil.GlobalMemoryAllowed() || len(ceil.Projects) == 0, true, nil
	}
	if s.trackerStore == nil {
		return false, false, nil
	}
	repos, err := s.trackerStore.ListTrackerRepos()
	if err != nil {
		return false, false, err
	}
	for _, r := range repos {
		if r.TrackerID == id || r.TrackerID == tracker.ShortTrackerID(id) {
			return ceilingAllowsProject(ceil, r.ProjectKey), true, nil
		}
	}
	return false, false, nil
}

// memoryID packs the (scope, scope_key) of a memory route into one id for the resolver.
func memoryID(c *rux.Context) string {
	scope := strings.TrimSpace(c.Param("scope"))
	if scope == "" {
		return ""
	}
	return scope + "/" + scopeKeyParam(c)
}

// memoryScope: a project-scoped memory is inside when its project is; a memory of any other
// scope (global) is inside only for a ceiling that allows global memory.
func memoryScope(_ *Server, ceil *config.CallerCeiling, id string) (bool, bool, error) {
	scope, key, _ := strings.Cut(id, "/")
	switch scope {
	case jobstore.ScopedMemoryProject:
		return ceilingAllowsProject(ceil, key), true, nil
	case jobstore.ScopedMemoryGlobal:
		return ceil.GlobalMemoryAllowed() || len(ceil.Projects) == 0, true, nil
	}
	return false, false, nil
}

func xferProjects(s *Server, id string) ([]string, bool, error) {
	if s.xfer == nil || s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	rec, ok, err := s.jobs.Meta().GetXfer(id)
	if err != nil || !ok {
		return nil, false, err
	}
	return []string{rec.ProjectKey}, true, nil
}

func wakeupProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	rec, ok, err := s.jobs.Meta().GetWakeup(id)
	if err != nil || !ok {
		return nil, false, err
	}
	return jobProjects(s, rec.JobID)
}

func retryProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	rec, ok, err := s.jobs.Meta().GetRetry(id)
	if err != nil || !ok {
		return nil, false, err
	}
	return jobProjects(s, rec.SourceJobID)
}

// decisionProjects: a decision belongs to a plan, to a terminal session, or to both; every
// one it names must be inside, and one that names neither is outside.
func decisionProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	d, ok, err := s.jobs.Meta().GetDecision(id)
	if err != nil || !ok {
		return nil, false, err
	}
	var out []string
	if d.PlanID != "" {
		ps, found, perr := planProjects(s, d.PlanID)
		if perr != nil {
			return nil, false, perr
		}
		if !found {
			return nil, true, nil
		}
		out = append(out, ps...)
	}
	if d.SessionID != "" {
		ps, found, perr := sessionProjects(s, d.SessionID)
		if perr != nil {
			return nil, false, perr
		}
		if !found {
			return nil, true, nil
		}
		out = append(out, ps...)
	}
	return out, true, nil
}

func nudgeProjects(s *Server, id string) ([]string, bool, error) {
	if s.jobs == nil || s.jobs.Meta() == nil || id == "" {
		return nil, false, nil
	}
	n, ok, err := s.jobs.Meta().GetSessionNudge(id)
	if err != nil || !ok {
		return nil, false, err
	}
	return sessionProjects(s, n.SessionID)
}

// workbenchThreadProjects: a thread id is `j:<job>`, `r:<relay session>` or `s:<agent
// session>` (workbench.ParseThreadID); the project is the one of what it names.
func workbenchThreadProjects(s *Server, id string) ([]string, bool, error) {
	kind, raw, err := workbench.ParseThreadID(id)
	if err != nil {
		return nil, false, nil
	}
	switch kind {
	case workbench.KindJob:
		return jobProjects(s, raw)
	case workbench.KindRelay:
		if s.relay == nil {
			return nil, false, nil
		}
		a, serr := s.relay.Session(raw)
		if serr != nil {
			return nil, false, nil
		}
		return []string{a.ProjectKey}, true, nil
	case workbench.KindAgent:
		return sessionJobProjects(s, raw)
	}
	return nil, false, nil
}

func intParam(name string) func(c *rux.Context) string {
	return func(c *rux.Context) string {
		v := strings.TrimSpace(c.Param(name))
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return ""
		}
		return v
	}
}

func parseInt64(id string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	return n, err == nil
}

func mergeSuggestionProjects(s *Server, id string) ([]string, bool, error) {
	n, ok := parseInt64(id)
	if !ok || s.work == nil || s.work.Store() == nil {
		return nil, false, nil
	}
	sug, err := s.work.Store().GetWorkMergeSuggestion(n)
	if err != nil {
		return nil, false, nil
	}
	var out []string
	for _, wid := range []string{sug.TargetID, sug.SourceID} {
		ps, found, perr := workItemProjects(s, wid)
		if perr != nil {
			return nil, false, perr
		}
		if !found {
			return nil, true, nil
		}
		out = append(out, ps...)
	}
	return out, true, nil
}

func memoryCandidateProjects(s *Server, id string) ([]string, bool, error) {
	n, ok := parseInt64(id)
	if !ok || s.trackerStore == nil {
		return nil, false, nil
	}
	cand, err := s.trackerStore.GetMemoryCandidate(n)
	if err != nil {
		return nil, false, nil
	}
	return []string{cand.ProjectKey}, true, nil
}

func memorySuggestionScope(s *Server, ceil *config.CallerCeiling, id string) (bool, bool, error) {
	n, ok := parseInt64(id)
	if !ok || s.trackerStore == nil {
		return false, false, nil
	}
	sug, err := s.trackerStore.GetMemorySuggestion(n)
	if err != nil {
		return false, false, nil
	}
	return trackerRepoScope(s, ceil, sug.TrackerID)
}

// workerScope: a worker is infrastructure, not a project record; the ceiling's `workers`
// list decides what a restricted caller may see of it.
func workerScope(_ *Server, ceil *config.CallerCeiling, id string) (bool, bool, error) {
	if id == "" {
		return false, false, nil
	}
	return ceil.AllowsWorker(id), true, nil
}

// alwaysOutside is the resolver of kinds that cannot be attributed to a project (the today
// cards aggregate every project): a restricted caller never reaches them.
func alwaysOutside(*Server, *config.CallerCeiling, string) (bool, bool, error) {
	return false, true, nil
}

// resourceKinds is the resolver table: ONE entry per Resource(kind) used by routeDecls
// (TestEveryResourceKindHasResolver enforces it in both directions).
var resourceKinds = map[string]resourceKind{
	"job":       {id: paramID("id"), scope: projectScope(jobProjects)},
	"plan":      {id: paramID("id"), scope: projectScope(planProjects)},
	"todo":      {id: paramID("todo_id"), scope: projectScope(todoProjects)},
	"schedule":  {id: paramID("id"), scope: projectScope(scheduleProjects)},
	"workflow":  {id: paramID("id"), scope: projectScope(workflowProjects)},
	"session":   {id: paramID("sid"), scope: projectScope(sessionProjects)},
	"work_item": {id: paramID("id"), scope: projectScope(workItemProjects)},
	"tracker_repo": {
		id: func(c *rux.Context) string {
			if v := strings.TrimSpace(c.Param("tracker_id")); v != "" {
				return v
			}
			return strings.TrimSpace(c.Query("tracker_id"))
		},
		scope: trackerRepoScope,
	},
	"memory":            {id: memoryID, scope: memoryScope},
	"mail":              {id: intParam("id"), scope: mailScope},
	"worker":            {id: paramID("id"), scope: workerScope},
	"xfer":              {id: paramID("id"), scope: projectScope(xferProjects)},
	"wakeup":            {id: paramID("wid"), scope: projectScope(wakeupProjects)},
	"retry":             {id: paramID("rid"), scope: projectScope(retryProjects)},
	"decision":          {id: paramID("id"), scope: projectScope(decisionProjects)},
	"nudge":             {id: paramID("id"), scope: projectScope(nudgeProjects)},
	"workbench_thread":  {id: paramID("id"), scope: projectScope(workbenchThreadProjects)},
	"merge_suggestion":  {id: intParam("n"), scope: projectScope(mergeSuggestionProjects)},
	"memory_candidate":  {id: intParam("n"), scope: projectScope(memoryCandidateProjects)},
	"memory_suggestion": {id: intParam("n"), scope: memorySuggestionScope},
	// today_card: the cards are an aggregate over every project and carry no project of
	// their own; the id is always "-" so the gate answers 404 for a restricted caller.
	"today_card": {id: func(*rux.Context) string { return "-" }, scope: alwaysOutside},
	// issue: the route addresses an issue id that can exist in several repos; the handler
	// narrows the matching rows to the repos inside the ceiling (handleIssueGet).
	"issue": {id: func(*rux.Context) string { return "" }, scope: alwaysOutside},
}

// ceilingGate is the ceiling half of authzGate for a user-class caller that HAS a ceiling.
// It reports whether it answered the request (the gate then aborts). Order, on purpose:
//
//  1. read-only: a write route is refused with 403 before any resource is looked up;
//  2. project scope per the route's declaration: PathProject and the path-carried Resource
//     are judged here (404 when outside); BodyProject, the body-carried Resource ids and
//     Filter lists are judged by the handler; a NoProject write is refused (403) — a
//     restricted caller does not change server-global state, unless the route is ReadSafe.
func (s *Server) ceilingGate(c *rux.Context, ceil *config.CallerCeiling, decl routeDecl) bool {
	write := decl.effectiveRW(decl.key) == rwWrite
	if write && ceil.IsReadOnly() {
		writeError(c, http.StatusForbidden, "caller ceiling",
			"this caller is read-only: state-changing requests are refused")
		return true
	}
	switch decl.scope.kind {
	case scopeNoProject:
		if callerKindFromCtx(c) == callerKindJob && decl.user.kind == userRequire {
			// The permission model does not govern a job credential (can() is true for it),
			// so a permission-gated server-global read (configuration, MCP servers, grants)
			// would be open to the agent of a restricted job. It is not.
			writeError(c, http.StatusForbidden, "caller ceiling",
				"a job of a caller with a scope ceiling may not use server-global routes ("+decl.key+")")
			return true
		}
		if write {
			writeError(c, http.StatusForbidden, "caller ceiling",
				"a caller with a scope ceiling may not change server-global state ("+decl.key+")")
			return true
		}
	case scopePathProject:
		if p := c.Param(decl.scope.arg); !ceilingAllowsProject(ceil, p) {
			writeOutOfScope(c, "project", p)
			return true
		}
	case scopeResource:
		if write && decl.scope.arg == "worker" {
			// The workers list of a ceiling says where the caller's jobs may run, not that
			// the caller manages workers.
			writeError(c, http.StatusForbidden, "caller ceiling",
				"a caller with a scope ceiling may not manage workers ("+decl.key+")")
			return true
		}
		if len(ceil.Projects) == 0 && decl.scope.arg != "worker" {
			// Projects are not restricted: nothing to resolve (a missing record is the
			// handler's own 404).
			return false
		}
		rk, ok := resourceKinds[decl.scope.arg]
		if !ok {
			writeOutOfScope(c, decl.scope.arg, "")
			return true
		}
		id := rk.id(c)
		if id == "" {
			// The id is not in the path / query: the handler judges it after binding.
			return false
		}
		if !s.resourceInScope(c, ceil, decl.scope.arg, id) {
			return true
		}
	}
	return false
}

// requireScopedMemoryScope is requireResourceScope for a memory named by (scope, scope_key)
// in a request body. A scope the store would reject anyway is left to the store's 400.
func (s *Server) requireScopedMemoryScope(c *rux.Context, scope, scopeKey string) bool {
	norm, key, err := jobstore.NormalizeScopedMemoryScope(scope, scopeKey)
	if err != nil {
		c.Set(ctxCeilingChecked, true)
		if s.ceilingOf(c) == nil {
			return true
		}
		writeOutOfScope(c, "memory", "")
		return false
	}
	return s.requireResourceScope(c, "memory", norm+"/"+key)
}

// requireResourceScopes is requireResourceScope for a list of ids of one kind (blank ids
// are skipped; the store rejects them on its own).
func (s *Server) requireResourceScopes(c *rux.Context, kind string, ids []string) bool {
	c.Set(ctxCeilingChecked, true)
	if s.ceilingOf(c) == nil {
		return true
	}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if !s.requireResourceScope(c, kind, id) {
			return false
		}
	}
	return true
}

// requireTrackerSyncScope is the scope check of POST /v1/tracker/sync, whose body can both
// name a tracker and (re)assign its project. A restricted caller may sync a tracker that is
// inside its ceiling, or register a new one under a project that is; it may not point a
// tracker at a project outside.
func (s *Server) requireTrackerSyncScope(c *rux.Context, trackerID, projectKey string) bool {
	c.Set(ctxCeilingChecked, true)
	ceil := s.ceilingOf(c)
	if ceil == nil || len(ceil.Projects) == 0 {
		return true
	}
	if projectKey != "" && !jobstore.IsReservedTracker(trackerID) && !ceilingAllowsProject(ceil, projectKey) {
		writeOutOfScope(c, "project", projectKey)
		return false
	}
	in, found, err := trackerRepoScope(s, ceil, trackerID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "scope check failed", err.Error())
		return false
	}
	if !found && projectKey != "" && ceilingAllowsProject(ceil, projectKey) {
		return true // a new tracker registered under a project of the caller's own
	}
	if !found || !in {
		writeOutOfScope(c, "tracker_repo", trackerID)
		return false
	}
	return true
}

// scopeRows narrows the rows of a Filter route to the caller's ceiling: a row whose project
// is outside (or empty) is dropped. A caller without a project-restricting ceiling gets the
// slice back untouched (same backing array, no copy).
func scopeRows[T any](s *Server, c *rux.Context, rows []T, project func(T) string) []T {
	if !s.ceilingActive(c) {
		return rows
	}
	in := s.scopeFilter(c)
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		if in(project(r)) {
			out = append(out, r)
		}
	}
	return out
}

// scopeRowsByKind is scopeRows for rows that only carry the id of a record of `kind`: the
// project is resolved through the kind's resolver, memoized per id for the request.
func scopeRowsByKind[T any](s *Server, c *rux.Context, kind string, rows []T, id func(T) string) []T {
	ceil := s.ceilingOf(c)
	if ceil == nil || len(ceil.Projects) == 0 {
		return rows
	}
	rk, ok := resourceKinds[kind]
	if !ok {
		return nil
	}
	memo := map[string]bool{}
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		key := id(r)
		in, seen := memo[key]
		if !seen {
			v, found, err := rk.scope(s, ceil, key)
			in = err == nil && found && v
			memo[key] = in
		}
		if in {
			out = append(out, r)
		}
	}
	return out
}

// scopeMemories narrows a scoped-memory listing: a project memory stays when its project is
// inside the ceiling, a global memory only for a ceiling that allows global memory.
func (s *Server) scopeMemories(c *rux.Context, items []jobstore.ScopedMemory) []jobstore.ScopedMemory {
	ceil := s.ceilingOf(c)
	if ceil == nil || len(ceil.Projects) == 0 {
		return items
	}
	out := make([]jobstore.ScopedMemory, 0, len(items))
	for _, m := range items {
		if in, found, _ := memoryScope(s, ceil, m.Scope+"/"+m.ScopeKey); found && in {
			out = append(out, m)
		}
	}
	return out
}

// scopeWorkbench narrows a workbench listing to the project groups (and attention items)
// inside the ceiling.
func (s *Server) scopeWorkbench(c *rux.Context, res workbench.Response) workbench.Response {
	if !s.ceilingActive(c) {
		return res
	}
	in := s.scopeFilter(c)
	out := workbench.Response{Since: res.Since, Projects: make([]workbench.ProjectGroup, 0, len(res.Projects))}
	for _, g := range res.Projects {
		if !in(g.ProjectKey) {
			continue
		}
		out.Total += len(g.Threads)
		out.Projects = append(out.Projects, g)
	}
	for _, it := range res.Attention {
		if in(it.ProjectKey) {
			out.Attention = append(out.Attention, it)
		}
	}
	return out
}

// singleCeilingProject returns the one literal project the caller's ceiling is limited to,
// or "" (no ceiling, several projects, or a glob).
func (s *Server) singleCeilingProject(c *rux.Context) string {
	ceil := s.ceilingOf(c)
	if ceil == nil || len(ceil.Projects) != 1 || strings.ContainsAny(ceil.Projects[0], `*?[\`) {
		return ""
	}
	return ceil.Projects[0]
}
