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
}

// routeDecls is the declaration of every route that has one, keyed by the canonical route
// key (`<METHOD> <path with {param}>`, see canonicalRouteKey). A route that is not here
// must be on pendingRoutes.
//
// A declared route can STILL be on pendingRoutes, and exactly four are: the SEC-01
// snapshot forces their JobCred annotation to be written now, while the design names a
// plain permission for their user side (the two tracker routes: tracker.sync; PUT
// /v1/steward/notes and POST /v1/steward/review-summary: steward.manage). A plain
// permission is a Require declaration, and Require is the one category the gate ENFORCES —
// which T1.4 must not do, because this task adds no new requirement. So their user side says
// UserPending and their domain task (T1.5g for the trackers and the steward) turns it into
// the real category. POST /v1/jobs was the fifth of that list and has been a Require in the
// table since T1.5a migrated the job domain.
var routeDecls = map[string]routeDecl{
	"POST /v1/jobs/{id}/comments":                      {user: Public(), jobCred: "POST /v1/jobs/*/comments"},
	"POST /v1/plans/{id}/comments":                     {user: Public(), jobCred: "POST /v1/plans/*/comments"},
	"PUT /v1/plans/{id}/handoff":                       {user: InHandler(permDeny, authz.PermPlanManageOthers), jobCred: "PUT /v1/plans/*/handoff"},
	"POST /v1/plans/{id}/todos/{todo_id}/comments":     {user: Public(), jobCred: "POST /v1/plans/*/todos/*/comments"},
	"POST /v1/todos/{todo_id}/comments":                {user: Public(), jobCred: "POST /v1/todos/*/comments"},
	"POST /v1/decisions":                               {user: Public(), jobCred: "POST /v1/decisions"},
	"POST /v1/jobs/{id}/wakeups":                       {user: InHandler(permDeny, authz.PermJobWakeOthers, authz.PermJobSubmit), jobCred: "POST /v1/jobs/*/wakeups"},
	"POST /v1/jobs/{id}/say":                           {user: InHandler(permDeny, authz.PermJobManageOthers), jobCred: "POST /v1/jobs/*/say"},
	"POST /v1/jobs/{id}/end":                           {user: InHandler(permDeny, authz.PermJobManageOthers), jobCred: "POST /v1/jobs/*/end"},
	"POST /v1/sessions/{sid}/watches":                  {user: InHandler(permDeny, authz.PermSessionReplyOthers), jobCred: "POST /v1/sessions/*/watches"},
	"POST /v1/work-items/{id}/report":                  {user: Public(), jobCred: "POST /v1/work-items/*/report"},
	"PATCH /v1/todos/{todo_id}":                        {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermJobSubmit), jobCred: "PATCH /v1/todos/*"},
	"PATCH /v1/jobs/{id}":                              {user: InHandler(permDeny, authz.PermJobManageOthers), jobCred: "PATCH /v1/jobs/*"},
	"POST /v1/jobs":                                    {user: Require(authz.PermJobSubmit), jobCred: "POST /v1/jobs"},
	"POST /v1/tracker/sync":                            {user: UserPending(), jobCred: "POST /v1/tracker/sync"},
	"POST /v1/tracker/repos/{tracker_id}/rename":       {user: UserPending(), jobCred: "POST /v1/tracker/repos/*/rename"},
	"POST /v1/memories/{scope}/{scope_key}/{key}/flag": {user: Public(), jobCred: "POST /v1/memories/*/*/*/flag"},
	"GET /v1/work-items":                               {user: Public(), jobCred: "GET /v1/work-items"},
	"GET /v1/work-items/{id}":                          {user: Public(), jobCred: "GET /v1/work-items/*"},
	"GET /v1/work-items/{id}/journal":                  {user: Public(), jobCred: "GET /v1/work-items/*/journal"},
	"GET /v1/work-items/{id}/sessions":                 {user: Public(), jobCred: "GET /v1/work-items/*/sessions"},
	"GET /v1/work-items/{id}/links":                    {user: Public(), jobCred: "GET /v1/work-items/*/links"},
	"GET /v1/work-items/{id}/requests":                 {user: Public(), jobCred: "GET /v1/work-items/*/requests"},
	"GET /v1/work-items/requests":                      {user: Public(), jobCred: "GET /v1/work-items/requests"},
	"GET /v1/work-items/merge-suggestions":             {user: Public(), jobCred: "GET /v1/work-items/merge-suggestions"},
	"GET /v1/sessions":                                 {user: InHandler(permFilter, authz.PermSessionReadOthers), jobCred: "GET /v1/sessions"},
	"GET /v1/sessions/{sid}":                           {user: InHandler(permDeny, authz.PermSessionReadOthers), jobCred: "GET /v1/sessions/*"},
	"GET /v1/sessions/{sid}/tail":                      {user: InHandler(permDeny, authz.PermSessionReadOthers), jobCred: "GET /v1/sessions/*/tail"},
	"GET /v1/jobs":                                     {user: InHandler(permFilter, authz.PermJobReadOthers), jobCred: "GET /v1/jobs"},
	"GET /v1/jobs/{id}":                                {user: InHandler(permDeny, authz.PermJobReadOthers), jobCred: "GET /v1/jobs/*"},
	"GET /v1/steward":                                  {user: Public(), jobCred: "GET /v1/steward"},
	"GET /v1/steward/notes":                            {user: Public(), jobCred: "GET /v1/steward/notes"},
	"GET /v1/issues":                                   {user: Public(), jobCred: "GET /v1/issues"},
	"GET /v1/issues/{id}":                              {user: Public(), jobCred: "GET /v1/issues/*"},
	"GET /v1/today":                                    {user: Public(), jobCred: "GET /v1/today"},
	"GET /v1/memory-findings":                          {user: Public(), jobCred: "GET /v1/memory-findings"},
	"GET /v1/memory-suggestions":                       {user: Public(), jobCred: "GET /v1/memory-suggestions"},
	"PATCH /v1/work-items/{id}":                        {user: Public(), jobCred: "PATCH /v1/work-items/*"},
	"POST /v1/work-items/{id}/journal":                 {user: Public(), jobCred: "POST /v1/work-items/*/journal"},
	"POST /v1/work-items/{id}/report-request":          {user: InHandler(permDeny, authz.PermSessionReplyOthers), jobCred: "POST /v1/work-items/*/report-request"},
	"POST /v1/work-items/{id}/summarize":               {user: Public(), jobCred: "POST /v1/work-items/*/summarize"},
	"POST /v1/work-items/{id}/merge-suggestions":       {user: Public(), jobCred: "POST /v1/work-items/*/merge-suggestions"},
	"POST /v1/session-ask":                             {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobManageOthers), jobCred: "POST /v1/session-ask"},
	"PUT /v1/steward/notes":                            {user: UserPending(), jobCred: "PUT /v1/steward/notes"},
	"POST /v1/steward/review-summary":                  {user: UserPending(), jobCred: "POST /v1/steward/review-summary"},
	"POST /v1/today/advice":                            {user: Public(), jobCred: "POST /v1/today/advice"},
	"POST /v1/memory-suggestions":                      {user: Public(), jobCred: "POST /v1/memory-suggestions"},
	"GET /v1/workers/{id}/assignable":                  {user: Public(), workerSelf: true},
	"GET /v1/workers/{id}/upgrade/file":                {user: NoUserSurface(), workerSelf: true},
	"GET /v1/xfer/{id}/content":                        {user: InHandler(permDeny, authz.PermXferManageOthers), workerSelf: true},
	"PUT /v1/xfer/{id}/content":                        {user: NoUserSurface(), workerSelf: true},
	"GET /health":                                      {user: NoUserSurface(), external: "none"},
	"GET /metrics":                                     {user: NoUserSurface(), external: "metrics.token"},
	"GET /v1/jobs/{id}/attach":                         {user: NoUserSurface(), external: "attach ticket"},
	"GET /v1/ws":                                       {user: NoUserSurface(), external: "ws ticket"},
	"POST /v1/schedules/{id}/trigger":                  {user: NoUserSurface(), external: "trigger_token"},
	"POST /v1/push/actions":                            {user: NoUserSurface(), external: "push action token"},

	// The web console's SPA fallback (Static): rux serves it through NotFound rather than
	// through a route, so it never appears in the router's route list and the coverage test
	// skips a static declaration in the reverse direction. It authenticates nothing and
	// asks nothing of the caller: it hands out index.html and the bundled assets.
	"GET /*": {user: NoUserSurface(), static: true},

	// ---- plan T1.5a: the job domain (jobs, workflows, retries, wakeups) ----
	//
	// job.submit is the plain permission of every entry point that starts work (the
	// gate enforces it: Require). Every other job route is InHandler because the answer
	// depends on the resource — 「操作自己的资源不需要这类权限」 — and the handler applies it
	// through the rules of authz_job_rules.go. The job and plan lists
	// (GET /v1/jobs, GET /v1/jobs/{id}, PATCH /v1/jobs/{id}) are declared above, next to the
	// routes whose SEC-01 annotation forced them into the first table.
	"POST /v1/agents/{id}/probe":                {user: Require(authz.PermJobSubmit)},
	"POST /v1/workflows":                        {user: Require(authz.PermJobSubmit)},
	"POST /v1/jobs/{id}/accept":                 {user: Require(authz.PermJobReview)},
	"POST /v1/jobs/{id}/reject":                 {user: Require(authz.PermJobReview)},
	"POST /v1/jobs/{id}/approve":                {user: Require(authz.PermJobReview)},
	"GET /v1/jobs/{id}/request":                 {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/logs/stdout":             {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/logs/stderr":             {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/stream":                  {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/acp/stream":              {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/events":                  {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/deliveries":              {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/comments":                {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/artifacts":               {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/artifacts/{name}":        {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/diff":                    {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/worktree":                {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/wakeups":                 {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/wakeups/{wid}":                     {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/jobs/{id}/retries":                 {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/workflows":                         {user: InHandler(permFilter, authz.PermJobReadOthers)},
	"GET /v1/workflows/{id}":                    {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/workflows/{id}/events":             {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/workflows/{id}/export":             {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"GET /v1/workflow-templates":                {user: Public()},
	"GET /v1/workflow-templates/{name}":         {user: Public()},
	"POST /v1/workflow-templates/{name}/render": {user: Public()},
	"POST /v1/jobs/secret-scan":                 {user: InHandler(permFilter, authz.PermSecretScanAll)},
	"POST /v1/jobs/{id}/findings/issue":         {user: Public()},
	// attach: someone else's (or an ownerless) job needs job.attach.others, the
	// caller's OWN job the attach gate job.attach (design §1 / row 38, 54, 55).
	"POST /v1/jobs/{id}/attach-ticket":  {user: InHandler(permDeny, authz.PermJobAttachOthers, authz.PermJobAttach)},
	"GET /v1/jobs/{id}/pty/recording":   {user: InHandler(permDeny, authz.PermJobAttachOthers, authz.PermJobAttach)},
	"GET /v1/jobs/{id}/pty/sessions":    {user: InHandler(permDeny, authz.PermJobAttachOthers, authz.PermJobAttach)},
	"GET /v1/pty/sessions":              {user: InHandler(permFilter, authz.PermJobAttachOthers)},
	"POST /v1/jobs/{id}/cancel":         {user: InHandler(permDeny, authz.PermJobCancelOthers)},
	"POST /v1/workflows/{id}/cancel":    {user: InHandler(permDeny, authz.PermJobCancelOthers)},
	"DELETE /v1/retries/{rid}":          {user: InHandler(permDeny, authz.PermJobCancelOthers)},
	"DELETE /v1/jobs/{id}":              {user: InHandler(permDeny, authz.PermJobDeleteOthers)},
	"POST /v1/jobs/{id}/redact":         {user: InHandler(permDeny, authz.PermJobDeleteOthers)},
	"POST /v1/jobs/{id}/resume":         {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit)},
	"POST /v1/jobs/{id}/rebuild":        {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit)},
	"POST /v1/workflows/{id}/pick":      {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermJobSubmit)},
	"POST /v1/jobs/{id}/deliver":        {user: InHandler(permDeny, authz.PermJobManageOthers)},
	"DELETE /v1/jobs/{id}/worktree":     {user: InHandler(permDeny, authz.PermJobManageOthers)},
	"POST /v1/jobs/{id}/worktree/merge": {user: InHandler(permDeny, authz.PermJobManageOthers)},
	"PATCH /v1/wakeups/{wid}":           {user: InHandler(permDeny, authz.PermJobWakeOthers, authz.PermJobSubmit)},
	"DELETE /v1/wakeups/{wid}":          {user: InHandler(permDeny, authz.PermJobWakeOthers)},

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
	"POST /v1/jobs/{id}/interactions":                         {user: InHandler(permDeny, authz.PermJobManageOthers)},
	"GET /v1/jobs/{id}/interactions":                          {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"POST /v1/jobs/{id}/interactions/{interaction_id}/answer": {user: Require(authz.PermInteractionAnswer)},
	"POST /v1/jobs/{id}/interactions/{interaction_id}/punt":   {user: Require(authz.PermInteractionAnswer)},
	"GET /v1/interactions":                                    {user: InHandler(permFilter, authz.PermJobReadOthers)},
	"POST /v1/decisions/{id}/answer":                          {user: InHandler(permDeny, authz.PermInteractionAnswer, authz.PermSessionReplyOthers)},
	"GET /v1/decisions":                                       {user: Public()},
	"GET /v1/decisions/{id}":                                  {user: Public()},
	"GET /v1/push/vapid-public-key":                           {user: Public()},
	"GET /v1/push/subscriptions":                              {user: Public()},
	"POST /v1/push/subscriptions":                             {user: Public()},
	"DELETE /v1/push/subscriptions":                           {user: Public()},
	"POST /v1/push/test":                                      {user: Public()},
	"POST /v1/ws-ticket":                                      {user: Public()},

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
	"POST /v1/sessions":                                   {user: Public()},
	"POST /v1/sessions/{sid}/heartbeat":                   {user: Public()},
	"POST /v1/sessions/{sid}/replies":                     {user: Public()},
	"POST /v1/sessions/{sid}/owner":                       {user: Require(authz.PermSessionTransfer)},
	"GET /v1/sessions/{sid}/messages":                     {user: InHandler(permDeny, authz.PermSessionReadOthers)},
	"GET /v1/sessions/{sid}/nudges":                       {user: InHandler(permDeny, authz.PermSessionReadOthers)},
	"GET /v1/sessions/{sid}/takeover-plan":                {user: InHandler(permDeny, authz.PermSessionReadOthers)},
	"GET /v1/sessions/{sid}/turns/{id}":                   {user: InHandler(permDeny, authz.PermSessionReadOthers)},
	"GET /v1/sessions/{sid}/watches":                      {user: InHandler(permDeny, authz.PermSessionReadOthers)},
	"GET /v1/nudges":                                      {user: InHandler(permFilter, authz.PermSessionReadOthers)},
	"POST /v1/sessions/{sid}/messages":                    {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"GET /v1/sessions/{sid}/outbox":                       {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"POST /v1/sessions/{sid}/say":                         {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"POST /v1/sessions/{sid}/deliver":                     {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobSubmit)},
	"POST /v1/sessions/{sid}/resume":                      {user: InHandler(permDeny, authz.PermSessionReplyOthers, authz.PermJobSubmit)},
	"POST /v1/sessions/{sid}/relay":                       {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"POST /v1/sessions/{sid}/nudges":                      {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"POST /v1/sessions/{sid}/watches/complete":            {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"DELETE /v1/sessions/{sid}/watches/{job_id}":          {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"POST /v1/sessions/{sid}/turns/{id}/ack":              {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"DELETE /v1/sessions/{sid}/turns/{id}/ack":            {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"POST /v1/sessions/{sid}/turns/{id}/complete-watches": {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"PATCH /v1/nudges/{id}":                               {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"DELETE /v1/nudges/{id}":                              {user: InHandler(permDeny, authz.PermSessionReplyOthers)},
	"DELETE /v1/sessions/{sid}":                           {user: InHandler(permDeny, authz.PermSessionManageOthers)},
	"POST /v1/sessions/{sid}/turns":                       {user: InHandler(permDeny, authz.PermSessionManageOthers)},
	"POST /v1/sessions/{sid}/turns/{id}/release":          {user: InHandler(permDeny, authz.PermSessionManageOthers)},
	"POST /v1/sessions/{sid}/release-takeover":            {user: InHandler(permDeny, authz.PermSessionManageOthers)},
	"POST /v1/sessions/{sid}/permissions/{id}/answer":     {user: InHandler(permDeny, authz.PermSessionPermissionAnswer)},
	"POST /v1/sessions/{sid}/permissions":                 {user: Public()},
	"POST /v1/sessions/{sid}/permissions/resolve":         {user: Public()},

	// The workbench routes (design rows 272–279). The layout, the thread preferences and the
	// seen-all marker only ever touch the CALLER's own preference rows, so they ask nothing (a
	// worker token and a job credential keep the handler's user-only refusal). The listing and
	// the diff are job.read.others — its filter form for the list, its deny form for one
	// thread — and review / turn are 对会话 job 说话, hence job.manage.others; a RELAY thread turn
	// is speech into a terminal session instead, session.reply.others.
	"GET /v1/workbench/layout":               {user: Public()},
	"PUT /v1/workbench/layout":               {user: Public()},
	"POST /v1/workbench/threads/seen-all":    {user: Public()},
	"PATCH /v1/workbench/threads/{id}":       {user: Public()},
	"GET /v1/workbench/threads":              {user: InHandler(permFilter, authz.PermJobReadOthers)},
	"GET /v1/workbench/threads/{id}/diff":    {user: InHandler(permDeny, authz.PermJobReadOthers)},
	"POST /v1/workbench/threads/{id}/review": {user: InHandler(permDeny, authz.PermJobManageOthers)},
	"POST /v1/workbench/threads/{id}/turn":   {user: InHandler(permDeny, authz.PermJobManageOthers, authz.PermSessionReplyOthers, authz.PermJobSubmit)},

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
	"PATCH /v1/plans/{id}":              {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermPlanSuperviseOthers, authz.PermJobSubmit)},
	"POST /v1/plans/{id}/jobs":          {user: InHandler(permDeny, authz.PermPlanManageOthers)},
	"POST /v1/plans/{id}/pause":         {user: InHandler(permDeny, authz.PermPlanManageOthers)},
	"POST /v1/plans/{id}/resume":        {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermJobSubmit)},
	"POST /v1/plans/{id}/run":           {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermJobSubmit)},
	"POST /v1/plans/{id}/todos":         {user: InHandler(permDeny, authz.PermPlanManageOthers)},
	"POST /v1/todos/{todo_id}/dispatch": {user: InHandler(permDeny, authz.PermPlanManageOthers, authz.PermJobSubmit)},
	"POST /v1/work-items/{id}/to-todo":  {user: InHandler(permDeny, authz.PermPlanManageOthers)},
	// The reads (Public), the two supervisor reads beside the create route, and the domain's
	// SEC-01-annotated entries whose user side was already Public in the first table above.
	"POST /v1/plans":                                       {user: Public()},
	"GET /v1/plans":                                        {user: Public()},
	"GET /v1/plans/{id}":                                   {user: Public()},
	"GET /v1/plans/{id}/events":                            {user: Public()},
	"GET /v1/plans/{id}/comments":                          {user: Public()},
	"GET /v1/plans/{id}/todos/{todo_id}/comments":          {user: Public()},
	"GET /v1/plans/{id}/handoff":                           {user: Public()},
	"GET /v1/plans/{id}/handoff/history":                   {user: Public()},
	"GET /v1/todos/{todo_id}/comments":                     {user: Public()},
	"POST /v1/work-items":                                  {user: Public()},
	"POST /v1/work-items/delete":                           {user: Public()},
	"DELETE /v1/work-items/{id}":                           {user: Public()},
	"POST /v1/work-items/{id}/links":                       {user: Public()},
	"DELETE /v1/work-items/{id}/links":                     {user: Public()},
	"POST /v1/work-items/{id}/merge":                       {user: Public()},
	"POST /v1/work-items/{id}/split":                       {user: Public()},
	"POST /v1/work-items/{id}/sessions":                    {user: Public()},
	"DELETE /v1/work-items/{id}/sessions/{sid}":            {user: Public()},
	"POST /v1/work-items/{id}/suggestions/{field}/accept":  {user: Public()},
	"POST /v1/work-items/{id}/suggestions/{field}/dismiss": {user: Public()},
	"GET /v1/work-items/digest":                            {user: Public()},
	"POST /v1/work-items/digest":                           {user: Public()},
	"GET /v1/work-items/summarizer":                        {user: Public()},
	"POST /v1/work-items/merge-suggestions/{n}/accept":     {user: Public()},
	"POST /v1/work-items/merge-suggestions/{n}/dismiss":    {user: Public()},
	// The dashboard over plans, todos and jobs (Today): it reads and writes only the calling
	// person's own view rows, so every route asks nothing of a user-class caller.
	"POST /v1/today/actions":        {user: Public()},
	"GET /v1/today/handled":         {user: Public()},
	"GET /v1/today/lanes":           {user: Public()},
	"POST /v1/today/snooze":         {user: Public()},
	"DELETE /v1/today/snooze/{key}": {user: Public()},
	"GET /v1/today/snoozed":         {user: Public()},
}

// pendingRoutes is the 待迁移 list of plan T1.4: every route whose user-side declaration,
// handler check and reverse test are still to be written by T1.5a–i. A route leaves this
// list in exactly the sub-task that declares it (design 「覆盖测试」: every route is either
// declared or on this list — a route in neither makes buildRouter fail closed).
//
// The groups below are the plan's domain sub-tasks, so a task can delete its own lines.
var pendingRoutes = map[string]bool{
	// T1.5b interactions / decisions / push — MIGRATED: their declarations are in
	// routeDecls (the T1.5b block) and their reverse cases in authz_inhandler_test.go.
	// T1.5c terminal sessions, nudges, workbench — MIGRATED: their declarations are in
	// routeDecls (the T1.5c block) and their reverse cases in authz_inhandler_test.go.
	// T1.5d plans / todos / work items / today — MIGRATED: their declarations are in
	// routeDecls (the T1.5d block) and their reverse cases in authz_inhandler_test.go.
	// T1.5e schedules
	"POST /v1/schedules":                   true,
	"GET /v1/schedules":                    true,
	"GET /v1/schedules/{id}":               true,
	"DELETE /v1/schedules/{id}":            true,
	"POST /v1/schedules/{id}/enable":       true,
	"POST /v1/schedules/{id}/disable":      true,
	"POST /v1/schedules/{id}/run-now":      true,
	"POST /v1/schedules/{id}/rotate-token": true,
	// T1.5f tunnels
	"GET /v1/tunnels/connect":               true,
	"GET /v1/tunnels":                       true,
	"GET /v1/tunnels/forwarders":            true,
	"POST /v1/tunnels/forwarders":           true,
	"PUT /v1/tunnels/forwarders/{id}":       true,
	"DELETE /v1/tunnels/forwarders/{id}":    true,
	"POST /v1/tunnels/forwarders/{id}/stop": true,
	"POST /v1/tunnels/hosted/{name}":        true,
	"DELETE /v1/tunnels/hosted/{name}":      true,
	"GET /v1/tunnels/presets":               true,
	"GET /v1/tunnels/local-presets":         true,
	"POST /v1/tunnels/local-presets/{name}": true,
	"GET /v1/tunnels/presets/{name}":        true,
	"PUT /v1/tunnels/presets/{name}":        true,
	"DELETE /v1/tunnels/presets/{name}":     true,
	// T1.5g xfer / tracker / memories / steward / issues
	"POST /v1/steward/start":                             true,
	"POST /v1/steward/stop":                              true,
	"POST /v1/steward/restart":                           true,
	"POST /v1/steward/ask":                               true,
	"POST /v1/steward/review":                            true,
	"PUT /v1/steward/notes":                              true,
	"POST /v1/steward/review-summary":                    true,
	"POST /v1/tracker/sync":                              true,
	"GET /v1/tracker/issues":                             true,
	"GET /v1/tracker/repos":                              true,
	"GET /v1/tracker/repos/{tracker_id}/clients":         true,
	"POST /v1/tracker/repos/{tracker_id}/sync":           true,
	"POST /v1/tracker/repos/{tracker_id}/rename":         true,
	"DELETE /v1/tracker/repos/{tracker_id}":              true,
	"POST /v1/tracker/issues/batch":                      true,
	"GET /v1/tracker/issues/{id}":                        true,
	"PUT /v1/tracker/issues/{id}":                        true,
	"POST /v1/tracker/issues/{id}/comments":              true,
	"GET /v1/tracker/memories":                           true,
	"PUT /v1/tracker/memories/{id}":                      true,
	"DELETE /v1/tracker/memories/{id}":                   true,
	"GET /v1/memories":                                   true,
	"POST /v1/memories":                                  true,
	"GET /v1/memories/{scope}/{scope_key}/{key}":         true,
	"PUT /v1/memories/{scope}/{scope_key}/{key}":         true,
	"DELETE /v1/memories/{scope}/{scope_key}/{key}":      true,
	"DELETE /v1/memories/{scope}/{scope_key}/{key}/flag": true,
	"POST /v1/xfer":                                      true,
	"POST /v1/xfer/precheck":                             true,
	"GET /v1/xfer":                                       true,
	"GET /v1/xfer/{id}":                                  true,
	"DELETE /v1/xfer/{id}":                               true,
	"POST /v1/memory-suggestions/{n}/adopt":              true,
	"POST /v1/memory-suggestions/{n}/dismiss":            true,
	"GET /v1/memory-candidates":                          true,
	"POST /v1/memory-candidates/{n}/accept":              true,
	"POST /v1/memory-candidates/{n}/reject":              true,
	// T1.5h config / projects / rules / skills / mcp / workers / update
	"GET /v1/workers/connect":                 true,
	"GET /v1/workers/tunnel-connect":          true,
	"GET /v1/workers/pty-connect":             true,
	"GET /v1/config":                          true,
	"PUT /v1/config/agents/{key}":             true,
	"DELETE /v1/config/agents/{key}":          true,
	"PUT /v1/config/server":                   true,
	"PUT /v1/config/work":                     true,
	"PUT /v1/config/steward":                  true,
	"POST /v1/config/validate":                true,
	"POST /v1/config/reload":                  true,
	"GET /v1/projects":                        true,
	"GET /v1/update":                          true,
	"POST /v1/update/check":                   true,
	"POST /v1/update/apply":                   true,
	"DELETE /v1/update/apply":                 true,
	"POST /v1/projects":                       true,
	"GET /v1/projects/{key}":                  true,
	"PUT /v1/projects/{key}":                  true,
	"DELETE /v1/projects/{key}":               true,
	"GET /v1/projects/{key}/git":              true,
	"GET /v1/projects/{key}/repos":            true,
	"GET /v1/projects/{key}/file":             true,
	"GET /v1/projects/{key}/templates":        true,
	"GET /v1/projects/{key}/templates/{name}": true,
	"GET /v1/skills":                          true,
	"GET /v1/mcp-servers":                     true,
	"GET /v1/mcp-servers/{name}":              true,
	"POST /v1/mcp-servers/{name}/test":        true,
	"POST /v1/skills/import":                  true,
	"GET /v1/skills/{name}":                   true,
	"POST /v1/skills/{name}/update":           true,
	"DELETE /v1/skills/{name}":                true,
	"GET /v1/skills/{name}/export":            true,
	"GET /v1/rules":                           true,
	"GET /v1/rules/{name}":                    true,
	"PUT /v1/rules/{name}":                    true,
	"DELETE /v1/rules/{name}":                 true,
	"GET /v1/workers/{id}":                    true,
	"GET /v1/workers/{id}/projects":           true,
	"POST /v1/workers/{id}/reload":            true,
	"POST /v1/workers/{id}/upgrade":           true,
	"PUT /v1/workers/{id}/upgrade/file":       true,
	"POST /v1/workers":                        true,
	"DELETE /v1/workers/{id}":                 true,
	// T1.5i the rest (health, metrics, meta, stats, runners, agents, messages)
	"GET /v1/agents":                          true,
	"GET /v1/runners":                         true,
	"GET /v1/runners/{name}/messenger/agents": true,
	"GET /v1/meta":                            true,
	"GET /v1/stats":                           true,
	"GET /v1/stats/overview":                  true,
	"POST /v1/stats/backfill":                 true,
	"POST /v1/agents/register":                true,
	"GET /v1/agents/presence":                 true,
	"POST /v1/agents/{id}/inbox/poll":         true,
	"GET /v1/agents/{id}/inbox":               true,
	"POST /v1/messages":                       true,
	"POST /v1/agents/{id}/deregister":         true,
}

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
		if callerKindFromCtx(c) != callerKindUser {
			c.Next()
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
