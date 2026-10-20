package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// 授权管理接口的测试（设计「接口」一节、§2「授权模型与判定」与 §4，计划 T2.1）：
//
//   - 权限矩阵：谁可以调哪一条路由（根 / 非根 user / worker / job 凭据）；
//   - 有效权限与来源：whoami 报出持有哪些权限、每项从哪来（root / config / db）；
//   - 孤儿授权：配置里删掉调用方 → 停用；同一 id 回来 → 仍停用；activate 恢复；
//   - 展开快照与「有新权限待确认」；
//   - 缓存：本进程写入立即生效，其他进程（直接写库）按 TTL 生效。

// authTestServer is the fixture every case here starts from: an explicit root administrator
// (`root`) plus two ordinary callers. The root is NOT the single-token `default` on purpose, so
// the root-ness comes from `can_admin` and not from being the only caller.
func authTestServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
	}})
}

// authGrantByID reads one grant through the list endpoint, so a case asserts what an operator
// (the root administrator) would see rather than what the store happens to hold.
func authGrantByID(t *testing.T, s *Server, id string) authGrantView {
	t.Helper()
	var list authGrantsView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/grants", "tok-root", nil), &list)
	for _, g := range list.Grants {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("grant %s is not in the list: %+v", id, list.Grants)
	return authGrantView{}
}

// TestAuthWhoamiReportsPermissionsAndSources: whoami answers the four questions a refused
// caller has — who am I, am I a root administrator, what do I really hold, and where did each
// permission come from (设计「接口」: 「调用方、是否根管理员、有效权限与来源、会话 id 的附带
// 情况与来源」).
func TestAuthWhoamiReportsPermissionsAndSources(t *testing.T) {
	t.Parallel()
	s := authTestServer(t)

	var who authWhoamiView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-alice", nil), &who)
	if who.CallerID != "alice" || who.Kind != callerKindUser || who.Root {
		t.Fatalf("whoami = %+v, want alice/user/not root", who)
	}
	if len(who.Roots) != 1 || who.Roots[0] != "root" {
		t.Errorf("roots = %v, want [root]", who.Roots)
	}
	sourced := map[string]authPermissionView{}
	for _, p := range who.Permissions {
		sourced[p.Name] = p
	}
	// The seeded group comes from the database, and the permission names the grant it came from
	// (the dependency set the long-connection teardown needs).
	if got := sourced[authz.PermJobSubmit].Source; got != sourceDB {
		t.Errorf("%s source = %q, want %q", authz.PermJobSubmit, got, sourceDB)
	}
	if got := sourced[authz.PermJobSubmit].GrantID; got != "seed:users:"+authz.PermJobSubmit {
		t.Errorf("%s grant = %q, want the seeded row", authz.PermJobSubmit, got)
	}
	// The config-derived floor keeps its own source, so a console can say READ-ONLY.
	if got := sourced[authz.PermConfigWrite].Source; got != sourceConfig {
		t.Errorf("%s source = %q, want %q", authz.PermConfigWrite, got, sourceConfig)
	}
	// A root-only permission is held by nobody else, whatever the switches say.
	if _, ok := sourced[authz.PermAuthManage]; ok {
		t.Errorf("a non-root caller must not hold %s", authz.PermAuthManage)
	}
	if who.GrantsVersion != s.grantsVersion() {
		t.Errorf("grants_version = %d, want the resolver's picture (%d)", who.GrantsVersion, s.grantsVersion())
	}

	// The root administrator holds the whole catalog, every entry sourced `root`.
	decode(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-root", nil), &who)
	if !who.Root {
		t.Fatal("a caller declared `can_admin: true` must be a root administrator")
	}
	var names []string
	for _, p := range who.Permissions {
		names = append(names, p.Name)
		if p.Source != sourceRoot {
			t.Errorf("%s source = %q, want %q", p.Name, p.Source, sourceRoot)
		}
	}
	if want := catalogNames(); !slices.Equal(names, want) {
		t.Errorf("the root's permissions = %v, want the whole catalog", names)
	}
	// whoami reports the session the request named (the header the session-grant task's client
	// injection sets), so an operator can see what a request was attached to.
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	req.Header.Set(sessionHeader, "sess-1")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var attached authWhoamiView
	if err := json.NewDecoder(rec.Body).Decode(&attached); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	if attached.SessionID != "sess-1" {
		t.Errorf("session_id = %q, want the header's value", attached.SessionID)
	}
	// Without the header nothing is attached.
	var plain authWhoamiView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-alice", nil), &plain)
	if plain.SessionID != "" {
		t.Errorf("session_id = %q, want it empty without the header", plain.SessionID)
	}
}

// TestAuthPermissionMatrix: 权限矩阵（设计「接口」:「auth.manage 只看根管理员……worker、job 凭据
// 访问一律 403」与 HTTPS 路由声明）：whoami / 权限目录 / 路由表对任何 user 类调用方开放，授权管理与
// 审计只有根管理员能碰，worker 与 job 凭据两边都进不去。
func TestAuthPermissionMatrix(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice"},
		},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
	})
	jobToken := seedJobToken(t, s, "job-matrix", jobstore.JobCredentialMember, "")

	public := []struct{ method, path string }{
		{http.MethodGet, "/v1/auth/whoami"},
		{http.MethodGet, "/v1/auth/catalog"},
		{http.MethodGet, "/v1/auth/routes"},
	}
	managed := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/auth/expand?pattern=tunnel.*&subject=users", nil},
		{http.MethodGet, "/v1/auth/grants", nil},
		{http.MethodPost, "/v1/auth/grants", map[string]any{"subject": "users", "pattern": "tunnel.*"}},
		{http.MethodDelete, "/v1/auth/grants/grant-nope", nil},
		{http.MethodPost, "/v1/auth/grants/grant-nope/activate", nil},
		{http.MethodGet, "/v1/auth/audit", nil},
	}

	for _, tc := range public {
		for _, tok := range []string{"tok-alice", "tok-root", jobToken} {
			resp := do(t, s, tc.method, tc.path, tok, nil)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s %s as %s = %d, want 200 (body %s)",
					tc.method, tc.path, tok, resp.StatusCode, readBody(t, resp))
				continue
			}
			_ = resp.Body.Close()
		}
		// A worker token never reaches them: workercredential.go's whitelist refuses every
		// route outside the worker's own four.
		if resp := do(t, s, tc.method, tc.path, "tok-w1", nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a worker token = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
	}

	for _, tc := range managed {
		// The root administrator passes the gate; what it does depends on the route (the
		// unknown grant is a 404, the empty list a 200).
		if resp := do(t, s, tc.method, tc.path, "tok-root", tc.body); resp.StatusCode == http.StatusForbidden {
			t.Errorf("%s %s as the root = 403, want past the gate (body %s)",
				tc.method, tc.path, readBody(t, resp))
		}
		// A non-root user-class caller is refused, and the refusal names the permission and
		// the command that shows what it holds (the batch-1 wording).
		resp := do(t, s, tc.method, tc.path, "tok-alice", tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a non-root caller = %d, want 403", tc.method, tc.path, resp.StatusCode)
			continue
		}
		body := readBody(t, resp)
		if !strings.Contains(body, authz.PermAuthManage) {
			t.Errorf("%s %s: the 403 does not name %s: %s", tc.method, tc.path, authz.PermAuthManage, body)
		}
		if !strings.Contains(body, "gofer auth whoami") {
			t.Errorf("%s %s: the 403 does not point at `gofer auth whoami`: %s", tc.method, tc.path, body)
		}
		// A job credential reads every GET through SEC-01, so the handler's caller-kind half
		// is what keeps the grants out of its reach.
		if resp := do(t, s, tc.method, tc.path, jobToken, tc.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a job credential = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
		// A worker token never gets past its whitelist.
		if resp := do(t, s, tc.method, tc.path, "tok-w1", tc.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a worker token = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// TestAuthGrantLifecycleAndOrphan is the acceptance case of the task: a grant given to a caller
// takes effect at once, survives a configuration reload that drops the caller as an ORPHAN (it
// stops applying), stays an orphan when the caller comes back (design §2: 「同一 id 以后重新
// 出现，仍保持停用」), is restored by activate, and stops applying the moment it is revoked.
func TestAuthGrantLifecycleAndOrphan(t *testing.T) {
	t.Parallel()
	s := authTestServer(t)
	const grant = authz.PermXferManageOthers // root-only, seeded to nobody

	if s.effective("alice", "").Has(grant) {
		t.Fatalf("alice must not hold %s before anything is granted", grant)
	}

	// The root grants alice "xfer.*": the expansion applies the safety rules of design §2, so
	// the wildcard reaches the `.others` permission but NOT the dangerous xfer.write.
	resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", map[string]any{
		"subject": "caller:alice", "pattern": "xfer.*", "note": "container transfer",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create grant = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var created authGrantView
	decode(t, resp, &created)
	if created.Subject != "caller:alice" || created.State != grantStateActive || !created.Revocable {
		t.Fatalf("created grant = %+v, want an active, revocable caller grant", created)
	}
	if !slices.Equal(created.Permissions, []string{grant}) {
		t.Errorf("expansion = %v, want [%s]", created.Permissions, grant)
	}
	if slices.Contains(created.Permissions, authz.PermXferWrite) {
		t.Errorf("a wildcard must not hand out the dangerous %s", authz.PermXferWrite)
	}
	// The snapshot is what the pattern meant when it was saved.
	if !slices.Equal(created.Snapshot, []string{grant}) {
		t.Errorf("snapshot = %v, want [%s]", created.Snapshot, grant)
	}
	if created.GrantedBy != "root" {
		t.Errorf("granted_by = %q, want the root caller", created.GrantedBy)
	}

	// Immediate: a write of THIS process refreshes the resolver (design §3「一致性」), so alice
	// holds it on her very next request — and whoami names the grant it came from.
	var who authWhoamiView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-alice", nil), &who)
	if !slices.ContainsFunc(who.Permissions, func(p authPermissionView) bool {
		return p.Name == grant && p.Source == sourceDB && p.GrantID == created.ID
	}) {
		t.Fatalf("alice does not hold %s from grant %s: %+v", grant, created.ID, who.Permissions)
	}

	// The caller disappears from the configuration: its grant is an orphan (design §2), which
	// stops applying.
	s.SetServerConfig(&config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "bob", Token: "tok-bob"},
	}})
	if s.effective("alice", "").Has(grant) {
		t.Errorf("an orphaned grant must stop applying")
	}
	orphaned := authGrantByID(t, s, created.ID)
	if orphaned.State != grantStateOrphaned || orphaned.Active {
		t.Errorf("grant after the caller was removed = %+v, want orphaned and inactive", orphaned)
	}

	// The caller comes back: the grant stays an orphan — a token reused by somebody else must
	// not inherit it — until a root administrator activates it.
	s.SetServerConfig(&config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
	}})
	if s.effective("alice", "").Has(grant) {
		t.Errorf("a returning caller must not silently get its old grants back")
	}
	resp = do(t, s, http.MethodPost, "/v1/auth/grants/"+created.ID+"/activate", "tok-root", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var activated authGrantView
	decode(t, resp, &activated)
	if activated.State != grantStateActive || !activated.Active {
		t.Fatalf("activated grant = %+v, want active", activated)
	}
	if !s.effective("alice", "").Has(grant) {
		t.Errorf("activate must put the grant back in force")
	}

	// Revoking it takes effect at once as well, and the row stays for the audit trail.
	resp = do(t, s, http.MethodDelete, "/v1/auth/grants/"+created.ID, "tok-root", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var revoked authGrantView
	decode(t, resp, &revoked)
	if revoked.State != grantStateRevoked || revoked.Active || revoked.RevokedBy != "root" {
		t.Fatalf("revoked grant = %+v, want revoked by the root", revoked)
	}
	if s.effective("alice", "").Has(grant) {
		t.Errorf("a revoked grant must stop applying at once")
	}
	// Revoking twice and activating a revoked row change nothing (both stay 200 with the row).
	for _, req := range []struct {
		method, path string
	}{{http.MethodDelete, "/v1/auth/grants/" + created.ID},
		{http.MethodPost, "/v1/auth/grants/" + created.ID + "/activate"}} {
		resp = do(t, s, req.method, req.path, "tok-root", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s = %d, want 200", req.method, req.path, resp.StatusCode)
		}
		var again authGrantView
		decode(t, resp, &again)
		if again.State != grantStateRevoked {
			t.Errorf("%s %s left the grant %s, want revoked", req.method, req.path, again.State)
		}
	}
	// Unknown and config-derived ids are refused with their own words.
	if resp := do(t, s, http.MethodDelete, "/v1/auth/grants/grant-nope", "tok-root", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("revoking an unknown grant = %d, want 404", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/auth/grants/config:users/activate", "tok-root", nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("activating a config-derived entry = %d, want 409", resp.StatusCode)
	}
}

// TestAuthGrantListShowsDerivedAndNewPermissions: 授权列表给出数据库行、配置推出的只读项，以及
// 每条模式在保存时展开出的快照（设计 §2「保存时…保存展开快照」）；目录后来长大了那条模式时，
// 列表报出「有新权限待确认」。
func TestAuthGrantListShowsDerivedAndNewPermissions(t *testing.T) {
	t.Parallel()
	// One gate is OPEN with a caller holding the matching capability, so the deployment really
	// has a per-caller derived set (with every gate closed the whole derivation lands on the
	// `users` subject and no caller has one of its own).
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice", CanAnswer: true},
		},
		Governance: config.GovernanceConfig{RequireAnswerCapability: true},
	})

	var list authGrantsView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/grants", "tok-root", nil), &list)
	if list.Version != s.grantsVersion() {
		t.Errorf("version = %d, want the grant table's generation (%d)", list.Version, s.grantsVersion())
	}
	seeded := 0
	for _, g := range list.Grants {
		if g.ID == "seed:users:"+authz.PermJobSubmit {
			seeded++
			if g.Source != sourceDB || g.State != grantStateActive || !g.Active {
				t.Errorf("the seeded grant = %+v, want an active db row", g)
			}
			if !slices.Equal(g.Permissions, []string{authz.PermJobSubmit}) {
				t.Errorf("the seeded grant expands to %v", g.Permissions)
			}
			if len(g.NewPermissions) != 0 {
				t.Errorf("nothing is new for a freshly seeded grant: %v", g.NewPermissions)
			}
		}
	}
	if seeded != 1 {
		t.Errorf("the seeded %s is missing from the grant list", authz.PermJobSubmit)
	}
	// The config-derived half is read-only and is what a caller gets without any database row.
	var derivedUsers, derivedCaller bool
	for _, d := range list.Derived {
		if !d.ReadOnly || d.Source != sourceConfig {
			t.Errorf("derived %s = %+v, want a read-only config entry", d.ID, d)
		}
		switch d.Subject {
		case jobstore.SubjectUsers:
			derivedUsers = true
			if !slices.Contains(d.Permissions, authz.PermConfigWrite) {
				t.Errorf("the derived `users` set should carry the admin group with the switch closed: %v", d.Permissions)
			}
		case "caller:alice":
			derivedCaller = true
			// The open answer gate derives the answer group for the caller that holds the bit,
			// and NOT for everybody (the `session.reply.others` row of design §3).
			for _, p := range []string{authz.PermJobReview, authz.PermSessionReplyOthers} {
				if !slices.Contains(d.Permissions, p) {
					t.Errorf("alice's derived set misses %s: %v", p, d.Permissions)
				}
			}
		}
	}
	if !derivedUsers {
		t.Error("the grant list must show the config-derived `users` set")
	}
	if !derivedCaller {
		t.Error("the grant list must show a declared caller's own derived set")
	}

	// 「有新权限待确认」: a grant saved when tracker.* meant tracker.sync only. The comparison is
	// a pure function of the row, so the catalog upgrade is simulated on a fabricated snapshot
	// (the live catalog cannot shrink or grow inside a test).
	row := jobstore.AuthGrant{
		ID: "g-old", Subject: jobstore.SubjectUsers, Pattern: "tracker.*",
		ExpandSnapshot: []string{authz.PermTrackerSync},
	}
	current, pending, ok := grantViewPermissions(row)
	if !ok {
		t.Fatal("tracker.* must be readable today")
	}
	if want := []string{authz.PermTrackerSync, authz.PermTrackerDelete}; !slices.Equal(current, want) {
		t.Errorf("current = %v, want %v", current, want)
	}
	if !slices.Equal(pending, []string{authz.PermTrackerDelete}) {
		t.Errorf("pending = %v, want [%s]", pending, authz.PermTrackerDelete)
	}
	view := authGrantViewOf(row, time.Now().Unix())
	if view.State != grantStateActive || !slices.Equal(view.NewPermissions, []string{authz.PermTrackerDelete}) {
		t.Errorf("view = %+v, want the pending permission reported", view)
	}
	// A pattern the catalog can no longer read grants nothing, and the row says so.
	unmatched := authGrantViewOf(jobstore.AuthGrant{ID: "g-gone", Subject: jobstore.SubjectUsers, Pattern: "nope.*"}, time.Now().Unix())
	if unmatched.State != grantStateUnmatched || unmatched.Active || len(unmatched.Permissions) != 0 {
		t.Errorf("an unreadable pattern = %+v, want unmatched and inactive", unmatched)
	}
	// Expiry is the caller's clock.
	expired := authGrantViewOf(jobstore.AuthGrant{
		ID: "g-exp", Subject: jobstore.SubjectUsers, Pattern: authz.PermConfigRead,
		ExpiresAt: time.Now().Unix() - 1, ExpandSnapshot: []string{authz.PermConfigRead},
	}, time.Now().Unix())
	if expired.State != grantStateExpired || expired.Active {
		t.Errorf("an expired grant = %+v, want expired and inactive", expired)
	}
}

// TestAuthGrantCreateValidation: 授权在写入之前先校完（设计 §4「有效期」、决策 10、§2「保存时模式必须
// 至少匹配一项」），所以被拒的请求什么都不变。
func TestAuthGrantCreateValidation(t *testing.T) {
	t.Parallel()
	s := authTestServer(t)
	st := s.jobs.Meta()
	if _, err := st.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "s-live", Agent: "claude", CallerID: "alice",
	}); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if _, err := st.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "s-done", Agent: "claude", CallerID: "alice",
	}); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if _, err := st.SetSessionState("s-done", jobstore.SessionEnded); err != nil {
		t.Fatalf("end session: %v", err)
	}

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown subject", map[string]any{"subject": "worker:w1", "pattern": "tunnel.open"}, http.StatusBadRequest},
		{"empty pattern", map[string]any{"subject": "users", "pattern": " "}, http.StatusBadRequest},
		{"unknown caller", map[string]any{"subject": "caller:carol", "pattern": "tunnel.open"}, http.StatusBadRequest},
		{"unsupported scope", map[string]any{"subject": "caller:alice", "pattern": "tunnel.open", "scope": "project:self"}, http.StatusBadRequest},
		// A pattern that means nothing: it reaches no permission at all, only auth.manage, or
		// only a dangerous permission behind the wildcard (design §2「展开规则」).
		{"pattern matches nothing", map[string]any{"subject": "caller:alice", "pattern": "nope.*"}, http.StatusBadRequest},
		{"pattern reaches only auth.manage", map[string]any{"subject": "caller:alice", "pattern": "auth.*"}, http.StatusBadRequest},
		{"pattern reaches only a dangerous permission", map[string]any{"subject": "caller:alice", "pattern": "memory.*"}, http.StatusBadRequest},
		// A session subject must exist, be alive and carry an expiry of at most 24 hours.
		{"unknown session", map[string]any{"subject": "session:s-nope", "pattern": "tunnel.open", "expires_sec": 3600}, http.StatusNotFound},
		{"session without an expiry", map[string]any{"subject": "session:s-live", "pattern": "tunnel.open"}, http.StatusBadRequest},
		{"session expiry too long", map[string]any{"subject": "session:s-live", "pattern": "tunnel.open", "expires_sec": 90000}, http.StatusBadRequest},
		{"ended session", map[string]any{"subject": "session:s-done", "pattern": "tunnel.open", "expires_sec": 3600}, http.StatusConflict},
		{"dangerous permission for a session", map[string]any{"subject": "session:s-live", "pattern": authz.PermJobSubmit, "expires_sec": 3600}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tc.want, readBody(t, resp))
			}
		})
	}
	// Nothing was written by any of them: only the seed is in the table.
	rows, err := st.ListAuthGrants()
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(rows) != len(authz.Seeded(authz.SeedV1))+len(authz.Seeded(authz.SeedV2))+len(authz.Seeded(authz.SeedV3)) {
		t.Errorf("a refused create wrote a row: %d rows in auth_grants", len(rows))
	}

	// A live session is granted with the owner and the incarnation it has NOW (设计「接口」:
	// 「会话主体自动记下 owner 与化身」), and the expiry is the one that was asked for.
	resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", map[string]any{
		"subject": "session:s-live", "pattern": authz.PermTunnelOpen, "expires_sec": 3600,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("session grant = %d, want 201 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var created authGrantView
	decode(t, resp, &created)
	if created.SessionOwner != "alice" {
		t.Errorf("session_owner = %q, want the session's owner", created.SessionOwner)
	}
	if created.ExpiresAt <= time.Now().Unix() || created.ExpiresAt > time.Now().Unix()+authSessionGrantMaxSec {
		t.Errorf("expires_at = %d, want it within the next 24 hours", created.ExpiresAt)
	}
	if !slices.Equal(created.Permissions, []string{authz.PermTunnelOpen}) {
		t.Errorf("session grant expansion = %v", created.Permissions)
	}
}

// TestAuthGrantCacheRefresh: 数据库授权缓存带版本号（设计 §3「一致性」）：本进程写入后立即刷新
// （上面的用例已覆盖），其他进程写库则在 TTL 内被读到 —— 这里用「直接经 store 写入」模拟另一个进程，
// 因为那样的写入不经过本进程的失效路径。
func TestAuthGrantCacheRefresh(t *testing.T) {
	t.Parallel()
	s := authTestServer(t)
	st := s.jobs.Meta()
	const grant = authz.PermXferManageOthers

	// A long TTL, and a warm picture: the write below must NOT be visible yet.
	s.grantCacheTTL = 30 * time.Second
	if s.effective("alice", "").Has(grant) {
		t.Fatalf("alice must not hold %s before anything is granted", grant)
	}
	before := s.grantsVersion()
	created, err := st.CreateAuthGrant(jobstore.AuthGrant{
		Subject: "caller:alice", Pattern: "xfer.*", GrantedBy: "other-process",
	})
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if s.effective("alice", "").Has(grant) {
		t.Error("another process's write must not be visible before the TTL expires")
	}

	// Once the picture has aged out, the next request picks it up (and reports the newer
	// generation of the grant table).
	wait.Until(t, 5*time.Second, "the grant written by another process to be picked up after the TTL", func() bool {
		s.grantCacheTTL = time.Millisecond
		return s.effective("alice", "").Has(grant)
	})
	if after := s.grantsVersion(); after <= before {
		t.Errorf("grants version = %d after a write, want more than %d", after, before)
	}
	if view := authGrantByID(t, s, created.ID); view.State != grantStateActive {
		t.Errorf("the grant read back as %s, want active", view.State)
	}
}

// TestAuthAuditFilters: GET /v1/auth/audit 按主体 / 权限 / 授权 id 过滤，条数带上限。审计的写入是
// 计划 T2.4 的事，所以这里直接经存储层插行 —— 过滤规则本身才是本任务的交付。
func TestAuthAuditFilters(t *testing.T) {
	t.Parallel()
	s := authTestServer(t)
	st := s.jobs.Meta()
	for _, e := range []jobstore.AuthAuditEntry{
		{Action: "grant.create", Actor: "root", Subject: "caller:alice", Permission: authz.PermTunnelOpen, GrantID: "g1"},
		{Action: "request.denied", Actor: "alice", Subject: "caller:alice", Permission: authz.PermConfigWrite,
			GrantID: "g1", Method: http.MethodPost, Path: "/v1/config/reload", Status: http.StatusForbidden, Reason: "permission required"},
		{Action: "grant.revoke", Actor: "root", Subject: "caller:bob", Permission: authz.PermTunnelOpen, GrantID: "g2"},
	} {
		if err := st.InsertAuthAudit(e); err != nil {
			t.Fatalf("insert audit entry: %v", err)
		}
	}
	var all struct {
		Entries []jobstore.AuthAuditEntry `json:"entries"`
		Count   int                       `json:"count"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/auth/audit", "tok-root", nil), &all)
	if all.Count != 3 || len(all.Entries) != 3 {
		t.Fatalf("audit = %d entries (%d), want 3", len(all.Entries), all.Count)
	}
	// Newest first.
	if all.Entries[0].Action != "grant.revoke" {
		t.Errorf("the newest entry is %s, want grant.revoke first", all.Entries[0].Action)
	}
	var one struct {
		Entries []jobstore.AuthAuditEntry `json:"entries"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/auth/audit?permission="+authz.PermConfigWrite, "tok-root", nil), &one)
	if len(one.Entries) != 1 || one.Entries[0].Status != http.StatusForbidden {
		t.Fatalf("filtered audit = %+v, want the refused request", one.Entries)
	}
	decode(t, do(t, s, http.MethodGet, "/v1/auth/audit?grant_id=g1&limit=1", "tok-root", nil), &one)
	if len(one.Entries) != 1 {
		t.Errorf("limit=1 returned %d entries", len(one.Entries))
	}
	if resp := do(t, s, http.MethodGet, "/v1/auth/audit?limit=zero", "tok-root", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("limit=zero = %d, want 400", resp.StatusCode)
	}
}

// TestAuthCatalogAndRoutes: 权限目录与路由表（设计「架构」：参考文档由代码生成，运行时也可查）：
// 目录逐项带风险 / 可否授予会话 / 来源，路由表逐条带声明与权限，两者都与代码里的那一份一致。
func TestAuthCatalogAndRoutes(t *testing.T) {
	t.Parallel()
	s := authTestServer(t)

	var cat authCatalogView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/catalog", "tok-alice", nil), &cat)
	byName := map[string]authPermissionCatalogView{}
	for _, p := range cat.Permissions {
		byName[p.Name] = p
	}
	if len(byName) != len(authz.Permissions()) {
		t.Fatalf("catalog = %d entries, want %d", len(byName), len(authz.Permissions()))
	}
	submit := byName[authz.PermJobSubmit]
	if submit.Risk != string(authz.RiskDangerous) || submit.RiskLabel != authz.RiskDangerous.Label() {
		t.Errorf("job.submit risk = %s/%s, want dangerous", submit.Risk, submit.RiskLabel)
	}
	if submit.SessionGrantable || submit.SeedVersion != authz.SeedV1 {
		t.Errorf("job.submit = %+v, want not session-grantable and seeded", submit)
	}
	manage := byName[authz.PermAuthManage]
	if manage.Source != string(authz.SourceRoot) || manage.SessionGrantable {
		t.Errorf("auth.manage = %+v, want root-only and not session-grantable", manage)
	}
	// The presets of design §2 come with what they really hand out.
	if len(cat.Presets) == 0 {
		t.Fatal("the catalog carries no preset")
	}
	for _, preset := range cat.Presets {
		if len(preset.Patterns) == 0 || len(preset.Permissions) == 0 {
			t.Errorf("preset %s = %+v, want patterns and their expansion", preset.Key, preset)
		}
	}

	var routes struct {
		Routes []authRouteView `json:"routes"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/auth/routes", "tok-alice", nil), &routes)
	if len(routes.Routes) != len(routeDecls) {
		t.Errorf("the route table has %d rows, want one per declaration (%d)", len(routes.Routes), len(routeDecls))
	}
	for _, r := range routes.Routes {
		decl, ok := routeDecls[r.Method+" "+r.Path]
		if !ok {
			t.Errorf("route %s %s has no declaration", r.Method, r.Path)
			continue
		}
		if r.User != declUserCategory(decl) {
			t.Errorf("%s %s: category %q, want %q", r.Method, r.Path, r.User, declUserCategory(decl))
		}
		if !slices.Equal(r.Permissions, decl.user.perms) {
			t.Errorf("%s %s: permissions %v, want %v", r.Method, r.Path, r.Permissions, decl.user.perms)
		}
		if r.JobCredential != decl.jobCred {
			t.Errorf("%s %s: job credential %q, want %q", r.Method, r.Path, r.JobCredential, decl.jobCred)
		}
	}
}

// TestAuthExpandPreview: the preview endpoint answers what authz.Expand computes for each subject
// kind, with what the rules kept out, and a 400 (same body shape) when nothing is left. The
// console previews with it instead of re-implementing the expansion rules.
func TestAuthExpandPreview(t *testing.T) {
	t.Parallel()
	s := authTestServer(t)
	get := func(token, query string) (int, authExpandView) {
		resp := do(t, s, http.MethodGet, "/v1/auth/expand?"+query, token, nil)
		code := resp.StatusCode
		var v authExpandView
		decode(t, resp, &v)
		return code, v
	}
	names := func(v authExpandView) []string {
		var out []string
		for _, p := range v.Permissions {
			out = append(out, p.Name)
		}
		return out
	}

	// users: the same names authz.Expand returns, and the wildcard keeps the dangerous ones out.
	code, v := get("tok-root", "pattern=*&subject=users")
	want, wantExcluded, err := authz.Explain("*", authz.SubjectUsers)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || !slices.Equal(names(v), want.Names()) {
		t.Fatalf("users * = %d %v, want %v", code, names(v), want.Names())
	}
	if len(v.Excluded) != len(wantExcluded) || len(v.Excluded) == 0 {
		t.Fatalf("excluded = %+v, want %d entries", v.Excluded, len(wantExcluded))
	}
	reasons := map[string]string{}
	for _, e := range v.Excluded {
		reasons[e.Name] = e.Reason
	}
	if reasons[authz.PermAuthManage] != authz.ExcludedRoot || reasons[authz.PermJobSubmit] != authz.ExcludedDangerousWildcard {
		t.Errorf("reasons = %v, want auth.manage root_only and job.submit dangerous_wildcard", reasons)
	}

	// A literal dangerous name is handed out for users, but is not session-grantable.
	if code, v = get("tok-root", "pattern=job.submit&subject=caller:alice"); code != http.StatusOK || !slices.Equal(names(v), []string{authz.PermJobSubmit}) || v.Kind != "caller" {
		t.Errorf("caller literal = %d %+v", code, v)
	}
	code, v = get("tok-root", "pattern=job.submit&subject=session:s1")
	if code != http.StatusBadRequest || v.Problem != "only_not_session_grantable" || len(v.Permissions) != 0 || v.Error == "" {
		t.Errorf("session literal dangerous = %d %+v, want 400 only_not_session_grantable", code, v)
	}

	// A session subject only expands to the session-grantable permissions.
	code, v = get("tok-root", "pattern=tunnel.*&subject=session:s1")
	sess, _, err := authz.Explain("tunnel.*", authz.SubjectSession)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || v.Subject != "session:s1" || !slices.Equal(names(v), sess.Names()) {
		t.Errorf("session tunnel.* = %d %+v, want %v", code, v, sess.Names())
	}
	for _, p := range v.Permissions {
		if !p.SessionGrantable {
			t.Errorf("session expansion holds %s which is not session-grantable", p.Name)
		}
	}

	// Nothing left: no match, only auth.manage, bad input.
	if code, v = get("tok-root", "pattern=nope.*&subject=users"); code != http.StatusBadRequest || v.Problem != "no_match" {
		t.Errorf("no match = %d %+v", code, v)
	}
	if code, v = get("tok-root", "pattern=auth.manage&subject=users"); code != http.StatusBadRequest || v.Problem != "only_root" {
		t.Errorf("auth.manage = %d %+v", code, v)
	}
	if code, _ = get("tok-root", "subject=users"); code != http.StatusBadRequest {
		t.Errorf("empty pattern = %d, want 400", code)
	}
	if code, _ = get("tok-root", "pattern=tunnel.*&subject=worker:w1"); code != http.StatusBadRequest {
		t.Errorf("unknown subject = %d, want 400", code)
	}

	// Management level: a non-root caller is refused, naming the permission.
	resp := do(t, s, http.MethodGet, "/v1/auth/expand?pattern=tunnel.*&subject=users", "tok-alice", nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(readBody(t, resp), authz.PermAuthManage) {
		t.Errorf("non-root = %d, want 403 naming auth.manage", resp.StatusCode)
	}
}
