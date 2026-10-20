// 长连接按依赖撤销（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md
// §6「长连接的撤销」，计划 T2.3）。
//
// A long-lived connection — a tunnel splice, a hosted forwarder, an attach relay, an SSE stream
// and the browser push channel — is judged ONCE, when it is opened. Its permissions can go away
// while it is still open: a grant is revoked or reaches its expiry, the session it was granted to
// ends or comes back as a new incarnation, or a configuration reload drops the caller, changes its
// token or removes the capability bit that derived the permission. The table below is how such a
// connection can be re-judged and dropped.
//
// One entry per connection, keyed by the caller plus the permissions it rests on:
//
//   - caller, session: the identity the connection was opened with, so the re-judgment resolves
//     the SAME effective permissions the connect time did (a `session:<sid>` grant only applies to
//     the session the request named, design §4);
//   - tokenFingerprint: the token it authenticated with, so a reload that changes the caller's
//     token (or removes the caller) ends the connection instead of leaving it authenticated by a
//     credential that no longer exists;
//   - perms: the permissions the connection NEEDS (an empty set means "it rests on nothing but its
//     identity" — the caller's own job needs no permission, 设计 §1「操作自己的资源不需要这类权限」);
//   - deps: the database grants the decision rested on at connect time — the dependency set of the
//     design (「解析结果附带『依赖的授权 id 集合』；长连接按调用方 id + 依赖集合登记」). It is
//     recorded for the operator's benefit (the log line names the grants) and for the re-judgment:
//     a permission granted BOTH by the configuration and by a grant stays allowed after that grant
//     is revoked, because the re-judgment asks the live resolver, not the dependency set.
//
// Re-judging is the rule, not the diff (「断开时重新判定一次：仍然有权限的连接保留」): every trigger
// below runs recheckLiveConns, which asks the live permission model again and closes only the
// connections whose permissions really went away.
//
// Triggers:
//
//   - a grant write this process performs (revoke / activate: auth_handler.go) and a session
//     hand-over (session_owner_handler.go) re-judge at once;
//   - a configuration reload (SetServerConfig) re-judges at once — the config-derived grants have
//     no stable id, which is why the whole caller is compared by re-resolving;
//   - everything that no request announces — an expiry, a session ending or being taken over after
//     it ended, another process's write — is caught by the periodic sweep (ensureLiveConnSweeper).

package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/pushhub"
)

// liveConnKind names what is registered, for the log line and for the key of a connection that
// must be reachable by name (a hosted forwarder, which its stop route takes away).
type liveConnKind string

const (
	liveConnTunnel  liveConnKind = "tunnel"
	liveConnHosted  liveConnKind = "hosted-forward"
	liveConnAttach  liveConnKind = "attach"
	liveConnSSE     liveConnKind = "sse"
	liveConnWS      liveConnKind = "ws"
	liveConnHostedT liveConnKind = "hosted-dial"
)

// defaultLiveConnSweepInterval is how often the periodic re-judgment runs. It bounds how long a
// connection can outlive a trigger nobody announces (an expiry, a session ending, another
// process's revocation, which itself shows up within the 5s grant-cache TTL). A connection is
// dropped at most this long after its permission went away — the same read-time judgment the
// session grants follow (design §4), applied to the connections that rest on them.
const defaultLiveConnSweepInterval = 10 * time.Second

// liveConn is ONE registered long-lived connection.
type liveConn struct {
	// key is the registry key: kind/<n> for an anonymous connection, a stable name for one that
	// has to be reachable (hostedKey).
	key string
	// kind and resource describe the connection for the log line.
	kind     liveConnKind
	resource string
	// caller, callerKind and session are the identity the connection was opened with.
	caller     string
	callerKind string
	session    string
	// tokenFP is the fingerprint of the bearer token it authenticated with (empty for a
	// connection that has none: a hosted forwarder restored from a legacy autostart preset).
	tokenFP string
	// perms is what the connection needs; deps are the database grants the connect-time decision
	// rested on (design §6's dependency set).
	perms []string
	deps  []string
	// projects re-evaluates the projects of the connection's RESOURCE at re-judgment time (the
	// job of an SSE stream or an attach relay), so a ceiling cannot be outrun by opening the
	// connection first. ok=false means the resource names no project at all (a tunnel, a hosted
	// forwarder: 设计「没有项目的资源…视为范围外」) or cannot be resolved any more. nil means the
	// connection is not judged by a resource project (the push channel, which is scoped per
	// topic and per frame by pushhub instead).
	projects func() ([]string, bool)
	// write marks a connection that carries state changes: a tunnel, a hosted dial, a hosted
	// forwarder and a write-mode attach relay (its pty lease types into the job). A read-only
	// ceiling ends such a connection (设计「只读」).
	write bool
	// ceilingFP is the fingerprint of the ceiling that was in force when a connection whose range is
	// PINNED at connect time was opened (the push channel). Its frames and topics are judged against
	// the ceiling resolved then, so a re-judgment that finds a different one — narrowed, widened or
	// turned read-only — ends the connection, and the client reconnects and re-subscribes under the
	// new range instead of silently keeping the old one.
	ceilingFP     string
	ceilingPinned bool
	// close ends the connection. It is called at most once, outside the registry lock.
	close func(reason string)
}

// liveConnRegistry is the table of registered connections. It is a value on the Server (no
// construction needed) and its zero state is an empty table.
type liveConnRegistry struct {
	mu    sync.Mutex
	next  uint64
	byKey map[string]*liveConn
	// sweeping marks the running periodic re-judgment, so the first registration starts one and
	// the goroutine exits once the table has been empty for a whole tick.
	sweeping bool
}

// liveConnHandle is what a registration hands back: release removes the entry when the connection
// ends by itself, setPerms re-declares what it needs (the push channel's topics).
type liveConnHandle struct {
	reg *liveConnRegistry
	key string
}

// liveConnSpec is one registration.
type liveConnSpec struct {
	kind     liveConnKind
	resource string
	// key makes the registration reachable by name (hostedKey); empty gives a private one.
	key string
	// caller and callerKind say who opened it; session is the terminal session the request named
	// (empty when it named none, which is also the case for a hosted forwarder: design §6
	// 「autostart 预设只认启动者的调用方级授权」).
	caller     string
	callerKind string
	session    string
	// token is the bearer token it authenticated with (hashed on entry, never kept).
	token string
	// perms is what it needs; deps is the connect-time dependency set.
	perms []string
	deps  []string
	// projects re-evaluates the resource's projects (see liveConn.projects); write marks a
	// state-changing connection.
	projects func() ([]string, bool)
	write    bool
	// ceilingFP / ceilingPinned pin the connection to the ceiling in force at connect time (see
	// liveConn.ceilingFP).
	ceilingFP     string
	ceilingPinned bool
	close         func(reason string)
}

// trackHostedForwarder registers one RUNNING hosted forwarder as a long connection of its
// starter: the permissions are the hosted pair every hosted route asks (design §3「有意不保持现状
// 的三处」#2), and the close stops the forwarder — losing them must take the listener away, not
// just refuse the next request. It is keyed by the forwarder's name so its stop route can take
// the registration away again (forgetLiveConn).
func (s *Server) trackHostedForwarder(name string, spec liveConnSpec) {
	mgr := s.hostedForwarders
	spec.kind, spec.resource, spec.key = liveConnHosted, name, hostedLiveConnKey(name)
	spec.perms = []string{authz.PermTunnelHosted, authz.PermTunnelOpen}
	// A forwarder accepts local connections for its worker: it is a write-shaped connection of a
	// caller whose ceiling says nothing about a project (it has none).
	spec.projects, spec.write = liveConnProjectless, true
	spec.close = func(string) {
		if mgr != nil {
			_ = mgr.Stop(name)
		}
	}
	s.registerLiveConn(spec)
}

// hostedLiveConnKey is the registry key of a hosted forwarder: its name, which is what its stop
// route has to reach.
func hostedLiveConnKey(name string) string {
	return string(liveConnHosted) + "/" + name
}

// tokenFingerprint hashes a bearer token so the table can recognize "the same credential" across a
// reload without holding the secret (the server keeps tokens in memory only, plan §11).
func tokenFingerprint(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

// requestLiveConnSpec builds the registration of a connection opened by a user-class REQUEST: who
// the request authenticated as, the session it named, the token it presented and the database
// grants its decision rested on.
func (s *Server) requestLiveConnSpec(c *rux.Context, kind liveConnKind, resource string, perms ...string) liveConnSpec {
	spec := liveConnSpec{
		kind: kind, resource: resource,
		caller: callerFromCtx(c), callerKind: callerKindFromCtx(c),
		session: requestSessionID(c), perms: perms,
		// The decision is already resolved (the handler asked s.can to get here), and its
		// dependency set is design §6's 「依赖的授权 id 集合」.
		deps: s.decisionOf(c).GrantIDs(),
	}
	if tok := presentedToken(c); tok != "" {
		spec.token = tok
	}
	return spec
}

// presentedToken is the bearer credential a request authenticated with, or "" (the empty-token
// deployment). It is hashed before it reaches the long-connection table.
func presentedToken(c *rux.Context) string {
	if c == nil || c.Req == nil {
		return ""
	}
	tok, _ := bearerToken(c.Req.Header.Get("Authorization"))
	return tok
}

// ticketLiveConnSpec builds the registration of a connection opened with an EXTERNAL ticket (the
// attach relay and the browser push channel): the handshake carries no Authorization header, so
// the ticket binds the caller, the session, the permissions the issue-time decision admitted it on
// and the dependency set behind them (design §6「attach 与 ws 的 ticket 也带上调用方和依赖集合」).
func ticketLiveConnSpec(b AttachTicketBinding, kind liveConnKind, resource string) liveConnSpec {
	return liveConnSpec{
		kind: kind, resource: resource,
		caller: b.Caller, callerKind: callerKindUser,
		session: b.Session, token: b.Token, perms: b.Perms, deps: b.Deps,
	}
}

// liveJobReadPerms is what a streaming connection to ONE job rests on: nothing when the job is the
// caller's own resource (or the caller is not governed by permissions), job.read.others otherwise
// — exactly the rule mayReadJob applied to admit it (design §1, the ownership exception).
func (s *Server) liveJobReadPerms(c *rux.Context, owner string) []string {
	if callerKindFromCtx(c) != callerKindUser {
		return nil
	}
	if owner == callerFromCtx(c) {
		return nil
	}
	return []string{authz.PermJobReadOthers}
}

// registerLiveConn adds one long-lived connection to the table. release() (via the returned
// handle) takes it out again when the connection ends by itself; the registry closes it when a
// re-judgment finds it may not stay.
func (s *Server) registerLiveConn(spec liveConnSpec) *liveConnHandle {
	if spec.close == nil {
		return &liveConnHandle{}
	}
	r := &s.liveConns
	entry := &liveConn{
		key: spec.key, kind: spec.kind, resource: spec.resource,
		caller: spec.caller, callerKind: spec.callerKind, session: spec.session,
		tokenFP: tokenFingerprint(spec.token),
		perms:   dedupePerms(spec.perms), deps: append([]string(nil), spec.deps...),
		projects: spec.projects, write: spec.write,
		ceilingFP: spec.ceilingFP, ceilingPinned: spec.ceilingPinned,
		close: spec.close,
	}
	if entry.callerKind == "" {
		entry.callerKind = callerKindUser
	}
	start := false
	r.mu.Lock()
	if r.byKey == nil {
		r.byKey = map[string]*liveConn{}
	}
	if entry.key == "" {
		r.next++
		entry.key = fmt.Sprintf("%s/%d", spec.kind, r.next)
	}
	r.byKey[entry.key] = entry
	if !r.sweeping {
		r.sweeping = true
		start = true
	}
	r.mu.Unlock()
	slog.Debug("authz.live_conn_registered", "kind", entry.kind, "resource", entry.resource,
		"caller", entry.caller, "session", entry.session, "perms", entry.perms, "grants", entry.deps)
	if start {
		go s.sweepLiveConns()
	}
	return &liveConnHandle{reg: r, key: entry.key}
}

// release takes the connection out of the table without closing it (it is ending on its own).
func (h *liveConnHandle) release() {
	if h == nil || h.reg == nil || h.key == "" {
		return
	}
	h.reg.remove(h.key)
}

// setPerms re-declares what the connection needs (the push channel's subscriptions). It is what
// makes "no longer allowed to read this topic" reachable by a re-judgment.
func (h *liveConnHandle) setPerms(perms ...string) {
	if h == nil || h.reg == nil || h.key == "" {
		return
	}
	h.reg.mu.Lock()
	if e := h.reg.byKey[h.key]; e != nil {
		e.perms = dedupePerms(perms)
	}
	h.reg.mu.Unlock()
}

// remove drops one entry and ends it if it is still registered. ok reports whether this call was
// the one that took it out.
func (r *liveConnRegistry) remove(key string) bool {
	r.mu.Lock()
	e, ok := r.byKey[key]
	if ok {
		delete(r.byKey, key)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	if e.close != nil {
		e.close("connection ended")
	}
	return true
}

// drop ends one entry because it may not stay, naming the trigger in the log.
func (r *liveConnRegistry) drop(key, reason string) {
	r.mu.Lock()
	e, ok := r.byKey[key]
	if ok {
		delete(r.byKey, key)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	slog.Info("authz.live_conn_dropped", "event", "authz.live_conn_dropped", "component", "server",
		"kind", e.kind, "resource", e.resource, "caller", e.caller, "session", e.session,
		"perms", e.perms, "grants", e.deps, "reason", reason)
	if e.close != nil {
		e.close(reason)
	}
}

// forgetLiveConn takes the registration of key away without ending it: the connection is already
// gone (a hosted forwarder its stop route just stopped).
func (s *Server) forgetLiveConn(key string) {
	s.liveConns.mu.Lock()
	delete(s.liveConns.byKey, key)
	s.liveConns.mu.Unlock()
}

// forgetHostedLiveConns takes away every hosted registration (its listeners are all stopped).
func (s *Server) forgetHostedLiveConns() {
	prefix := string(liveConnHosted) + "/"
	s.liveConns.mu.Lock()
	for key, e := range s.liveConns.byKey {
		if e.kind == liveConnHosted && strings.HasPrefix(key, prefix) {
			delete(s.liveConns.byKey, key)
		}
	}
	s.liveConns.mu.Unlock()
}

// snapshot copies the registered connections, so a re-judgment works on one immutable picture: a
// push channel re-declares its permissions on every subscribe (setPerms) while a sweep may be
// reading them.
func (r *liveConnRegistry) snapshot() []liveConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]liveConn, 0, len(r.byKey))
	for _, e := range r.byKey {
		c := *e
		c.perms = append([]string(nil), e.perms...)
		out = append(out, c)
	}
	return out
}

// recheckLiveConns re-judges every registered connection and ends the ones that may not stay
// (design §6「断开时重新判定一次：仍然有权限的连接保留」). It is the ONE place a long connection is
// dropped, so every trigger behaves the same way.
func (s *Server) recheckLiveConns(reason string) {
	for _, e := range s.liveConns.snapshot() {
		if s.liveConnStillAllowed(e) {
			continue
		}
		s.liveConns.drop(e.key, reason)
	}
}

// liveConnProjectless is the projects re-evaluation of a connection whose resource names no
// project at all — a tunnel, a hosted dial, a hosted forwarder. 设计「没有项目的资源（…隧道）：存在
// ceiling 且 projects 非空时视为范围外（失败关闭）」: with a ceiling such a connection cannot show
// a project inside it, so the re-judgment ends it.
func liveConnProjectless() ([]string, bool) { return nil, false }

// liveConnJobProjects is the projects re-evaluation of a connection that streams ONE job (SSE,
// attach): the job's project, asked again on every re-judgment so a job that moved out of scope —
// or a ceiling that was tightened — ends the connection.
func (s *Server) liveConnJobProjects(id string) func() ([]string, bool) {
	return func() ([]string, bool) {
		projects, found, err := jobProjects(s, id)
		if err != nil || !found {
			return nil, false
		}
		return projects, true
	}
}

// liveConnStillAllowed decides whether one connection may stay: its identity still resolves in the
// live configuration under the same token, every permission it needs is still held, and its
// caller's ceiling still admits it (gofer-7798 第二期).
func (s *Server) liveConnStillAllowed(e liveConn) bool {
	if e.callerKind == callerKindWorker {
		// A worker token is not governed by permissions nor by a ceiling (设计 §2「只对 user 类调用方
		// 计算」): its own allowlist decided the connection when it was opened.
		return true
	}
	if e.callerKind == callerKindUser && !s.liveCallerIdentity(e) {
		return false
	}
	if !s.liveConnCeilingAllows(e) {
		return false
	}
	if e.callerKind != callerKindUser || len(e.perms) == 0 {
		// A job credential rests on its ceiling owner's admission, not on the permission model
		// (its own route table judged it at connect time).
		return true
	}
	d := s.effective(e.caller, e.session)
	for _, p := range e.perms {
		if !d.Has(p) {
			return false
		}
	}
	return true
}

// liveConnCeilingAllows re-judges one connection against the ceiling that governs it
// (gofer-7798 第二期, 设计「长连接」): a read-only ceiling ends a write-shaped connection, and a
// ceiling that restricts projects ends a connection whose resource project is outside it (a
// resource that names no project is outside). A caller without a ceiling — every caller by
// default — passes here exactly as before the ceilings existed.
func (s *Server) liveConnCeilingAllows(e liveConn) bool {
	ceil, known := s.liveConnCeiling(e)
	if !known {
		// A job credential whose ceiling owner cannot be resolved is failed closed (决策 3).
		return false
	}
	if e.ceilingPinned && ceilingFingerprint(ceil) != e.ceilingFP {
		// The ceiling the connection was opened under is gone or has another shape: its range is
		// no longer the caller's range (gofer-7798 第二期, 计划 E3-1).
		return false
	}
	if ceil == nil {
		return true
	}
	if e.write && ceil.IsReadOnly() {
		return false
	}
	if e.projects == nil {
		// Not a project-shaped connection (the push channel): its frames are scoped by
		// pushhub and its topics by the ws subscription check.
		return true
	}
	projects, have := e.projects()
	return have && ceilingAllowsAll(ceil, projects)
}

// liveConnCeiling resolves the ceiling a long connection is judged by: its own for a user-class
// caller, and the one of its CEILING OWNER for a job credential (决策 3, the same owner
// jobCredentialCeiling asks about for an HTTP request). known=false means the answer cannot be
// established — a job credential whose owner does not resolve — and the caller fails closed.
func (s *Server) liveConnCeiling(e liveConn) (ceil *config.CallerCeiling, known bool) {
	cfg := s.serverCfg()
	if cfg == nil {
		return nil, true
	}
	switch e.callerKind {
	case callerKindUser:
		if e.caller == "" {
			return nil, true
		}
		c, _ := cfg.CallerCeiling(e.caller)
		return c, true
	case callerKindJob:
		// A deployment without a restricted caller cannot have a restricted owner: skip the
		// lookup (and with it the fail-closed refusal), like jobCredentialCeiling does.
		if s.jobs == nil || len(cfg.CeilingCallers()) == 0 {
			return nil, true
		}
		owner, err := s.jobs.CredentialCeilingOwner(e.caller)
		if err != nil {
			return nil, false
		}
		if owner == "" {
			return nil, true
		}
		c, _ := cfg.CallerCeiling(owner)
		return c, true
	}
	return nil, true
}

// liveCallerIdentity reports whether the caller a connection was opened by still exists under the
// same token. A caller the configuration no longer declares, or one whose token changed, ends the
// connection: it was authenticated by a credential that no longer exists (design §6「配置重载后，
// 该调用方被删、token 变了」).
//
// A connection registered WITHOUT a token keeps its identity: a hosted forwarder restored from an
// autostart preset is 「按根管理员处理」 when its starter is not a live caller (the T1.5f rule), and
// such a starter can never match a caller id again.
func (s *Server) liveCallerIdentity(e liveConn) bool {
	entries := s.callerEntries()
	if len(entries) == 0 {
		// The tokenless deployment: the anonymous caller is the only one there is.
		return e.caller == "" && s.allowEmptyToken
	}
	if e.tokenFP == "" {
		return true
	}
	for _, ce := range entries {
		if ce.id == e.caller && ce.kind == callerKindUser {
			return tokenFingerprint(ce.token) == e.tokenFP
		}
	}
	return false
}

// sweepLiveConns is the periodic re-judgment: it runs while the table has entries and exits once
// it has been empty for a whole tick, so a server without long connections pays nothing. It is
// what catches an expiry, a session that ended or came back as a new incarnation, and another
// process's revoke.
func (s *Server) sweepLiveConns() {
	r := &s.liveConns
	t := time.NewTicker(s.liveConnSweepEvery())
	defer t.Stop()
	for range t.C {
		s.recheckLiveConns("sweep")
		r.mu.Lock()
		empty := len(r.byKey) == 0
		if empty {
			r.sweeping = false
		}
		r.mu.Unlock()
		if empty {
			return
		}
	}
}

// liveConnSweepEvery is the sweep interval in force (a test shrinks it).
func (s *Server) liveConnSweepEvery() time.Duration {
	if s.connSweepEvery > 0 {
		return s.connSweepEvery
	}
	return defaultLiveConnSweepInterval
}

// dedupePerms removes duplicates from a permission list, keeping the order.
func dedupePerms(perms []string) []string {
	if len(perms) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// canAs decides one permission for an EXPLICIT caller/session pair and records the answer for the
// calling-assertion test, exactly like s.can. The ticket-based long connections use it: their
// identity comes from the ticket, not from an Authorization header the handshake cannot carry.
func (s *Server) canAs(c *rux.Context, caller, session string, perms ...string) bool {
	d := s.effective(caller, session)
	ok := true
	for _, p := range perms {
		held := d.Has(p)
		recordDecision(c, p, held)
		ok = ok && held
	}
	return ok
}

// ---- the push channel's topics ----

// wsTopicAllowed decides whether a browser push connection may subscribe to topic (design §6
// 「/v1/ws 的推送按 job.read.others / session.read.others 过滤 topic」, and 7798 第二期 for the
// project range).
//
// The topics that carry someone else's resource need that resource's `.others` permission, which
// is also what the REST reads behind them ask (permissions.md):
//
//	job:<id>    one job's events — the caller's OWN job needs nothing (设计 §1「操作自己的资源不需要
//	            这类权限」), anyone else's needs job.read.others;
//	jobs        the job-list invalidation, which names job ids and statuses — job.read.others;
//	sessions    the session-list invalidation — session.read.others;
//	worker-enrollments  the enrollment-list invalidation — worker.manage;
//	everything else (the console's own meta / stats / pending snapshots) is left as it was: the
//	design names these two permissions for the two resource topics.
func (s *Server) wsTopicAllowed(c *rux.Context, ceil *config.CallerCeiling, caller, session, topic string) bool {
	switch {
	case topic == pushhub.TopicSessions:
		return s.canAs(c, caller, session, authz.PermSessionReadOthers)
	case topic == pushhub.TopicJobs:
		return s.canAs(c, caller, session, authz.PermJobReadOthers)
	case strings.HasPrefix(topic, pushhub.JobTopicPrefix):
		id := strings.TrimPrefix(topic, pushhub.JobTopicPrefix)
		res, ok := s.jobForAccess(id)
		// 7798 第二期 (设计「/v1/ws 的 job:<id> 主题对自己的 job 也要过项目范围」): a job outside
		// the caller's ceiling is answered exactly like a job that does not exist — the OWN job
		// included, and a job nobody can attribute to a project included (fail closed).
		if ceil != nil && len(ceil.Projects) > 0 &&
			(!ok || !ceilingAllowsAll(ceil, []string{res.ProjectKey})) {
			return false
		}
		if ok && res.CallerID != "" && res.CallerID == caller {
			return true // the caller's own job needs no permission
		}
		return s.canAs(c, caller, session, authz.PermJobReadOthers)
	case topic == pushhub.TopicWorkerEnrollments:
		// Explicit: the switch's fall-through is "allow", which must never apply here.
		return s.canAs(c, caller, session, authz.PermWorkerManage)
	}
	return true
}

// wsTopicPerms is what a subscription to topic rests on: the permission that allowed it (empty for
// the caller's own job and for the topics that ask nothing), so the push connection can re-declare
// its needs on every subscribe / unsubscribe.
func (s *Server) wsTopicPerms(c *rux.Context, caller, session, topic string) []string {
	switch {
	case topic == pushhub.TopicSessions:
		return []string{authz.PermSessionReadOthers}
	case topic == pushhub.TopicJobs:
		return []string{authz.PermJobReadOthers}
	case topic == pushhub.TopicWorkerEnrollments:
		return []string{authz.PermWorkerManage}
	case strings.HasPrefix(topic, pushhub.JobTopicPrefix):
		id := strings.TrimPrefix(topic, pushhub.JobTopicPrefix)
		if res, ok := s.jobForAccess(id); ok && res.CallerID != "" && res.CallerID == caller {
			return nil
		}
		return []string{authz.PermJobReadOthers}
	}
	return nil
}

// wsFilterTopics splits the topics a client asked for into the ones it MAY subscribe to and the ones
// it may not (design §6). The refused ones are reported to the client in the hub's own error frame,
// so a caller that lost job.read.others (or asks for another caller's job) is told rather than
// silently left without news.
func (s *Server) wsFilterTopics(c *rux.Context, ceil *config.CallerCeiling, b AttachTicketBinding, topics []string) (allowed, rejected []string) {
	for _, t := range topics {
		if s.wsTopicAllowed(c, ceil, b.Caller, b.Session, t) {
			allowed = append(allowed, t)
			continue
		}
		slog.Info("authz.ws_topic_denied", "event", "authz.ws_topic_denied", "component", "server",
			"caller", b.Caller, "session", b.Session, "topic", t)
		rejected = append(rejected, t)
	}
	return allowed, rejected
}

// wsRefreshTopicPerms re-declares what the push channel rests on from its CURRENT subscriptions, so
// a subscription that is no longer allowed is reachable by a re-judgment (the neighbor of the
// per-subscribe filter: a permission can go away while the connection is open).
func (s *Server) wsRefreshTopicPerms(h *liveConnHandle, pc *pushhub.Conn, c *rux.Context, b AttachTicketBinding) {
	var perms []string
	for _, t := range pc.Topics() {
		perms = append(perms, s.wsTopicPerms(c, b.Caller, b.Session, t)...)
	}
	h.setPerms(perms...)
}

// wsCeiling is the ceiling that governs one push connection: the one of the owner its ticket bound
// (the issuing caller). It is asked of the LIVE configuration, so a subscription made after a
// reload is judged by the ceiling in force then. nil means the owner has no ceiling (every caller
// by default), and the connection is left exactly as it was before the ceilings existed.
func (s *Server) wsCeiling(owner string) *config.CallerCeiling {
	if owner == "" {
		return nil
	}
	cfg := s.serverCfg()
	if cfg == nil {
		return nil
	}
	ceil, ok := cfg.CallerCeiling(owner)
	if !ok {
		return nil
	}
	return ceil
}

// ceilingConnScope turns a ceiling into the project restriction of one push connection (7798
// 第二期). A ceiling that names no project restricts nothing of the fan-out: the zero ConnScope is
// the unrestricted connection, whose frames stay byte-identical.
func ceilingConnScope(ceil *config.CallerCeiling) pushhub.ConnScope {
	if ceil == nil || len(ceil.Projects) == 0 {
		return pushhub.ConnScope{}
	}
	return pushhub.ConnScope{
		Key:   ceilingScopeKey(sortedCeilingProjects(ceil)),
		Scope: func(project string) bool { return ceilingAllowsProject(ceil, project) },
	}
}

// sortedCeilingProjects is the ceiling's project patterns in a stable order, so two ceilings that
// admit the same projects fingerprint the same way.
func sortedCeilingProjects(ceil *config.CallerCeiling) []string {
	if ceil == nil || len(ceil.Projects) == 0 {
		return nil
	}
	out := append([]string(nil), ceil.Projects...)
	sort.Strings(out)
	return out
}

// ceilingFingerprint identifies the shape of a ceiling as far as a long connection is concerned:
// the projects it admits and whether it is read-only. A connection that pins this at connect time
// (the push channel) is dropped when the fingerprint changes; a nil ceiling — the unrestricted
// caller — fingerprints as the empty string.
func ceilingFingerprint(ceil *config.CallerCeiling) string {
	if ceil == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(sortedCeilingProjects(ceil), "\x1f") +
		"\x00" + strconv.FormatBool(ceil.IsReadOnly())))
	return "ceil-fp:" + hex.EncodeToString(sum[:8])
}

// ceilingScopeKey fingerprints a project restriction for the snapshot cache: connections of the
// same ceiling share one computed stats / pending frame, connections of different ceilings never
// do.
func ceilingScopeKey(sortedPatterns []string) string {
	sum := sha256.Sum256([]byte(strings.Join(sortedPatterns, "\x1f")))
	return "ceil:" + hex.EncodeToString(sum[:8])
}
