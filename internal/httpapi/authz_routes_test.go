package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/metrics"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/ptyrelay"
)

// 路由声明框架的兜底测试（设计「架构」的「测试兜底」1 与 JobCred / WorkerSelf 快照，计划 T1.4）：
//
//   - 覆盖测试：每条注册的路由要么已声明，要么在「待迁移」清单里，且两个方向都成立（声明 /
//     清单里没有多出来的、对不上任何路由的条目）；
//   - 目录测试：声明里的权限名都在权限目录里；
//   - 白名单快照：声明表引用的 SEC-01 键集合与 jobcredential.go 的三张表逐项相等，
//     WorkerSelf 集合与 workercredential.go 的 4 条逐项相等，且声明与路由本身一致；
//   - 按类别：gate 对 7 种类别（加上 NoUserSurface 与 待迁移）的行为。

// newRouteTableServer builds a server with EVERY conditional route mounted — metrics, the
// presence routes, the pty relay and the ws hub — so a coverage test sees the whole route
// surface of buildRouter and not only the routes a minimal test server mounts.
func newRouteTableServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, testToken, false)
	s.SetMetrics(metrics.New(), true, "")
	s.SetPresence(presence.NewService(s.jobs.Meta()))
	s.SetPtyRelay(ptyrelay.NewNonceStore(), ptyrelay.NewRegistry())
	s.hub = fakeHub{}
	s.router = s.buildRouter()
	return s
}

// registeredRouteKeys returns the canonical key of every route of the server's router.
func registeredRouteKeys(t *testing.T, s *Server) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, ri := range s.router.Routes() {
		for _, method := range ri.Methods {
			out[canonicalRouteKey(method, ri.Path)] = true
		}
	}
	return out
}

// TestEveryRouteIsDeclaredOrPending is the design's coverage test: every route of the
// router carries a declaration or sits on the 待迁移 list of plan T1.4, and neither table
// has an entry that matches no route (a stale entry would hide a rename).
func TestEveryRouteIsDeclaredOrPending(t *testing.T) {
	t.Parallel()
	registered := registeredRouteKeys(t, newRouteTableServer(t))
	if len(registered) < 300 {
		t.Fatalf("the test server registered only %d routes; the router is not fully wired", len(registered))
	}

	for key := range registered {
		if _, declared := routeDecls[key]; declared {
			continue
		}
		if pendingRoutes[key] {
			continue
		}
		t.Errorf("route %s is neither declared nor on the 待迁移 list (authz_routes.go)", key)
	}

	for key, decl := range routeDecls {
		if decl.static {
			// The SPA fallback is registered through NotFound, not as a route.
			continue
		}
		if !registered[key] {
			t.Errorf("declaration %s matches no registered route", key)
			continue
		}
		if decl.user.kind == userPending && !pendingRoutes[key] {
			t.Errorf("declared route %s still has an unmigrated user side but is not on the 待迁移 list", key)
		}
		if decl.user.kind != userPending && pendingRoutes[key] {
			t.Errorf("route %s is declared (user category %d) and must not stay on the 待迁移 list", key, decl.user.kind)
		}
	}

	for key := range pendingRoutes {
		if !registered[key] {
			t.Errorf("待迁移 entry %s matches no registered route", key)
		}
	}
}

// TestDeclaredPermissionsAreInCatalog: every permission a declaration names is a catalog
// permission (design: 「声明里的权限名必须在目录里」), and a declaration that names
// permissions carries the mode they are decided with.
func TestDeclaredPermissionsAreInCatalog(t *testing.T) {
	t.Parallel()
	for key, decl := range routeDecls {
		switch decl.user.kind {
		case userRequire, userInHandler:
			if len(decl.user.perms) == 0 {
				t.Errorf("%s is declared with an empty permission list", key)
			}
			if decl.user.mode != permDeny && decl.user.mode != permFilter {
				t.Errorf("%s has mode %q, want %q or %q", key, decl.user.mode, permDeny, permFilter)
			}
		default:
			if len(decl.user.perms) != 0 {
				t.Errorf("%s declares permissions %v without Require/InHandler", key, decl.user.perms)
			}
		}
		for _, p := range decl.user.perms {
			if _, ok := authz.Lookup(p); !ok {
				t.Errorf("%s declares unknown permission %q", key, p)
			}
		}
	}
}

// TestJobCredKeysMatchSEC01Tables is the SEC-01 half of the whitelist snapshot: the set of
// SEC-01 keys the declaration table references is exactly the union of jobWriteAllowlist,
// stewardReadAllow and stewardWriteAllow, and each reference is the key the SEC-01 table
// really matches for that route (a request to it collapses the same way).
func TestJobCredKeysMatchSEC01Tables(t *testing.T) {
	t.Parallel()
	want := map[string]bool{}
	for key := range jobWriteAllowlist {
		want[key] = true
	}
	for key := range stewardReadAllow {
		want[key] = true
	}
	for key := range stewardWriteAllow {
		want[key] = true
	}

	got := map[string]bool{}
	for key, decl := range routeDecls {
		if decl.jobCred == "" {
			continue
		}
		got[decl.jobCred] = true
		method, path, ok := strings.Cut(key, " ")
		if !ok {
			t.Errorf("route key %q is not `<METHOD> <path>`", key)
			continue
		}
		// The SEC-01 middleware keys a request, not a template: ask it about a concrete
		// path of this very route. A mismatch means the annotation and the table the
		// middleware actually consults disagree.
		if routeKey := jobRouteKey(method, samplePath(path)); routeKey != decl.jobCred {
			t.Errorf("route %s declares SEC-01 key %q but the SEC-01 table matches it as %q", key, decl.jobCred, routeKey)
		}
	}
	for key := range want {
		if !got[key] {
			t.Errorf("no declaration references SEC-01 key %s", key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("declaration references SEC-01 key %s, which is in none of the three tables", key)
		}
	}
}

// workerHandshakeRoutes are the three WebSocket handshakes a worker dials
// (buildRouter registers them OUTSIDE the /v1 group: a WS upgrade cannot use the JSON error
// envelope, so the worker middleware — and with it workercredential.go's whitelist — never sees
// them). The declaration table marks all three WorkerSelf, because they ARE the worker's own
// surface, and this test's expected value is the whitelist UNION them: the whitelist itself is
// unchanged, it simply cannot express a route its middleware never matches.
var workerHandshakeRoutes = []workerSelfRoute{
	{http.MethodGet, "/v1/workers/connect"},
	{http.MethodGet, "/v1/workers/tunnel-connect"},
	{http.MethodGet, "/v1/workers/pty-connect"},
}

// TestWorkerSelfRoutesMatchWhitelist is the WorkerSelf half of the snapshot: the declared
// WorkerSelf routes are exactly workercredential.go's four whitelist rows (methods included,
// and the whitelist really allows each of them) plus the three handshakes registered outside the
// /v1 group, item by item.
func TestWorkerSelfRoutesMatchWhitelist(t *testing.T) {
	t.Parallel()
	want := map[string]bool{}
	for _, rt := range workerSelfRoutes {
		want[rt.method+" "+rt.pattern] = true
	}
	handshake := map[string]bool{}
	for _, rt := range workerHandshakeRoutes {
		key := rt.method + " " + rt.pattern
		want[key] = true
		handshake[key] = true
	}

	got := map[string]bool{}
	for key, decl := range routeDecls {
		if !decl.workerSelf {
			continue
		}
		method, path, ok := strings.Cut(key, " ")
		if !ok {
			t.Errorf("route key %q is not `<METHOD> <path>`", key)
			continue
		}
		got[wildcardRouteKey(key)] = true
		if !workerRouteAllowed(method, samplePath(path)) && !handshake[method+" "+path] {
			t.Errorf("route %s is declared WorkerSelf but neither workercredential.go's whitelist nor the handshake set holds it", key)
		}
	}
	for key := range want {
		if !got[key] {
			t.Errorf("no declaration marks the worker route %s as WorkerSelf", key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("declaration marks %s WorkerSelf, which is neither a worker whitelist row nor a handshake route", key)
		}
	}
}

// TestXferContentGetDeclaration pins the combined declaration of design / plan T1.4:
// `GET /v1/xfer/{id}/content` is `InHandler(xfer.manage.others) + WorkerSelf` — a user
// downloads someone else's transfer only with the permission, the handler doing the
// deciding, while the executing worker reaches its own upload through the whitelist.
func TestXferContentGetDeclaration(t *testing.T) {
	t.Parallel()
	const key = "GET /v1/xfer/{id}/content"
	decl, ok := routeDecls[key]
	if !ok {
		t.Fatalf("%s has no declaration", key)
	}
	if !decl.workerSelf {
		t.Error("the route must be declared WorkerSelf")
	}
	if decl.user.kind != userInHandler {
		t.Errorf("user category = %d, want InHandler", decl.user.kind)
	}
	if len(decl.user.perms) != 1 || decl.user.perms[0] != authz.PermXferManageOthers {
		t.Errorf("permissions = %v, want [%s]", decl.user.perms, authz.PermXferManageOthers)
	}
	if decl.user.mode != permDeny {
		t.Errorf("mode = %q, want %q", decl.user.mode, permDeny)
	}
	if !workerRouteAllowed(http.MethodGet, "/v1/xfer/x/content") {
		t.Error("the worker whitelist must allow the route")
	}
}

// TestAuthzGateCategories: the gate of every category — a route that needs a permission
// refuses a user-class caller without it, names it in the 403, and every other category
// leaves the request to the handler. A worker token and a job credential are untouched:
// their own tables are authoritative (design: JobCred / WorkerSelf 只是标注).
func TestAuthzGateCategories(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})

	cases := []struct {
		name     string
		decl     routeDecl
		kind     string
		wantCode int
	}{
		{"Require with the permission", routeDecl{user: Require(authz.PermConfigRead)}, callerKindUser, http.StatusOK},
		{"Require without it", routeDecl{user: Require(authz.PermAuthManage)}, callerKindUser, http.StatusForbidden},
		{"Require, one of two missing", routeDecl{user: Require(authz.PermConfigRead, authz.PermAuthManage)}, callerKindUser, http.StatusForbidden},
		{"Public", routeDecl{user: Public()}, callerKindUser, http.StatusOK},
		{"InHandler", routeDecl{user: InHandler(permDeny, authz.PermAuthManage)}, callerKindUser, http.StatusOK},
		{"InHandler, filter", routeDecl{user: InHandler(permFilter, authz.PermAuthManage)}, callerKindUser, http.StatusOK},
		{"NoUserSurface", routeDecl{user: NoUserSurface()}, callerKindUser, http.StatusOK},
		{"UserPending", routeDecl{user: UserPending()}, callerKindUser, http.StatusOK},
		{"JobCred annotation only", routeDecl{user: Public(), jobCred: "POST /v1/jobs"}, callerKindUser, http.StatusOK},
		{"Require, worker token", routeDecl{user: Require(authz.PermAuthManage)}, callerKindWorker, http.StatusOK},
		{"Require, job credential", routeDecl{user: Require(authz.PermAuthManage)}, callerKindJob, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveProbe(t, s, tc.kind, tc.decl)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode != http.StatusForbidden {
				return
			}
			body := rec.Body.String()
			if !strings.Contains(body, authz.PermAuthManage) {
				t.Errorf("403 body %q does not name the missing permission", body)
			}
			if !strings.Contains(body, "root administrator") {
				t.Errorf("403 body %q does not say where to get the permission", body)
			}
		})
	}
}

// TestCanRecordsDecisions: the decision record of one request (the design's 调用断言) holds
// every permission the decision path was asked about, in order — the route gate's own
// requirement first, then the handler's — so a later task can assert per route that each
// declared permission really was decided.
func TestCanRecordsDecisions(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})

	var recorded []decidedPerm
	r := rux.New()
	r.Use(fixedCaller("alice", callerKindUser))
	probe := func(c *rux.Context) {
		// The seeded `job.submit` comes from the database, auth.manage is not derivable.
		if !s.can(c, authz.PermJobSubmit) {
			t.Errorf("alice must hold the seeded %s", authz.PermJobSubmit)
		}
		if s.can(c, authz.PermAuthManage) {
			t.Errorf("auth.manage must not be held by a non-root caller")
		}
		recorded = decisionsOf(c)
		c.JSON(http.StatusOK, map[string]any{"ok": true})
	}
	r.GET("/probe", probe, s.authzGate(routeDecl{user: Require(authz.PermJobSubmit)}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	want := []decidedPerm{
		{perm: authz.PermJobSubmit, granted: true},   // the gate
		{perm: authz.PermJobSubmit, granted: true},   // the handler
		{perm: authz.PermAuthManage, granted: false}, // the handler
	}
	if len(recorded) != len(want) {
		t.Fatalf("recorded decisions = %v, want %v", recorded, want)
	}
	for i := range want {
		if recorded[i] != want[i] {
			t.Errorf("decision %d = %v, want %v", i, recorded[i], want[i])
		}
	}
}

// serveProbe runs one request through a router whose caller identity is fixed (the auth
// middleware's job in production) with the gate of decl on the route.
func serveProbe(t *testing.T, s *Server, kind string, decl routeDecl) *httptest.ResponseRecorder {
	t.Helper()
	r := rux.New()
	r.Use(fixedCaller("alice", kind))
	r.GET("/probe", func(c *rux.Context) { c.JSON(http.StatusOK, map[string]any{"ok": true}) }, s.authzGate(decl))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))
	return rec
}

// fixedCaller is the minimal stand-in for authMiddleware: it stamps a caller id and kind
// onto the request context, which is all the gate needs.
func fixedCaller(id, kind string) rux.HandlerFunc {
	return func(c *rux.Context) {
		c.Set(ctxCallerID, id)
		c.Set(ctxCallerKind, kind)
		c.Next()
	}
}

// samplePath turns a route template into one concrete path of that route: every `{name}`
// segment becomes a literal the SEC-01 / worker tables treat as a parameter (`x`).
func samplePath(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segs {
		if len(seg) > 1 && seg[0] == '{' && seg[len(seg)-1] == '}' {
			segs[i] = "x"
		}
	}
	return "/" + strings.Join(segs, "/")
}

// wildcardRouteKey renders a canonical route key in the `*`-per-segment spelling the SEC-01
// and worker tables use (`GET /v1/workers/{id}/assignable` -> `GET /v1/workers/*/assignable`).
func wildcardRouteKey(key string) string {
	method, path, ok := strings.Cut(key, " ")
	if !ok {
		return key
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segs {
		if len(seg) > 1 && seg[0] == '{' && seg[len(seg)-1] == '}' {
			segs[i] = "*"
		}
	}
	return method + " /" + strings.Join(segs, "/")
}
