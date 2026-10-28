// 路由声明表（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md
// 「架构」的「路由声明」与「测试兜底」两节，计划 T1.4）。
//
// Every route of buildRouter carries a declaration: which category of caller may reach
// it, and — for a user-class caller — whether a permission is needed. The declaration is
// attached PER ROUTE (wrapRoutes) rather than by a group middleware, because a group
// middleware cannot see which route was matched (rux publishes the matched route only
// around the handler chain).
//
// The seven categories of the design and how they are written here:
//
//	Require(perm…)         userCategory userRequire  — the gate refuses a caller that does
//	                                                   not hold every permission (403)
//	InHandler(perm…, mode) userCategory userInHandler — the HANDLER decides; the gate
//	                                                   passes through. `mode` records what
//	                                                   the handler answers a caller without
//	                                                   the permission that acts on someone
//	                                                   else's resource: deny | filter
//	Public                 userCategory userPublic   — any user-class caller
//	JobCred(key)           routeDecl.jobCred         — annotation (SEC-01 key)
//	WorkerSelf             routeDecl.workerSelf      — annotation (worker whitelist)
//	External(credential)   routeDecl.external        — the route authenticates itself
//	Static                 routeDecl.static          — served from the static console
//
// Two states lie outside the seven: NoUserSurface (the route has no user-class surface at
// all — it is reached with the caller's own credential, or only by a worker) and
// UserPending (待迁移: the user side is still the hand-written check inside the handler, so
// the gate adds nothing and the route also sits on the migration list below).
//
// JobCred and WorkerSelf are ANNOTATIONS (design: 「JobCred(key) 与 WorkerSelf 只是标注，
// SEC-01 与 worker 白名单仍是权威」): jobcredential.go's SEC-01 tables and
// workercredential.go's whitelist remain the authority on what a job credential and a
// worker token may reach, and neither table changes here. They are written down so the
// generated reference (T1.6) and `gofer auth routes` (T2.1) can show the whole picture,
// and so that the two halves cannot drift: the snapshot tests of authz_routes_test.go
// compare the keys referenced here with the tables, item by item.
//
// The user side of a route is declared exactly once: while a domain is still being
// migrated it says UserPending, and the route is on pendingRoutes; the domain task
// (T1.5a–i) writes the real category, moves the handler check onto s.can and deletes the
// route key from pendingRoutes.

package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
)

// permMode is InHandler's second argument (design: `InHandler(perm…, deny|filter)`: the
// result depends on resource ownership). It is part of the declaration's documentation —
// the handler implements it — and the calling-assertion test reads it.
type permMode string

const (
	// permDeny: a caller that does not hold the permission and asks for someone else's
	// resource is refused (403).
	permDeny permMode = "deny"
	// permFilter: such a caller still gets an answer, narrowed to their own resources
	// (the 过滤型 permissions).
	permFilter permMode = "filter"
)

// userCategory is the user-class category of a route.
type userCategory uint8

const (
	// userPublic: any user-class caller passes (Public).
	userPublic userCategory = iota
	// userRequire: the gate refuses a caller that misses one of the permissions.
	userRequire
	// userInHandler: the handler decides (InHandler); the gate passes through.
	userInHandler
	// userNone: the route has no user-class surface (NoUserSurface).
	userNone
	// userPending: the user side is not migrated yet (待迁移).
	userPending
)

// userRule is the user-class category of one route, with the permissions it names.
type userRule struct {
	kind  userCategory
	perms []string
	mode  permMode
}

// Public marks a route any user-class caller may reach without a permission of its own.
func Public() userRule { return userRule{kind: userPublic} }

// Require marks a route a user-class caller may reach only while holding every one of
// perms: the gate refuses the request with a 403 that names the missing permission. Its
// mode is permDeny — a plain requirement is always a refusal, it has no filtered form.
func Require(perms ...string) userRule {
	return userRule{kind: userRequire, perms: perms, mode: permDeny}
}

// InHandler marks a route whose permissions the HANDLER decides, because the answer
// depends on the resource (own vs someone else's, or a filtered list). mode records what
// the handler does about a caller that does not hold the permission.
func InHandler(mode permMode, perms ...string) userRule {
	return userRule{kind: userInHandler, perms: perms, mode: mode}
}

// NoUserSurface marks a route no user-class caller reaches: it authenticates with its own
// credential (External) or only a worker calls it. The gate adds nothing there — the
// handler owns the caller-kind rule, exactly as before T1.4.
func NoUserSurface() userRule { return userRule{kind: userNone} }

// UserPending marks a route whose user-side category is not migrated yet (plan T1.4's
// 待迁移 list): the gate adds nothing and the handler keeps its own check. Such a route
// must also be on pendingRoutes.
func UserPending() userRule { return userRule{kind: userPending} }

// routeDecl is one route's declaration: the user-class category plus the annotations.
type routeDecl struct {
	// user is what the gate does with a user-class caller.
	user userRule
	// jobCred is the SEC-01 route key this route appears under (jobcredential.go:
	// jobWriteAllowlist / stewardReadAllow / stewardWriteAllow), or "" when it appears in
	// none of them. Annotation only: the SEC-01 middleware stays the authority.
	jobCred string
	// workerSelf marks a route of workercredential.go's workerSelfRoutes. Annotation
	// only: the worker middleware stays the authority.
	workerSelf bool
	// external names the credential the route authenticates itself with (a ticket, a
	// trigger_token, a nonce, the metrics token or nothing at all). Annotation only.
	external string
	// static marks the web console's SPA fallback, which rux registers through NotFound
	// rather than as a route.
	static bool
	// scope is how the route's project is determined (PathProject / Resource / BodyProject /
	// Filter / NoProject). Required of every declaration; annotation only until S4b.
	scope routeScope
	// rw is the explicit read / write override (ReadSafe / Mutating). Left zero, the method
	// decides: GET / HEAD read, everything else write. Annotation only until S4b.
	rw rwClass
	// key is the canonical route key (`<METHOD> <path>`) the declaration was registered
	// under. wrapRoutes fills it in before attaching the gate; the table literals leave it
	// empty. The ceiling gate reads it for the read / write class of the route.
	key string
}

// routeDecls is the declaration of every route that has one, keyed by the canonical route
// key (`<METHOD> <path with {param}>`, see canonicalRouteKey). A route that is not here
// must be on pendingRoutes.
//
// T1.4 could declare a route while leaving its user side 待迁移, and exactly five did: the
// SEC-01 snapshot forced their JobCred annotation to be written then, while the design names
// a plain permission for their user side — and Require is the one category the gate ENFORCES,
// which T1.4 was not allowed to do. POST /v1/jobs left the list with T1.5a, and the four
// others — the two tracker routes (tracker.sync) and PUT /v1/steward/notes +
// POST /v1/steward/review-summary (steward.manage) — left it with T1.5g, which wrote the real
// category. No declared route is on pendingRoutes any more, and the coverage test asserts it.
var routeDecls = map[string]routeDecl{
	"POST /v1/jobs/{id}/comments":                      {user: InHandler(permDeny, authz.PermJobSubmit), jobCred: "POST /v1/jobs/*/comments", scope: Resource("job")},
	"POST /v1/plans/{id}/comments":                     {user: InHandler(permDeny, authz.PermJobSubmit), jobCred: "POST /v1/plans/*/comments", scope: Resource("plan")},
	"PUT /v1/plans/{id}/handoff":                       {user: InHandler(permDeny, authz.PermPlanManageOthers), jobCred: "PUT /v1/plans/*/handoff", scope: Resource("plan")},
	"POST /v1/plans/{id}/todos/{todo_id}/comments":     {user: InHandler(permDeny, authz.PermJobSubmit), jobCred: "POST /v1/plans/*/todos/*/comments", scope: Resource("plan")},
	"POST /v1/todos/{todo_id}/comments":                {user: InHandler(permDeny, authz.PermJobSubmit), jobCred: "POST /v1/todos/*/comments", scope: Resource("todo")},
	"POST /v1/decisions":                               {user: Public(), jobCred: "POST /v1/decisions", scope: Resource("plan")},
	"POST /v1/jobs/{id}/wakeups":                       {user: InHandler(permDeny, authz.PermJobWakeOthers, authz.PermJobSubmit), jobCred: "POST /v1/jobs/*/wakeups", scope: Resource("job")},
	"POST /v1/jobs/{id}/say":                           {user: InHandler(permDeny, authz.PermJobManageOthers), jobCred: "POST /v1/jobs/*/say", scope: Resource("job")},
	"POST /v1/jobs/{id}/end":                           {user: InHandler(permDeny, authz.PermJobManageOthers), jobCred: "POST /v1/jobs/*/end", scope: Resource("job")},
	"POST /v1/sessions/{sid}/watches":                  {user: InHandler(permDeny, authz.PermSessionReplyOthers), jobCred: "POST /v1/sessions/*/watches", scope: Resource("session")},
	"POST /v1/work-items/{id}/report":                  {user: Public(), jobCred: "POST /v1/work-items/*/report", scope: Resource("work_item")},
	"PATCH /v1/todos/{todo_id}":                        {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermJobSubmit), jobCred: "PATCH /v1/todos/*", scope: Resource("todo")},
	"PATCH /v1/jobs/{id}":                              {user: InHandler(permDeny, authz.PermJobManageOthers), jobCred: "PATCH /v1/jobs/*", scope: Resource("job")},
	"POST /v1/jobs":                                    {user: Require(authz.PermJobSubmit), jobCred: "POST /v1/jobs", scope: BodyProject()},
	"POST /v1/tracker/sync":                            {user: Require(authz.PermTrackerSync), jobCred: "POST /v1/tracker/sync", scope: Resource("tracker_repo")},
	"POST /v1/tracker/repos/{tracker_id}/rename":       {user: Require(authz.PermTrackerSync), jobCred: "POST /v1/tracker/repos/*/rename", scope: Resource("tracker_repo")},
	"POST /v1/memories/{scope}/{scope_key}/{key}/flag": {user: Public(), jobCred: "POST /v1/memories/*/*/*/flag", scope: Resource("memory")},
	"GET /v1/work-items":                               {user: Public(), jobCred: "GET /v1/work-items", scope: Filter()},
	"GET /v1/work-items/{id}":                          {user: Public(), jobCred: "GET /v1/work-items/*", scope: Resource("work_item")},
	"GET /v1/work-items/{id}/journal":                  {user: Public(), jobCred: "GET /v1/work-items/*/journal", scope: Resource("work_item")},
	"GET /v1/work-items/{id}/sessions":                 {user: Public(), jobCred: "GET /v1/work-items/*/sessions", scope: Resource("work_item")},
	"GET /v1/work-items/{id}/links":                    {user: Public(), jobCred: "GET /v1/work-items/*/links", scope: Resource("work_item")},
	"GET /v1/work-items/{id}/requests":                 {user: Public(), jobCred: "GET /v1/work-items/*/requests", scope: Resource("work_item")},
	"GET /v1/work-items/requests":                      {user: Public(), jobCred: "GET /v1/work-items/requests", scope: Filter()},
	"GET /v1/work-items/merge-suggestions":             {user: Public(), jobCred: "GET /v1/work-items/merge-suggestions", scope: Filter()},
	"GET /v1/sessions":                                 {user: InHandler(permFilter, authz.PermSessionReadOthers), jobCred: "GET /v1/sessions", scope: Filter()},
	"GET /v1/sessions/{sid}":                           {user: InHandler(permDeny, authz.PermSessionReadOthers), jobCred: "GET /v1/sessions/*", scope: Resource("session")},
	"GET /v1/sessions/{sid}/tail":                      {user: InHandler(permDeny, authz.PermSessionReadOthers), jobCred: "GET /v1/sessions/*/tail", scope: Resource("session")},
	"GET /v1/sessions/{sid}/wait":                      {user: InHandler(permDeny, authz.PermSessionReadOthers), scope: Resource("session")},
	"GET /v1/sessions/{sid}/screen":                    {user: InHandler(permDeny, authz.PermSessionReadOthers), scope: Resource("session")},
	"GET /v1/jobs":                                     {user: InHandler(permFilter, authz.PermJobReadOthers), jobCred: "GET /v1/jobs", scope: Filter()},
	"GET /v1/jobs/{id}":                                {user: InHandler(permDeny, authz.PermJobReadOthers), jobCred: "GET /v1/jobs/*", scope: Resource("job")},
	"GET /v1/steward":                                  {user: Public(), jobCred: "GET /v1/steward", scope: NoProject()},
	"GET /v1/steward/notes":                            {user: Public(), jobCred: "GET /v1/steward/notes", scope: NoProject()},
	"GET /v1/issues":                                   {user: Public(), jobCred: "GET /v1/issues", scope: Filter()},
	"GET /v1/issues/{id}":                              {user: Public(), jobCred: "GET /v1/issues/*", scope: Resource("issue")},
	"GET /v1/today":                                    {user: Public(), jobCred: "GET /v1/today", scope: Filter()},
	"GET /v1/memory-findings":                          {user: Public(), jobCred: "GET /v1/memory-findings", scope: Filter()},
	"GET /v1/memory-suggestions":                       {user: Public(), jobCred: "GET /v1/memory-suggestions", scope: Filter()},
	"PATCH /v1/work-items/{id}":                        {user: Public(), jobCred: "PATCH /v1/work-items/*", scope: Resource("work_item")},
	"POST /v1/work-items/capture":                      {user: Public(), jobCred: "POST /v1/work-items/capture", scope: BodyProject()},
	"POST /v1/work-items/{id}/journal":                 {user: Public(), jobCred: "POST /v1/work-items/*/journal", scope: Resource("work_item")},
	"POST /v1/work-items/{id}/report-request":          {user: InHandler(permDeny, authz.PermSessionReplyOthers), jobCred: "POST /v1/work-items/*/report-request", scope: Resource("work_item")},
	"POST /v1/work-items/{id}/summarize":               {user: Public(), jobCred: "POST /v1/work-items/*/summarize", scope: Resource("work_item")},
	"POST /v1/work-items/{id}/merge-suggestions":       {user: Public(), jobCred: "POST /v1/work-items/*/merge-suggestions", scope: Resource("work_item")},
	// ---- the gofer mailbox (plan M3) ----
	//
	// The letter's recipient decides the permission (设计「路由与权限」): a caller's own letters
	// (sent or received) need none; another caller's terminal session needs
	// session.reply.others, its session job job.manage.others (job.read.others to read). The
	// handler asks them through the same s.mayReplyToSession / s.mayManageJob rules every other
	// route uses; a job credential sends only to its job's source session, its plan's supervisor
	// session and its caller, and a worker is refused. Readdress is a fresh send (the NEW
	// recipient is judged) and is closed to job credentials. Redaction (POST /v1/mail/{id}/redact,
	// M8) follows job redaction's rule — the letter's own two sides or a holder of
	// job.delete.others — and a job credential may not redact either.
	"POST /v1/sessions/{sid}/mail/pause":  {user: Public(), scope: Resource("session")},
	"POST /v1/sessions/{sid}/mail/resume": {user: Public(), scope: Resource("session")},
	"POST /v1/mail":                       {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobManageOthers), jobCred: "POST /v1/mail", scope: BodyProject()},
	"GET /v1/mail":                        {user: InHandler(permFilter, authz.PermSessionReplyOthers, authz.PermJobReadOthers), jobCred: "GET /v1/mail", scope: Filter()},
	// The conversation aggregation of plan M1 is the same read as GET /v1/mail, only grouped: it
	// filters with that listing's keep predicate, so it shows nothing the listing would not. The
	// SEC-01 middleware keys it as /v1/mail/* (one segment of wildcard), like the listing.
	"GET /v1/mail/conversations": {user: InHandler(permFilter, authz.PermSessionReplyOthers, authz.PermJobReadOthers), jobCred: "GET /v1/mail/*", scope: Filter()},
	// The `agent:` addresses (设计 2026-10-22 mail-agent-address「D6」): the listing follows the
	// mailbox's read rules, ending a resident session manages a job (like a reply). Neither is
	// open to a job credential (it would be ending / listing somebody else's session): the static
	// /mail/agents routes are declared before /mail/{id}.
	"GET /v1/mail/agents":                 {user: InHandler(permFilter, authz.PermSessionReplyOthers, authz.PermJobReadOthers), scope: Filter()},
	"POST /v1/mail/agents/{name}/end":     {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobManageOthers), scope: Resource("mail")},
	"GET /v1/mail/{id}":                   {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobReadOthers), jobCred: "GET /v1/mail/*", scope: Resource("mail")},
	"POST /v1/mail/{id}/reply":            {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobManageOthers), jobCred: "POST /v1/mail/*/reply", scope: Resource("mail")},
	"POST /v1/mail/{id}/ack":              {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobManageOthers), jobCred: "POST /v1/mail/*/ack", scope: Resource("mail"), rw: ReadSafe()},
	"POST /v1/mail/{id}/cancel":           {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobManageOthers), jobCred: "POST /v1/mail/*/cancel", scope: Resource("mail")},
	"POST /v1/mail/{id}/readdress":        {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobManageOthers), scope: Resource("mail")},
	"POST /v1/mail/{id}/redact":           {user: InHandler(permDeny, authz.PermJobDeleteOthers), scope: Resource("mail")},
	"POST /v1/session-ask":                {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobManageOthers), jobCred: "POST /v1/session-ask", scope: Resource("session")},
	"PUT /v1/steward/notes":               {user: Require(authz.PermStewardManage), jobCred: "PUT /v1/steward/notes", scope: NoProject()},
	"POST /v1/steward/review-summary":     {user: Require(authz.PermStewardManage), jobCred: "POST /v1/steward/review-summary", scope: NoProject()},
	"POST /v1/today/advice":               {user: Public(), jobCred: "POST /v1/today/advice", scope: Resource("today_card")},
	"POST /v1/memory-suggestions":         {user: Public(), jobCred: "POST /v1/memory-suggestions", scope: BodyProject()},
	"GET /v1/workers/{id}/assignable":     {user: Public(), workerSelf: true, scope: Resource("worker")},
	"GET /v1/workers/{id}/upgrade/file":   {user: NoUserSurface(), workerSelf: true, scope: Resource("worker")},
	"GET /v1/xfer/{id}/content":           {user: InHandler(permDeny, authz.PermXferManageOthers), workerSelf: true, scope: Resource("xfer")},
	"PUT /v1/xfer/{id}/content":           {user: NoUserSurface(), workerSelf: true, scope: Resource("xfer")},
	"GET /health":                         {user: NoUserSurface(), external: "none", scope: NoProject()},
	"GET /metrics":                        {user: NoUserSurface(), external: "metrics.token", scope: NoProject()},
	"GET /v1/jobs/{id}/attach":            {user: NoUserSurface(), external: "attach ticket", scope: Resource("job"), rw: Mutating()},
	"GET /v1/ws":                          {user: NoUserSurface(), external: "ws ticket", scope: NoProject()},
	"POST /v1/schedules/{id}/trigger":     {user: NoUserSurface(), external: "trigger_token", scope: Resource("schedule")},
	"POST /v1/push/actions":               {user: NoUserSurface(), external: "push action token", scope: NoProject()},
	"POST /v1/worker-enroll":              {user: NoUserSurface(), external: "none", scope: NoProject()},
	"GET /v1/worker-enroll/{id}":          {user: NoUserSurface(), external: "enroll secret", scope: NoProject()},
	"POST /v1/worker-enroll/{id}/confirm": {user: NoUserSurface(), external: "enroll secret", scope: NoProject()},
	"POST /v1/worker-enroll/{id}/cancel":  {user: NoUserSurface(), external: "enroll secret", scope: NoProject()},

	// The web console's SPA fallback (Static): rux serves it through NotFound rather than
	// through a route, so it never appears in the router's route list and the coverage test
	// skips a static declaration in the reverse direction. It authenticates nothing and
	// asks nothing of the caller: it hands out index.html and the bundled assets.
	"GET /*": {user: NoUserSurface(), static: true, scope: NoProject()},

	// ---- plan T1.5a: the job domain (jobs, workflows, retries, wakeups) ----
	//
	// job.submit is the plain permission of every entry point that starts work (the
	// gate enforces it: Require). Every other job route is InHandler because the answer
	// depends on the resource — 「操作自己的资源不需要这类权限」 — and the handler applies it
	// through the rules of authz_job_rules.go. The job and plan lists
	// (GET /v1/jobs, GET /v1/jobs/{id}, PATCH /v1/jobs/{id}) are declared above, next to the
	// routes whose SEC-01 annotation forced them into the first table.
	"POST /v1/agents/{id}/probe": {user: Require(authz.PermJobSubmit), scope: NoProject()},
	"POST /v1/workflows":         {user: Require(authz.PermJobSubmit), scope: BodyProject()},
	// accept / reject / approve are InHandler: besides job.review, the answer depends on the job
	// — someone else's delivery also needs job.review.others, and a caller under a project ceiling
	// may not review a job its own ceiling owns (jobReviewer, job_handler.go). reject also needs
	// job.submit when resume=true (the resume starts a new job).
	"POST /v1/jobs/{id}/accept":                {user: InHandler(permDeny, authz.PermJobReview, authz.PermJobReviewOthers), scope: Resource("job")},
	"POST /v1/jobs/{id}/reject":                {user: InHandler(permDeny, authz.PermJobReview, authz.PermJobReviewOthers, authz.PermJobSubmit), scope: Resource("job")},
	"POST /v1/jobs/{id}/approve":               {user: InHandler(permDeny, authz.PermJobReview, authz.PermJobReviewOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/request":                {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/logs/stdout":            {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/logs/stderr":            {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/stream":                 {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/acp/stream":             {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/events":                 {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/deliveries":             {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/comments":               {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/artifacts":              {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/artifacts/{name}":       {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/diff":                   {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/review-hints":           {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/worktree":               {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/wakeups":                {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/wakeups/{wid}":                    {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("wakeup")},
	"GET /v1/jobs/{id}/retries":                {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"GET /v1/workflows":                        {user: InHandler(permFilter, authz.PermJobReadOthers), scope: Filter()},
	"GET /v1/workflows/{id}":                   {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("workflow")},
	"GET /v1/workflows/{id}/events":            {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("workflow")},
	"GET /v1/workflows/{id}/export":            {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("workflow")},
	"GET /v1/workflow-templates":               {user: Public(), scope: Filter()},
	"GET /v1/workflow-templates/{name}":        {user: Public(), scope: Filter()},
	"GET /v1/workflow-templates/{name}/source": {user: Public(), scope: BodyProject()},
	// Template writes are server-configuration writes: config.write, and a caller limited to
	// some projects can only write those projects' templates (BodyProject + the handler's
	// requireProjectScope; a global template needs the unscoped caller).
	"PUT /v1/workflow-templates/{name}":         {user: Require(authz.PermConfigWrite), scope: BodyProject()},
	"DELETE /v1/workflow-templates/{name}":      {user: Require(authz.PermConfigWrite), scope: BodyProject()},
	"POST /v1/workflows/{id}/save-as-template":  {user: Require(authz.PermConfigWrite), scope: Resource("workflow")},
	"POST /v1/workflow-templates/{name}/render": {user: Public(), scope: BodyProject(), rw: ReadSafe()},
	"POST /v1/jobs/secret-scan":                 {user: InHandler(permFilter, authz.PermSecretScanAll), scope: BodyProject()},
	"POST /v1/jobs/{id}/findings/issue":         {user: Public(), scope: Resource("job")},
	// attach: someone else's (or an ownerless) job needs job.attach.others, the
	// caller's OWN job the attach gate job.attach (design §1 / row 38, 54, 55).
	"POST /v1/jobs/{id}/attach-ticket":               {user: InHandler(permDeny, authz.PermJobAttachOthers, authz.PermJobAttach), scope: Resource("job")},
	"GET /v1/jobs/{id}/pty/recording":                {user: InHandler(permDeny, authz.PermJobAttachOthers, authz.PermJobAttach), scope: Resource("job")},
	"GET /v1/jobs/{id}/pty/sessions":                 {user: InHandler(permDeny, authz.PermJobAttachOthers, authz.PermJobAttach), scope: Resource("job")},
	"GET /v1/pty/sessions":                           {user: InHandler(permFilter, authz.PermJobAttachOthers), scope: Filter()},
	"POST /v1/jobs/{id}/cancel":                      {user: InHandler(permDeny, authz.PermJobCancelOthers), scope: Resource("job")},
	"POST /v1/workflows/{id}/cancel":                 {user: InHandler(permDeny, authz.PermJobCancelOthers), scope: Resource("workflow")},
	"DELETE /v1/retries/{rid}":                       {user: InHandler(permDeny, authz.PermJobCancelOthers), scope: Resource("retry")},
	"DELETE /v1/jobs/{id}":                           {user: InHandler(permDeny, authz.PermJobDeleteOthers), scope: Resource("job")},
	"POST /v1/jobs/{id}/redact":                      {user: InHandler(permDeny, authz.PermJobDeleteOthers), scope: Resource("job")},
	"POST /v1/jobs/{id}/resume":                      {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit), scope: Resource("job")},
	"POST /v1/jobs/{id}/follow-ups":                  {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit), scope: Resource("job")},
	"GET /v1/jobs/{id}/follow-ups":                   {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit), scope: Resource("job")},
	"DELETE /v1/jobs/{id}/follow-ups/{follow_up_id}": {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit), scope: Resource("job")},
	"POST /v1/jobs/{id}/rebuild":                     {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit), scope: Resource("job")},
	"POST /v1/workflows/{id}/pick":                   {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit), scope: Resource("workflow")},
	// S4 rerun (design §一·7) starts NEW work from the original spec, so it needs what a
	// submit needs (job.submit; job.manage.others for someone else's workflow) — the same
	// rule as resume / pick above.
	"POST /v1/workflows/{id}/rerun":     {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit), scope: Resource("workflow")},
	"POST /v1/jobs/{id}/deliver":        {user: InHandler(permDeny, authz.PermJobManageOthers), scope: Resource("job")},
	"DELETE /v1/jobs/{id}/worktree":     {user: InHandler(permDeny, authz.PermJobManageOthers), scope: Resource("job")},
	"POST /v1/jobs/{id}/worktree/merge": {user: InHandler(permDeny, authz.PermJobManageOthers), scope: Resource("job")},
	"PATCH /v1/wakeups/{wid}":           {user: InHandler(permDeny, authz.PermJobWakeOthers, authz.PermJobSubmit), scope: Resource("wakeup")},
	"DELETE /v1/wakeups/{wid}":          {user: InHandler(permDeny, authz.PermJobWakeOthers), scope: Resource("wakeup")},

	// ---- plan T1.5b: interactions, decisions and browser push ----
	//
	// `interaction.answer` is the 代答 permission: answering a job's tool approval or
	// question is NOT about the resource — any holder answers any job's interaction, which is
	// what the pre-migration handler did too — so those two routes are Require and the route
	// gate decides (design §1「回答 job 的工具审批与提问、punt」). Injecting an interaction and
	// reading a job's interactions ARE about the resource, so they are InHandler with the job
	// rules of authz_job_rules.go (job.manage.others / job.read.others), and the cross-job
	// pending list is their filter form (design §1: `job.read.others` 覆盖「interaction 列表
	// （含 GET /v1/interactions）」).
	//
	// A relay turn IS speech into a terminal session, so the decision answer route decides by
	// the row's KIND: kind=relay → `session.reply.others` (its own session's owner, or an
	// ownerless one, needs nothing — the rule of authz_session_rules.go), any other kind →
	// `interaction.answer` (design §1「中继类决策回答」and「非中继类决策」).
	//
	// The browser-push management routes keep their old user-only rule in the handler
	// (pushUserCaller: a worker token and a job credential may not keep a browser
	// subscription); the notification's ACTION BUTTONS are the permission-gated part, and
	// internal/webpush checks them at MINT and at REDEEM time (design §6).
	"POST /v1/jobs/{id}/interactions":                         {user: InHandler(permDeny, authz.PermJobManageOthers), scope: Resource("job")},
	"GET /v1/jobs/{id}/interactions":                          {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("job")},
	"POST /v1/jobs/{id}/interactions/{interaction_id}/answer": {user: Require(authz.PermInteractionAnswer), scope: Resource("job")},
	"POST /v1/jobs/{id}/interactions/{interaction_id}/punt":   {user: Require(authz.PermInteractionAnswer), scope: Resource("job")},
	"GET /v1/interactions":                                    {user: InHandler(permFilter, authz.PermJobReadOthers), scope: Filter()},
	"POST /v1/decisions/{id}/answer":                          {user: InHandler(permDeny, authz.PermInteractionAnswer, authz.PermSessionReplyOthers), scope: Resource("decision")},
	"GET /v1/decisions":                                       {user: Public(), scope: Filter()},
	"GET /v1/decisions/{id}":                                  {user: Public(), scope: Resource("decision")},
	"GET /v1/push/vapid-public-key":                           {user: Public(), scope: NoProject()},
	"GET /v1/push/subscriptions":                              {user: Public(), scope: NoProject()},
	"POST /v1/push/subscriptions":                             {user: Public(), scope: NoProject(), rw: ReadSafe()},
	"DELETE /v1/push/subscriptions":                           {user: Public(), scope: NoProject(), rw: ReadSafe()},
	"POST /v1/push/test":                                      {user: Public(), scope: NoProject()},
	"POST /v1/ws-ticket":                                      {user: Public(), scope: NoProject(), rw: ReadSafe()},

	// ---- plan T1.5c: terminal sessions, nudges and the workbench ----
	//
	// Four session permissions divide the domain, all of them InHandler because
	// 「操作自己的资源不需要这类权限」 (authz_session_rules.go holds the rules):
	//
	//   session.read.others    reads — the session list and the cross-session nudge list are
	//                          its FILTER form (without it: only the caller's own rows, no
	//                          403); a single session, its messages, turns, nudge list and
	//                          takeover plan are its deny form.
	//   session.reply.others   说话与待发内容 — say / messages / relay / deliver / the turn
	//                          acks / watches / nudges / outbox (the design's 0.5 revision took
	//                          the outbox back out of session.read.others) and the two entries
	//                          whose TARGET decides (session-ask, workbench turn).
	//   session.manage.others  deleting a session, opening or releasing a turn and releasing a
	//                          takeover.
	//   session.permission.answer  answering someone ELSE's terminal tool prompt (design §5):
	//                          the session's own owner needs nothing, and the permission is in
	//                          the 严格检查 group, so only a root administrator holds it.
	//
	// POST /v1/sessions/{sid}/owner is the one plain requirement: `session.transfer` belongs to
	// the root administrators alone (design §1 / §3, independent of the governance switches),
	// which is what the handler's old can_admin check asked.
	//
	// The routes that ask nothing of the caller keep their old rules in their handlers —
	// registering / heartbeating / reporting a reply is the owner rule of the relay itself
	// (RegisterForOwner / HeartbeatForOwner / RecordReply), and opening and resolving a tool
	// prompt is the HOOK's own action (permissionCallerIsOwner), not a permission.
	//
	// Three entries are job.submit entries as well (design §1「一切产生 job 的入口…只在这次请求会产生
	// job 时另需本权限」): a resume always starts the takeover job, a deliver with allow_takeover
	// starts a pty job, and continuing an AGENT (session-bound) workbench thread starts one when
	// the thread is not already waiting for input. A branch that produces no job — a plain tmux
	// deliver, a relay thread turn — does not check it (InHandler, the handler decides).
	"POST /v1/sessions":                    {user: Public(), scope: BodyProject()},
	"POST /v1/sessions/{sid}/heartbeat":    {user: Public(), scope: Resource("session")},
	"POST /v1/sessions/{sid}/replies":      {user: Public(), scope: Resource("session")},
	"POST /v1/sessions/{sid}/owner":        {user: Require(authz.PermSessionTransfer), scope: Resource("session")},
	"GET /v1/sessions/{sid}/messages":      {user: InHandler(permDeny, authz.PermSessionReadOthers), scope: Resource("session")},
	"GET /v1/sessions/{sid}/nudges":        {user: InHandler(permDeny, authz.PermSessionReadOthers), scope: Resource("session")},
	"GET /v1/sessions/{sid}/takeover-plan": {user: InHandler(permDeny, authz.PermSessionReadOthers), scope: Resource("session")},
	"GET /v1/sessions/{sid}/turns/{id}":    {user: InHandler(permDeny, authz.PermSessionReadOthers), scope: Resource("session")},
	"GET /v1/sessions/{sid}/watches":       {user: InHandler(permDeny, authz.PermSessionReadOthers), scope: Resource("session")},
	"GET /v1/sessions/{sid}/focus":         {user: InHandler(permDeny, authz.PermSessionReadOthers), scope: Resource("session")},
	"GET /v1/nudges":                       {user: InHandler(permFilter, authz.PermSessionReadOthers), scope: Filter()},
	"POST /v1/sessions/{sid}/messages":     {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"GET /v1/sessions/{sid}/outbox":        {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/say":          {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/deliver":      {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobSubmit), scope: Resource("session")},
	"POST /v1/sessions/{sid}/resume":       {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobSubmit), scope: Resource("session")},
	"POST /v1/sessions/{sid}/relay":        {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	// 终端投递模式与「现在送」（2026-10-22 D5）：改的是“消息能不能打进来”，权限同回答本会话。
	"POST /v1/sessions/{sid}/inject-mode":                 {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/held/send":                   {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/nudges":                      {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/watches/complete":            {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"DELETE /v1/sessions/{sid}/watches/{job_id}":          {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/turns/{id}/ack":              {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"DELETE /v1/sessions/{sid}/turns/{id}/ack":            {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/turns/{id}/complete-watches": {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	// 信箱取件（hook 的 claim → confirm → 注入）：自己的会话不需要权限，他人的需要 session.reply.others；
	// job 凭据与 worker 一律拒绝（不声明 JobCred / WorkerSelf）。
	"POST /v1/sessions/{sid}/mail/claim":               {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/mail/confirm":             {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/turns/{id}/complete-mail": {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("session")},
	"PATCH /v1/nudges/{id}":                            {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("nudge")},
	"DELETE /v1/nudges/{id}":                           {user: InHandler(permDeny, authz.PermSessionReplyOthers), scope: Resource("nudge")},
	"DELETE /v1/sessions/{sid}":                        {user: InHandler(permDeny, authz.PermSessionManageOthers), scope: Resource("session")},
	// 会话主题：自己的会话不需要权限，他人的需要 session.manage.others；job 凭据与 worker 一律拒绝。
	"PATCH /v1/sessions/{sid}":                        {user: InHandler(permDeny, authz.PermSessionManageOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/turns":                   {user: InHandler(permDeny, authz.PermSessionManageOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/turns/{id}/release":      {user: InHandler(permDeny, authz.PermSessionManageOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/release-takeover":        {user: InHandler(permDeny, authz.PermSessionManageOthers), scope: Resource("session")},
	"POST /v1/sessions/{sid}/permissions/{id}/answer": {user: InHandler(permDeny, authz.PermSessionPermissionAnswer), scope: Resource("session")},
	"POST /v1/sessions/{sid}/permissions":             {user: Public(), scope: Resource("session")},
	"POST /v1/sessions/{sid}/permissions/resolve":     {user: Public(), scope: Resource("session")},

	// The workbench routes (design rows 272–279). The layout, the thread preferences and the
	// seen-all marker only ever touch the CALLER's own preference rows, so they ask nothing (a
	// worker token and a job credential keep the handler's user-only refusal). The listing and
	// the diff are job.read.others — its filter form for the list, its deny form for one
	// thread — and review / turn are 对会话 job 说话, hence job.manage.others; a RELAY thread turn
	// is speech into a terminal session instead, session.reply.others.
	"GET /v1/workbench/layout":               {user: Public(), scope: NoProject()},
	"PUT /v1/workbench/layout":               {user: Public(), scope: NoProject(), rw: ReadSafe()},
	"POST /v1/workbench/threads/seen-all":    {user: Public(), scope: NoProject(), rw: ReadSafe()},
	"PATCH /v1/workbench/threads/{id}":       {user: Public(), scope: Resource("workbench_thread"), rw: ReadSafe()},
	"GET /v1/workbench/threads":              {user: InHandler(permFilter, authz.PermJobReadOthers), scope: Filter()},
	"GET /v1/workbench/threads/{id}/diff":    {user: InHandler(permDeny, authz.PermJobReadOthers), scope: Resource("workbench_thread")},
	"POST /v1/workbench/threads/{id}/review": {user: InHandler(permDeny, authz.PermJobManageOthers), scope: Resource("workbench_thread")},
	"POST /v1/workbench/threads/{id}/turn":   {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermSessionReplyOthers, authz.PermJobSubmit), scope: Resource("workbench_thread")},

	// Workbench image attachments. An upload only needs a login: the file is the uploader's
	// own until it is sent, so upload and delete are personal-resource writes (ReadSafe, like
	// the layout) that a ceilinged caller keeps — they change nothing outside the caller's
	// own unsent files; a job credential and a worker are refused by the handlers. Reading is
	// Resource("attachment"): an unsent file has no project and is judged by the handler
	// (uploader only); a SENT one resolves to its thread's project, so a ceilinged caller
	// cannot read another project's attachments, and the handler then applies the thread's own
	// visibility (owner or *.read.others, see AttachmentThreadAccess).
	"POST /v1/attachments":        {user: Public(), scope: NoProject(), rw: ReadSafe()},
	"GET /v1/attachments/{id}":    {user: Public(), scope: Resource("attachment")},
	"DELETE /v1/attachments/{id}": {user: Public(), scope: Resource("attachment"), rw: ReadSafe()},

	// ---- plan T1.5e: schedules ----
	//
	// 一个 schedule 的创建者在它存的 request 里（handleCreateSchedule 把 req.CallerID 盖上去），所以
	// 「自己的 schedule」= 存的那份 request 的 caller id（scheduleOwner）—— 与 gofer-hxld 的 trigger_token
	// 掩码读的是同一个值。创建者自己的 schedule 不需要权限，他人的需要 schedule.manage.others（种入，
	// 所以迁移后的默认部署与迁移前一样宽）。POST /v1/schedules 是唯一一条纯要求：建 schedule 是 design §1
	// 的 job.submit 入口，路由门（Require）直接判定。
	//
	// 两个入口另查 job.submit（design §1：「schedules 创建 / run-now / enable」）：run-now 在这次请求里就
	// 提交存的那份 request；enable 打开 sweep，之后 cron 循环起 job 且再无检查点，所以在「打开」这一步查
	// （authz_schedule_rules.go 的 mayStartScheduleJobs）。disable 只是关掉，不查 job.submit。
	//
	// 两条读接口是 Public（保持现状）：design §1 的目录行给 schedule.manage.others 的覆盖
	// 是「他人 schedule 的删除 / 启停 / 轮换 token / run-now」的写动作（不像紧邻的 xfer.manage.others
	// 行写了「读取」），而读带出来的 trigger_token 已由 gofer-hxld 按调用方掩码 —— 附录 A 的建议列曾把
	// 读划给该权限，但「附录 A 的『建议权限』列以正文为准」。
	"POST /v1/schedules":                   {user: Require(authz.PermJobSubmit), scope: BodyProject()},
	"GET /v1/schedules":                    {user: Public(), scope: Filter()},
	"GET /v1/schedules/{id}":               {user: Public(), scope: Resource("schedule")},
	"GET /v1/schedules/{id}/events":        {user: Public(), scope: Resource("schedule")},
	"DELETE /v1/schedules/{id}":            {user: InHandler(permDeny, authz.PermScheduleManageOthers), scope: Resource("schedule")},
	"POST /v1/schedules/{id}/disable":      {user: InHandler(permDeny, authz.PermScheduleManageOthers), scope: Resource("schedule")},
	"POST /v1/schedules/{id}/enable":       {user: InHandler(permDeny, authz.PermScheduleManageOthers, authz.PermJobSubmit), scope: Resource("schedule")},
	"POST /v1/schedules/{id}/run-now":      {user: InHandler(permDeny, authz.PermScheduleManageOthers, authz.PermJobSubmit), scope: Resource("schedule")},
	"POST /v1/schedules/{id}/rotate-token": {user: InHandler(permDeny, authz.PermScheduleManageOthers), scope: Resource("schedule")},
	// PATCH edits the task in place (cron/at, title, params, enabled, timeout_sec): the same
	// permission as deleting it. The handler asks job.submit on top when the body sets
	// enabled:true, because that turns the cron loop loose on the stored request.
	"PATCH /v1/schedules/{id}": {user: InHandler(permDeny, authz.PermScheduleManageOthers, authz.PermJobSubmit), scope: Resource("schedule")},

	// ---- plan T1.5d: plans 与 todos、work items（含 Today 首页）----
	//
	// 一个 plan 是协作容器（design §1）：它的读全部是 Public — 任何 user 类调用方看得到每个
	// plan — 而写看 plan.manage.others（种入，所以默认部署与迁移前一样宽）：写自己的 plan 不需
	// 要权限，写他人的 plan 需要它（「操作自己的资源不需要这类权限」）。todo 自己没有 owner：它
	// 所在 plan 的 owner 就是每一条 todo 规则的资源 owner（authz_plan_rules.go）。
	//
	// 三处另有权限：
	//   plan.supervise.others  给他人 plan 绑定 supervisor、或绑定他人的会话（仅根，是旧的
	//                          strict-admin 检查）：PATCH /v1/plans/{id} 重绑 supervisor 时
	//                          先查 plan owner、再要本权限。
	//   job.submit             只在「这次请求会起 job」时另需的入口（design §1 的 job.submit
	//                          行）：run / resume 派发 plan 里的 ready 项，PATCH 开启 leader
	//                          开关，todo PATCH 改为 ready 或改 assignee 即派发，todo
	//                          dispatch 显式派发。四处都在写之前查，所以被拒的请求什么都不变。
	//   plan.manage.others     其余写入口：handoff、挂 job、加 todo、pause，以及把 work item
	//                          转进他人 plan。
	//
	// work item 本身照 design 保持 Public（「work item 没有 owner 模型」）：它的写入口只拒 worker
	// 与 job 凭据（workNotAWorker，仍在 handler 里），不向 user 类调用方要权限。
	//
	// POST /v1/plans 也是 Public：design 只在 body 带 supervisor_session_id 时要求会话属于本人
	// 或根管理员，所以那一条分支留在 handler 里（callerIsRootAdmin，等价于仅根的
	// plan.supervise.others），路由本身不向调用方要权限。
	"PATCH /v1/plans/{id}":              {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermPlanSuperviseOthers, authz.PermJobSubmit), scope: Resource("plan")},
	"POST /v1/plans/{id}/jobs":          {user: InHandler(permDeny, authz.PermPlanManageOthers), scope: Resource("plan")},
	"POST /v1/plans/{id}/pause":         {user: InHandler(permDeny, authz.PermPlanManageOthers), scope: Resource("plan")},
	"POST /v1/plans/{id}/resume":        {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermJobSubmit), scope: Resource("plan")},
	"POST /v1/plans/{id}/run":           {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermJobSubmit), scope: Resource("plan")},
	"POST /v1/plans/{id}/todos":         {user: InHandler(permDeny, authz.PermPlanManageOthers), scope: Resource("plan")},
	"POST /v1/todos/{todo_id}/dispatch": {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermJobSubmit), scope: Resource("todo")},
	"POST /v1/work-items/{id}/to-todo":  {user: InHandler(permDeny, authz.PermPlanManageOthers), scope: Resource("work_item")},
	// The capture box's session recommendation: others' sessions are filtered out before
	// ranking (session.read.others); a job credential is refused in the handler.
	"GET /v1/work-items/{id}/dispatch-targets": {user: InHandler(permFilter, authz.PermSessionReadOthers), scope: Resource("work_item")},
	// The capture box's hand-over: to=session needs session.reply.others on someone else's
	// session, to=job job.read.others on someone else's job, to=todo plan.manage.others on
	// someone else's plan; archive / drop need nothing. A job credential is refused in the handler.
	"POST /v1/work-items/{id}/dispatch": {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobReadOthers, authz.PermPlanManageOthers), scope: Resource("work_item")},
	// The reads (Public), the two supervisor reads beside the create route, and the domain's
	// SEC-01-annotated entries whose user side was already Public in the first table above.
	"POST /v1/plans":                                       {user: Public(), scope: BodyProject()},
	"GET /v1/plans":                                        {user: Public(), scope: Filter()},
	"GET /v1/plans/{id}":                                   {user: Public(), scope: Resource("plan")},
	"GET /v1/plans/{id}/events":                            {user: Public(), scope: Resource("plan")},
	"GET /v1/plans/{id}/comments":                          {user: Public(), scope: Resource("plan")},
	"GET /v1/plans/{id}/todos/{todo_id}/comments":          {user: Public(), scope: Resource("plan")},
	"GET /v1/plans/{id}/handoff":                           {user: Public(), scope: Resource("plan")},
	"GET /v1/plans/{id}/handoff/history":                   {user: Public(), scope: Resource("plan")},
	"GET /v1/todos/{todo_id}/comments":                     {user: Public(), scope: Resource("todo")},
	"POST /v1/work-items":                                  {user: Public(), scope: BodyProject()},
	"POST /v1/work-items/delete":                           {user: Public(), scope: Resource("work_item")},
	"DELETE /v1/work-items/{id}":                           {user: Public(), scope: Resource("work_item")},
	"POST /v1/work-items/{id}/links":                       {user: Public(), scope: Resource("work_item")},
	"DELETE /v1/work-items/{id}/links":                     {user: Public(), scope: Resource("work_item")},
	"POST /v1/work-items/{id}/merge":                       {user: Public(), scope: Resource("work_item")},
	"POST /v1/work-items/{id}/split":                       {user: Public(), scope: Resource("work_item")},
	"POST /v1/work-items/{id}/sessions":                    {user: Public(), scope: Resource("work_item")},
	"DELETE /v1/work-items/{id}/sessions/{sid}":            {user: Public(), scope: Resource("work_item")},
	"POST /v1/work-items/{id}/suggestions/{field}/accept":  {user: Public(), scope: Resource("work_item")},
	"POST /v1/work-items/{id}/suggestions/{field}/dismiss": {user: Public(), scope: Resource("work_item")},
	"GET /v1/work-items/digest":                            {user: Public(), scope: Filter()},
	"POST /v1/work-items/digest":                           {user: Public(), scope: Filter()},
	"GET /v1/work-items/summarizer":                        {user: Public(), scope: NoProject()},
	"POST /v1/work-items/merge-suggestions/{n}/accept":     {user: Public(), scope: Resource("merge_suggestion")},
	"POST /v1/work-items/merge-suggestions/{n}/dismiss":    {user: Public(), scope: Resource("merge_suggestion")},
	// The dashboard over plans, todos and jobs (Today): it reads and writes only the calling
	// person's own view rows, so every route asks nothing of a user-class caller.
	"POST /v1/today/actions":        {user: Public(), scope: Resource("today_card")},
	"GET /v1/today/handled":         {user: Public(), scope: Filter()},
	"GET /v1/today/lanes":           {user: Public(), scope: Filter()},
	"POST /v1/today/snooze":         {user: Public(), scope: NoProject(), rw: ReadSafe()},
	"DELETE /v1/today/snooze/{key}": {user: Public(), scope: NoProject(), rw: ReadSafe()},
	"GET /v1/today/snoozed":         {user: Public(), scope: NoProject()},

	// ---- plan T1.5f: tunnels ----
	//
	// The design's three tunnel permissions divide the domain (authz_tunnel_rules.go holds the
	// handler rules):
	//
	//   tunnel.open            opening a tunnel connection — the /v1/tunnels/connect endpoint and
	//                          the in-process dial of a hosted forwarder. That endpoint is
	//                          registered OUTSIDE the /v1 group (a WS handshake cannot use the JSON
	//                          error envelope), so the gate resolves no caller for it: the handler
	//                          authenticates, stamps the caller onto the request context and asks
	//                          the permission through s.can itself, which is why the route says
	//                          InHandler (the declaration records who decides, and the reverse case
	//                          drives it).
	//   tunnel.hosted          托管转发启停、写或删预设、导入预设 — every hosted route, and each of
	//                          them 另需 tunnel.open (design §3「有意不保持现状的三处」#2). Four of
	//                          the five ask nothing about a resource, so the gate enforces the pair
	//                          (Require); the STOP route is InHandler because stopping someone else's
	//                          hosted forwarder is tunnel.manage.others.
	//   tunnel.manage.others   停他人的转发与托管转发 (仅根, the pre-migration owner-or-can_admin
	//                          tests): the external forwarder stop route and the other-caller branch
	//                          of the hosted stop route.
	//
	// The two lists stay Public — 「隧道与转发的列表为 Public，保持现状」 (design §1) — and so does the
	// forwarder REGISTRATION surface (register / heartbeat / goodbye), which is the forwarding
	// process's own bookkeeping and stays owner-only in the registry; the design's catalog gives it
	// no permission. Appendix A's draft column suggested tunnel.open for those three and a filter
	// form for the lists; 附录 A 的「建议权限」列以正文为准.
	"GET /v1/tunnels/connect": {
		user:     InHandler(permDeny, authz.PermTunnelOpen),
		external: "bearer token (the handler resolves the caller and decides tunnel.open)",
		scope:    NoProject(), rw: Mutating(),
	},
	"POST /v1/tunnels/forwarders/{id}/stop": {
		user:  InHandler(permDeny, authz.PermTunnelManageOthers),
		scope: NoProject(),
	},
	"DELETE /v1/tunnels/hosted/{name}": {
		user:  InHandler(permDeny, authz.PermTunnelHosted, authz.PermTunnelOpen, authz.PermTunnelManageOthers),
		scope: NoProject(),
	},
	"POST /v1/tunnels/hosted/{name}":        {user: Require(authz.PermTunnelHosted, authz.PermTunnelOpen), scope: NoProject()},
	"POST /v1/tunnels/local-presets/{name}": {user: Require(authz.PermTunnelHosted, authz.PermTunnelOpen), scope: NoProject()},
	"PUT /v1/tunnels/presets/{name}":        {user: Require(authz.PermTunnelHosted, authz.PermTunnelOpen), scope: NoProject()},
	"DELETE /v1/tunnels/presets/{name}":     {user: Require(authz.PermTunnelHosted, authz.PermTunnelOpen), scope: NoProject()},
	"GET /v1/tunnels":                       {user: Public(), scope: NoProject()},
	"GET /v1/tunnels/forwarders":            {user: Public(), scope: NoProject()},
	"POST /v1/tunnels/forwarders":           {user: Public(), scope: NoProject()},
	"PUT /v1/tunnels/forwarders/{id}":       {user: Public(), scope: NoProject()},
	"DELETE /v1/tunnels/forwarders/{id}":    {user: Public(), scope: NoProject()},
	"GET /v1/tunnels/local-presets":         {user: Public(), scope: NoProject()},
	"GET /v1/tunnels/presets":               {user: Public(), scope: NoProject()},
	"GET /v1/tunnels/presets/{name}":        {user: Public(), scope: NoProject()},

	// ---- plan T1.5g: transfers, tracker, memories, the steward and the session tail ----
	//
	// Four plain requirements carry the domain, all of them 种入 except tracker.delete:
	//
	//   xfer.write     创建传输（在执行主机读写文件）— 危险级, so it is written out literally (a
	//                  wildcard never reaches a dangerous permission anyway). Both create routes
	//                  ask it: the precheck validates exactly what the create validates, so a
	//                  caller that may not create a transfer must not be told a target is fine.
	//   memory.write   写、删 tracker 记忆与 scoped 记忆，采纳记忆建议 — 危险级 (the memories reach
	//                  every job's prompt). The design's 0.5 revision moved tracker 记忆删除 into
	//                  this row (the appendix's draft column said tracker.delete; 附录 A 的「建议
	//                  权限」列以正文为准).
	//   tracker.sync   推快照、repo sync — the two sync entries and the legacy-id rename.
	//   tracker.delete 删除 tracker 镜像 — the admin group (design §3): with
	//                  require_admin_capability off the `users` subject holds it, with the switch
	//                  on only the root administrator does. That is exactly the pre-migration admin
	//                  test the handler dropped (tracker_repo_sync_handler.go).
	//   steward.manage steward start / stop / restart / ask / review，以及 notes 写入.
	//
	// The domain's one InHandler rule is xfer.manage.others (authz_xfer_rules.go): reading,
	// downloading and deleting SOMEONE ELSE's transfer is 仅根, and deliberately NOT seeded —
	// design §3「有意不保持现状的三处」#1「xfer.manage.others 不种入：他人的传输只有根管理员能读取、
	// 下载」. The transfer list is its filter form.
	//
	// Everything else the domain owns asks NOTHING of a user-class caller (Public): the tracker
	// issue reads and writes (design §1 gives the tracker only tracker.sync / tracker.delete,
	// and an issue edit is neither), the tracker and scoped memory READS, the documents the
	// steward reads, and the two 「not a write」 decisions (rejecting a knowledge candidate,
	// dismissing a memory suggestion — design §1's memory.write row covers 采纳, not 丢弃). The
	// plain 「person only」 refusals of the handlers (stewardUserOnly, workNotAWorker,
	// scopedMemoryUserWrite, the tracker repo delete's user-only half) stay where they were: they
	// are about the caller KIND, not about a permission.
	//
	// The SEC-01-annotated entries of the domain are declared in the first table above —
	// POST /v1/tracker/sync, POST /v1/tracker/repos/{tracker_id}/rename,
	// POST /v1/memories/{scope}/{scope_key}/{key}/flag (the agent's stale-memory report), PUT
	// /v1/steward/notes, POST /v1/steward/review-summary, GET /v1/steward, GET /v1/steward/notes
	// and GET /v1/xfer/{id}/content (InHandler + WorkerSelf) — because the SEC-01 snapshot of
	// T1.4 forced them there; they were re-read, not re-declared, here.
	"POST /v1/xfer":                                 {user: Require(authz.PermXferWrite), scope: BodyProject()},
	"POST /v1/xfer/precheck":                        {user: Require(authz.PermXferWrite), scope: BodyProject()},
	"GET /v1/xfer":                                  {user: InHandler(permFilter, authz.PermXferManageOthers), scope: Filter()},
	"GET /v1/xfer/{id}":                             {user: InHandler(permDeny, authz.PermXferManageOthers), scope: Resource("xfer")},
	"DELETE /v1/xfer/{id}":                          {user: InHandler(permDeny, authz.PermXferManageOthers), scope: Resource("xfer")},
	"POST /v1/tracker/repos/{tracker_id}/sync":      {user: Require(authz.PermTrackerSync), scope: Resource("tracker_repo")},
	"DELETE /v1/tracker/repos/{tracker_id}":         {user: Require(authz.PermTrackerDelete), scope: Resource("tracker_repo")},
	"POST /v1/tracker/repos/{tracker_id}/issues":    {user: Public(), scope: Resource("tracker_repo")},
	"GET /v1/tracker/issues":                        {user: Public(), scope: Resource("tracker_repo")},
	"POST /v1/tracker/issues/batch":                 {user: Public(), scope: Resource("tracker_repo")},
	"GET /v1/tracker/issues/{id}":                   {user: Public(), scope: Resource("tracker_repo")},
	"PUT /v1/tracker/issues/{id}":                   {user: Public(), scope: Resource("tracker_repo")},
	"POST /v1/tracker/issues/{id}/comments":         {user: Public(), scope: Resource("tracker_repo")},
	"GET /v1/tracker/memories":                      {user: Public(), scope: Resource("tracker_repo")},
	"PUT /v1/tracker/memories/{id}":                 {user: Require(authz.PermMemoryWrite), scope: Resource("tracker_repo")},
	"DELETE /v1/tracker/memories/{id}":              {user: Require(authz.PermMemoryWrite), scope: Resource("tracker_repo")},
	"GET /v1/tracker/repos":                         {user: Public(), scope: Filter()},
	"GET /v1/tracker/repos/{tracker_id}/clients":    {user: Public(), scope: Resource("tracker_repo")},
	"GET /v1/memories":                              {user: Public(), scope: Filter()},
	"POST /v1/memories":                             {user: Require(authz.PermMemoryWrite), scope: BodyProject()},
	"GET /v1/memories/{scope}/{scope_key}/{key}":    {user: Public(), scope: Resource("memory")},
	"PUT /v1/memories/{scope}/{scope_key}/{key}":    {user: Require(authz.PermMemoryWrite), scope: Resource("memory")},
	"DELETE /v1/memories/{scope}/{scope_key}/{key}": {user: Require(authz.PermMemoryWrite), scope: Resource("memory")},
	// The DELETE half of the flag is the HUMAN review of a stale memory (the POST half is the
	// agent's report and stays Public, first table above): 「写、删 … scoped 记忆」.
	"DELETE /v1/memories/{scope}/{scope_key}/{key}/flag": {user: Require(authz.PermMemoryWrite), scope: Resource("memory")},
	"GET /v1/memory-candidates":                          {user: Public(), scope: Filter()},
	"POST /v1/memory-candidates/{n}/accept":              {user: Require(authz.PermMemoryWrite), scope: Resource("memory_candidate")},
	"POST /v1/memory-candidates/{n}/reject":              {user: Public(), scope: Resource("memory_candidate")},
	"POST /v1/memory-suggestions/{n}/adopt":              {user: Require(authz.PermMemoryWrite), scope: Resource("memory_suggestion")},
	"POST /v1/memory-suggestions/{n}/dismiss":            {user: Public(), scope: Resource("memory_suggestion")},
	"POST /v1/steward/start":                             {user: Require(authz.PermStewardManage), scope: NoProject()},
	"POST /v1/steward/stop":                              {user: Require(authz.PermStewardManage), scope: NoProject()},
	"POST /v1/steward/restart":                           {user: Require(authz.PermStewardManage), scope: NoProject()},
	"POST /v1/steward/ask":                               {user: Require(authz.PermStewardManage), scope: NoProject()},
	"POST /v1/steward/review":                            {user: Require(authz.PermStewardManage), scope: NoProject()},

	// ---- plan T1.5h: configuration, projects, rules, skills, MCP, workers, updates ----
	//
	// Three 种入 rows of design §1 carry the plain requirements of the domain:
	//
	//   config.read   `GET /v1/config` and the MCP REGISTRY reads. The design names its
	//                 exception in the same cell — `/meta`, `/agents` and `/runners` stay
	//                 Public — and so do the project, rule, skill and worker reads below,
	//                 because the catalog gives this domain no read permission for them.
	//   mcp.test      testing an MCP server: it starts a process on THIS machine. The
	//                 handler keeps its own 「a person's action」 refusals (a worker token
	//                 and a job credential are refused outright); appendix A's draft column
	//                 suggested config.write for it, but 附录 A 的「建议权限」列以正文为准 — the
	//                 catalog's own `mcp.test` row is what the route carries.
	//   update.check  running a release check now. The status read beside it
	//                 (`GET /v1/update`) is Public by design §1: it hands out the checked-in
	//                 version, which the operator's console shows without a permission.
	//
	// The admin group (design §3) carries every WRITE of the domain — config.write,
	// project.write, rules.write, skills.write, worker.manage, worker.upgrade,
	// worker.upgrade.upload and update.apply — and each of them replaces a pre-migration admin
	// test in the handler CELL BY CELL: with require_admin_capability OFF the `users` subject
	// derives the whole group, so every user-class caller keeps today's access, and with the
	// switch ON only the root administrator holds it, which is exactly what that test
	// answered (it required can_admin with the switch on). The one half of it the gate does NOT
	// carry is its caller-kind half: the gate leaves a worker token
	// and a job credential alone (design §2「只对 user 类调用方计算」), so every handler of the
	// group keeps a person-only refusal of its own (adminUserOnly, authz_admin_rules.go).
	//
	// Two entries need a SECOND permission, in both cases decided by the handler because
	// only the request body says whether it applies (InHandler, design §1's row of that
	// permission):
	//
	//   update.apply  POST /v1/update/apply starts the one-click update AND, when its body
	//                 names workers, hands those workers this server's own binary — so such a
	//                 request also needs worker.upgrade. Cancelling the same update (DELETE)
	//                 upgrades nobody: the plain requirement.
	//   worker.upgrade.upload  PUT /v1/workers/{id}/upgrade/file stores the binary a worker
	//                 downloads and executes (危险级, so it is spelled out literally); and
	//                 POST /v1/workers/{id}/upgrade with `source=staged` runs exactly that
	//                 stored binary, which is why the design's `worker.upgrade.upload` row
	//                 reads「上传二进制；`source=staged` 也需要」. The other sources
	//                 (`auto` / `server` / `mirror`) supply nothing through the request —
	//                 `mirror` downloads from the source the administrator configured in
	//                 server.update (gofer-n1vj) — so they need worker.upgrade only.
	//
	// The three worker handshakes are WorkerSelf ANNOTATIONS of the same kind as the four
	// rows of workercredential.go's whitelist: they are registered OUTSIDE the /v1 group (a WS
	// handshake cannot use the JSON error envelope), so the worker middleware — and with it
	// that whitelist — never sees them. They have no user-class surface at all: the handshake
	// authenticates the caller itself, which is why the user side says NoUserSurface.
	"GET /v1/config": {user: Require(authz.PermConfigRead), scope: NoProject()},
	// gofer-x9mb: the notification event catalog is a config read (it describes what can be
	// subscribed), while a test send talks to a configured target — config.write.
	"GET /v1/notify/events":          {user: Require(authz.PermConfigRead), scope: NoProject()},
	"POST /v1/notify/test":           {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"PUT /v1/config/agents/{key}":    {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"DELETE /v1/config/agents/{key}": {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"PUT /v1/config/server":          {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"PUT /v1/config/work":            {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"PUT /v1/config/steward":         {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"POST /v1/config/validate":       {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"POST /v1/config/reload":         {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"GET /v1/mcp-servers":            {user: Require(authz.PermConfigRead), scope: NoProject()},
	"GET /v1/mcp-servers/{name}":     {user: Require(authz.PermConfigRead), scope: NoProject()},
	// Extension settings: reading is seeded to `users`, writing is not. Both are
	// server-global (the keys change the whole deployment), so a restricted caller keeps
	// the read and the ceiling gate refuses its write.
	"GET /v1/settings":                        {user: Require(authz.PermSettingsRead), scope: NoProject()},
	"GET /v1/settings/export":                 {user: Require(authz.PermSettingsRead), scope: NoProject()},
	"GET /v1/settings/{key}":                  {user: Require(authz.PermSettingsRead), scope: NoProject()},
	"PUT /v1/settings/{key}":                  {user: Require(authz.PermSettingsWrite), scope: NoProject()},
	"DELETE /v1/settings/{key}":               {user: Require(authz.PermSettingsWrite), scope: NoProject()},
	"POST /v1/settings/import":                {user: Require(authz.PermSettingsWrite), scope: NoProject()},
	"POST /v1/mcp-servers/{name}/test":        {user: Require(authz.PermMcpTest), scope: NoProject()},
	"GET /v1/projects":                        {user: Public(), scope: Filter()},
	"GET /v1/projects/{key}":                  {user: Public(), scope: PathProject("key")},
	"GET /v1/projects/{key}/file":             {user: Public(), scope: PathProject("key")},
	"GET /v1/projects/{key}/git":              {user: Public(), scope: PathProject("key")},
	"GET /v1/projects/{key}/repos":            {user: Public(), scope: PathProject("key")},
	"GET /v1/projects/{key}/templates":        {user: Public(), scope: PathProject("key")},
	"GET /v1/projects/{key}/templates/{name}": {user: Public(), scope: PathProject("key")},
	"POST /v1/projects":                       {user: Require(authz.PermProjectWrite), scope: BodyProject()},
	"PUT /v1/projects/{key}":                  {user: Require(authz.PermProjectWrite), scope: PathProject("key")},
	"DELETE /v1/projects/{key}":               {user: Require(authz.PermProjectWrite), scope: PathProject("key")},
	"GET /v1/rules":                           {user: Public(), scope: NoProject()},
	"GET /v1/rules/{name}":                    {user: Public(), scope: NoProject()},
	"PUT /v1/rules/{name}":                    {user: Require(authz.PermRulesWrite), scope: NoProject()},
	"DELETE /v1/rules/{name}":                 {user: Require(authz.PermRulesWrite), scope: NoProject()},
	"GET /v1/skills":                          {user: Public(), scope: NoProject()},
	"GET /v1/skills/{name}":                   {user: Public(), scope: NoProject()},
	"GET /v1/skills/{name}/export":            {user: Public(), scope: NoProject()},
	"POST /v1/skills/import":                  {user: Require(authz.PermSkillsWrite), scope: NoProject()},
	"POST /v1/skills/{name}/update":           {user: Require(authz.PermSkillsWrite), scope: NoProject()},
	"DELETE /v1/skills/{name}":                {user: Require(authz.PermSkillsWrite), scope: NoProject()},
	"GET /v1/update":                          {user: Public(), scope: NoProject()},
	"POST /v1/update/check":                   {user: Require(authz.PermUpdateCheck), scope: NoProject()},
	"POST /v1/update/apply":                   {user: InHandler(permDeny, authz.PermUpdateApply, authz.PermWorkerUpgrade), scope: NoProject()},
	"DELETE /v1/update/apply":                 {user: Require(authz.PermUpdateApply), scope: NoProject()},
	// gofer-4uk5: restarting the managed serve is gated like the console's config writes;
	// reading its progress needs only config.read.
	"GET /v1/server/restart":                  {user: Require(authz.PermConfigRead), scope: NoProject()},
	"POST /v1/server/restart":                 {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"DELETE /v1/server/restart":               {user: Require(authz.PermConfigWrite), scope: NoProject()},
	"POST /v1/workers":                        {user: Require(authz.PermWorkerManage), scope: NoProject()},
	"DELETE /v1/workers/{id}":                 {user: Require(authz.PermWorkerManage), scope: Resource("worker")},
	"GET /v1/worker-enrollments":              {user: Require(authz.PermWorkerManage), scope: NoProject()},
	"POST /v1/worker-enrollments/{id}/accept": {user: Require(authz.PermWorkerManage), scope: NoProject()},
	"POST /v1/worker-enrollments/{id}/reject": {user: Require(authz.PermWorkerManage), scope: NoProject()},
	"POST /v1/workers/{id}/reload":            {user: Require(authz.PermWorkerManage), scope: Resource("worker")},
	"GET /v1/workers/{id}":                    {user: Public(), scope: Resource("worker")},
	"GET /v1/workers/{id}/projects":           {user: Public(), scope: Resource("worker")},
	"POST /v1/workers/{id}/upgrade":           {user: InHandler(permDeny, authz.PermWorkerUpgrade, authz.PermWorkerUpgradeUpload), scope: Resource("worker")},
	"PUT /v1/workers/{id}/upgrade/file":       {user: Require(authz.PermWorkerUpgradeUpload), scope: Resource("worker")},
	"GET /v1/workers/connect":                 {user: NoUserSurface(), workerSelf: true, scope: NoProject(), rw: Mutating()},
	"GET /v1/workers/tunnel-connect":          {user: NoUserSurface(), workerSelf: true, scope: NoProject(), rw: Mutating()},
	"GET /v1/workers/pty-connect":             {user: NoUserSurface(), workerSelf: true, scope: NoProject(), rw: Mutating()},

	// ---- plan T1.5i: the rest (agents, runners, meta, stats) ----
	//
	// The last block of the migration, the one that empties the 待迁移 list. Its routes fall in
	// three groups:
	//
	//	Public      the observability reads — the agent list, the runner list
	//	            and a runner's messenger agents, /v1/meta and the two stats reads. Design
	//	            §1's `config.read` row names 「/meta、/agents、/runners 为 Public」 as its own
	//	            exceptions, and 附录 A 的「建议权限」列 gives the stats reads the same; the
	//	            catalog gives the domain no read permission behind them.
	//	Public      POST /v1/stats/backfill: 附录 A gives it Public (「user-only」 was its
	//	            pre-migration CALLER-KIND check), and the handler keeps exactly that check —
	//	            「only a person runs the metrics backfill」 — because the permission model says
	//	            nothing about a non-user caller.
	"GET /v1/agents":  {user: Public(), scope: NoProject()},
	"GET /v1/meta":    {user: Public(), scope: NoProject()},
	"GET /v1/runners": {user: Public(), scope: NoProject()},
	"GET /v1/runners/{name}/messenger/agents": {user: Public(), scope: NoProject()},
	"GET /v1/stats":           {user: Public(), scope: Filter()},
	"GET /v1/stats/overview":  {user: Public(), scope: Filter()},
	"POST /v1/stats/backfill": {user: Public(), scope: NoProject()},

	// ---- plan T2.1: whoami, the permission catalog and grant management ----
	//
	// Three routes are Public: whoami (a caller must be able to see what it holds before it can
	// ask anybody for more), the permission catalog, and the route table — the last one renders
	// exactly what docs/reference/permissions.md holds, so `gofer auth routes` can ask a running
	// binary instead of a checkout. None of the three reveals anything its caller does not
	// already act on, and all three refuse a non-user caller where its own allowlist does.
	//
	// The six management routes carry auth.manage, the catalog's only 根 (root) permission and
	// the one no grant can ever match — internal/authz refuses it in Expand, so neither a `*`
	// grant nor a database row can hand it out. That is what makes 「auth.manage 只看根管理员」
	// true without a second check: the gate asks for a permission only the root holds. It leaves a
	// NON-user caller alone by design (design §2「只对 user 类调用方计算」), so the handlers add
	// the caller-kind half (grantAdminOnly) — without it a job credential, which SEC-01 lets read
	// every GET route, could list the grants.
	"GET /v1/auth/whoami":                {user: Public(), scope: NoProject()},
	"GET /v1/auth/catalog":               {user: Public(), scope: NoProject()},
	"GET /v1/auth/routes":                {user: Public(), scope: NoProject()},
	"GET /v1/auth/expand":                {user: Require(authz.PermAuthManage), scope: NoProject()},
	"GET /v1/auth/grants":                {user: Require(authz.PermAuthManage), scope: NoProject()},
	"POST /v1/auth/grants":               {user: Require(authz.PermAuthManage), scope: NoProject()},
	"DELETE /v1/auth/grants/{id}":        {user: Require(authz.PermAuthManage), scope: NoProject()},
	"POST /v1/auth/grants/{id}/activate": {user: Require(authz.PermAuthManage), scope: NoProject()},
	// renew extends ONLY the deadline of a session grant that is still in force; the handler
	// judges 生效 exactly as the listing does before it lets the store move expires_at.
	"POST /v1/auth/grants/{id}/renew": {user: Require(authz.PermAuthManage), scope: NoProject()},
	"GET /v1/auth/audit":              {user: Require(authz.PermAuthManage), scope: NoProject()},
}

// pendingRoutes is the 待迁移 list of plan T1.4: every route whose user-side declaration,
// handler check and reverse test were still to be written by T1.5a–i. A route leaves this
// list in exactly the sub-task that declares it (design 「覆盖测试」: every route is either
// declared or on this list — a route in neither makes buildRouter fail closed).
//
// It is EMPTY, and that is the state T1.5i is finished in: every domain sub-task wrote the
// real category of its routes, moved their handler checks onto the rules of authz_*_rules.go
// and deleted its own lines here. The map itself stays, and so does UserPending, so that
// wrapRoutes and the coverage test keep answering the question they are for — a route that is
// in NEITHER table fails closed (a domain added later cannot grow an undeclared route).
var pendingRoutes = map[string]bool{}

// authzGate wraps ONE route with its declaration. It runs after the auth middleware (it
// needs the resolved caller kind) and before the handler, and it is the only place a
// route-level requirement is enforced:
//
//   - a non-user caller (a worker token, a job credential) is untouched: those are
//     confined by workercredential.go's whitelist and jobcredential.go's SEC-01 tables,
//     which the design keeps authoritative;
//   - userPublic / userInHandler / userNone / userPending ask nothing at the gate —
//     Public has no requirement of its own, InHandler leaves the decision to the handler,
//     NoUserSurface has no user-class surface, and a 待迁移 route is exactly as it was
//     before T1.4.
//
// A route registered OUTSIDE the /v1 group (the WS handshakes, /health, /metrics, the
// schedule webhook) never passes the auth middleware, so callerKindFromCtx answers `user`
// for it with an empty caller id: such a route authenticates itself and is declared
// NoUserSurface + External, never Public or Require.
func (s *Server) authzGate(decl routeDecl) rux.HandlerFunc {
	return func(c *rux.Context) {
		if callerKindFromCtx(c) == callerKindJob {
			// A job credential inherits the ceiling of its job's owner (S5). The SEC-01
			// tables stay the authority on WHAT it may call; this only narrows WHERE.
			ceil, err := s.jobCredentialCeiling(c)
			if err != nil {
				writeError(c, http.StatusForbidden, "caller ceiling", err.Error())
				c.Abort()
				return
			}
			if ceil != nil && s.ceilingGate(c, ceil, decl) {
				c.Abort()
				return
			}
			c.Next()
			return
		}
		if callerKindFromCtx(c) != callerKindUser {
			c.Next()
			return
		}
		if ceil := s.ceilingOf(c); ceil != nil && s.ceilingGate(c, ceil, decl) {
			c.Abort()
			return
		}
		if decl.user.kind == userRequire && !s.can(c, decl.user.perms...) {
			s.writePermissionDenied(c, decl.user.perms...)
			c.Abort()
			return
		}
		c.Next()
	}
}

// wrapRoutes binds every registered route to its declaration, per route. It is called at
// the end of buildRouter, so it covers the conditional routes (/metrics, the WS
// handshake paths, the presence routes) as well.
//
// A route in neither table is a programming error — a route with no authz decision at
// all, which is what the design's coverage test forbids — and buildRouter refuses to
// build such a router (fail closed): the server would otherwise start with an
// undeclared, unguarded route. The coverage test in authz_routes_test.go asserts the same
// invariant from the outside.
func (s *Server) wrapRoutes(r *rux.Router) {
	var undeclared []string
	r.IterateRoutes(func(rt *rux.Route) {
		for _, method := range rt.Methods() {
			key := canonicalRouteKey(method, rt.Path())
			decl, declared := routeDecls[key]
			if !declared && !pendingRoutes[key] {
				undeclared = append(undeclared, key)
				continue
			}
			decl.key = key
			rt.Use(s.authzGate(decl))
		}
	})
	if len(undeclared) > 0 {
		panic("httpapi: route(s) without a permission declaration: " + strings.Join(undeclared, ", ") +
			" — declare each one in routeDecls, or list it in pendingRoutes while its migration is still pending (authz_routes.go)")
	}
}

// canonicalRouteKey renders a registered route as the key the declaration tables use:
// `<METHOD> <path>`, every parameter segment written `{name}`. rux rewrites a registered
// `{id}` to `:id` and a catch-all `{name:.+}` to `*name`, so both spellings collapse back
// to the `{name}` form the tables are written in.
func canonicalRouteKey(method, path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segs {
		if len(seg) > 1 && (seg[0] == ':' || seg[0] == '*') {
			segs[i] = "{" + seg[1:] + "}"
		}
	}
	return method + " /" + strings.Join(segs, "/")
}
