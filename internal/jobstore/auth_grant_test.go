package jobstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/authz"
)

// 授权管理的存储半边（计划 T2.1）：新增 / 撤销 / 恢复孤儿 / 代号递增 / 会话锚点 / 审计读取。
// 判定不在这一层，所以这里只钉「行怎么写、什么被拒、代号怎么动」。

// TestCreateAuthGrantStoresTheExpansion: 新增授权把展开快照与行一起写入（设计 §2「保存时模式必须
// 至少匹配一项，并保存展开快照」），并递增授权表代号（设计「一致性」：入口层的缓存据此失效）。
func TestCreateAuthGrantStoresTheExpansion(t *testing.T) {
	s := openTest(t)
	before, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)

	g, err := s.CreateAuthGrant(AuthGrant{
		Subject: "caller:agent-dev", Pattern: "tunnel.*", GrantedBy: "default", Note: "container tunnel",
	})
	assert.NoErr(t, err)
	assert.True(t, strings.HasPrefix(g.ID, "grant-"), "generated id %q is not a manager id", g.ID)
	assert.Eq(t, "*", g.Scope, "phase 1 grants the whole scope")
	assert.Gt(t, g.GrantedAt, int64(0))
	// The snapshot is the expansion in its stored (sorted) shape; which permissions a pattern
	// means is internal/authz's rule, the store's job is to keep row and snapshot together.
	assert.Eq(t, []string{authz.PermTunnelHosted, authz.PermTunnelManageOthers, authz.PermTunnelOpen},
		g.ExpandSnapshot)

	// Round trip: every column the row carries reads back, and the grant is active for its
	// subject.
	stored, ok, err := s.GetAuthGrant(g.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "caller:agent-dev", stored.Subject)
	assert.Eq(t, "tunnel.*", stored.Pattern)
	assert.Eq(t, "default", stored.GrantedBy)
	assert.Eq(t, "container tunnel", stored.Note)
	assert.Eq(t, g.ExpandSnapshot, stored.ExpandSnapshot)
	assert.Eq(t, int64(0), stored.RevokedAt)
	assert.Eq(t, int64(0), stored.OrphanedAt)
	active, err := s.ListActiveAuthGrants("caller:agent-dev")
	assert.NoErr(t, err)
	assert.Len(t, active, 1)
	all, err := s.ListActiveAuthGrantsAll()
	assert.NoErr(t, err)
	assert.Len(t, all, len(allSeededGrants())+1, "the seed plus the new grant")

	after, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, before+1, after, "a write must move the grant-table generation")

	// A pattern that means nothing is refused (设计 §2), and so is a subject the model does
	// not have; neither writes a row nor moves the generation.
	if _, err := s.CreateAuthGrant(AuthGrant{Subject: "caller:agent-dev", Pattern: "nope.*"}); !errors.Is(err, authz.ErrNoMatch) {
		t.Errorf("CreateAuthGrant(nope.*) = %v, want ErrNoMatch", err)
	}
	if _, err := s.CreateAuthGrant(AuthGrant{Subject: "worker:w1", Pattern: authz.PermTunnelOpen}); !errors.Is(err, authz.ErrUnknownSubject) {
		t.Errorf("CreateAuthGrant(worker:w1) = %v, want ErrUnknownSubject", err)
	}
	if _, err := s.CreateAuthGrant(AuthGrant{Subject: "caller:agent-dev", Pattern: ""}); err == nil {
		t.Error("CreateAuthGrant(empty pattern) = nil, want an error")
	}
	// job.submit is dangerous and may not be granted to a session, so a session grant of it
	// matches nothing at all.
	if _, err := s.CreateAuthGrant(AuthGrant{Subject: "session:s1", Pattern: authz.PermJobSubmit}); !errors.Is(err, authz.ErrNoMatch) {
		t.Errorf("CreateAuthGrant(session + job.submit) = %v, want ErrNoMatch", err)
	}
	after2, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, after, after2, "a refused create must not move the generation")

	if _, ok, err := s.GetAuthGrant("grant-nope"); err != nil || ok {
		t.Errorf("GetAuthGrant(unknown) = ok %t, err %v; want false and no error", ok, err)
	}
}

// TestRevokeOrphanAndActivate: 撤销写 revoked_at 而不删行；孤儿写入 orphaned_at 并在重新出现时不自动恢复
// （只有 activate 才恢复）；三者都幂等，并且只在真的改了行时动代号（设计 §2、接口 DELETE /
// activate）。
func TestRevokeOrphanAndActivate(t *testing.T) {
	s := openTest(t)
	g, err := s.CreateAuthGrant(AuthGrant{Subject: "caller:alice", Pattern: authz.PermTunnelOpen, GrantedBy: "default"})
	assert.NoErr(t, err)

	// A caller grant for somebody who is not in the configuration: orphaned, and out of the
	// active set the resolver reads.
	g2, err := s.CreateAuthGrant(AuthGrant{Subject: "caller:gone", Pattern: authz.PermConfigRead, GrantedBy: "default"})
	assert.NoErr(t, err)
	version, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	n, err := s.OrphanAuthGrants([]string{g2.ID})
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)
	orphaned, ok, err := s.GetAuthGrant(g2.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Gt(t, orphaned.OrphanedAt, int64(0))
	active, err := s.ListActiveAuthGrants("caller:gone")
	assert.NoErr(t, err)
	assert.Len(t, active, 0, "an orphaned grant is inert")
	// Idempotent: the same id marked twice changes nothing more (and the generation stays).
	n, err = s.OrphanAuthGrants([]string{g2.ID, "grant-nope"})
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
	v2, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, version+1, v2, "only the first pass wrote a row")

	// The caller coming back does NOT clear the mark (设计 §2：同一 id 重新出现时仍保持停用).
	restored, ok, err := s.ActivateAuthGrant(g2.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, int64(0), restored.OrphanedAt, "activate restores the grant")
	v3, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, v2+1, v3)
	// Activating an active row is a no-op: no write, no generation move.
	_, ok, err = s.ActivateAuthGrant(g2.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	v4, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, v3, v4)
	activeGone, err := s.ListActiveAuthGrants("caller:gone")
	assert.NoErr(t, err)
	assert.Len(t, activeGone, 1)

	// Revoking keeps the row (the audit view and 「撤销后重启不还原」 both need it), records
	// who did it, and takes it out of the active set.
	revoked, ok, err := s.RevokeAuthGrant(g.ID, "alice")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Gt(t, revoked.RevokedAt, int64(0))
	assert.Eq(t, "alice", revoked.RevokedBy)
	active, err = s.ListActiveAuthGrants("caller:alice")
	assert.NoErr(t, err)
	assert.Len(t, active, 0)
	// Revoking twice is idempotent — the first revocation's actor and time stay.
	again, ok, err := s.RevokeAuthGrant(g.ID, "bob")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, revoked.RevokedAt, again.RevokedAt)
	assert.Eq(t, "alice", again.RevokedBy)
	// A revoked row is not resurrected by activate either.
	_, _, err = s.ActivateAuthGrant(g.ID)
	assert.NoErr(t, err)
	stillRevoked, ok, err := s.GetAuthGrant(g.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Gt(t, stillRevoked.RevokedAt, int64(0))
	// An orphan pass never touches a revoked row.
	n, err = s.OrphanAuthGrants([]string{g.ID})
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
	// An unknown id is reported as absent rather than as an error.
	if _, ok, err := s.RevokeAuthGrant("grant-nope", "alice"); err != nil || ok {
		t.Errorf("RevokeAuthGrant(unknown) = ok %t, err %v; want false and no error", ok, err)
	}
	if _, ok, err := s.ActivateAuthGrant("grant-nope"); err != nil || ok {
		t.Errorf("ActivateAuthGrant(unknown) = ok %t, err %v; want false and no error", ok, err)
	}
}

// TestSessionGrantAnchor: 会话主体登记授权时要绑定的 owner 与化身，以及判定授权是否生效要看的状态，
// 都从会话行读（设计「接口」：「会话主体自动记下 owner 与化身」；§4「生效条件」），会话不存在时报
// absent 而不是报错。
//
// The incarnation itself is the session lifecycle's (design §4: the column is planted on
// every write that leaves `ended`, which is this task's jobstore half); the anchor reports
// exactly what the row holds, so a session without one yet binds "" rather than a made-up
// value.
func TestSessionGrantAnchor(t *testing.T) {
	s := openTest(t)
	if _, err := s.UpsertAgentSession(AgentSession{SessionID: "s-anchor", Agent: "claude", CallerID: "alice"}); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	a, ok, err := s.SessionGrantAnchor("s-anchor")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "alice", a.Owner)
	assert.Eq(t, sessionIncarnations(t, s)["s-anchor"], a.Incarnation, "the anchor reports the stored incarnation")
	assert.Neq(t, "", a.State)
	assert.Eq(t, int64(0), a.EndedAt)
	assert.False(t, a.Ended(), "a running session has not ended")
	// A session that ended reports it, and so does one taken over after it ended (the terminal
	// stamp survives the take-over — design §4).
	if _, err := s.SetSessionState("s-anchor", SessionEnded); err != nil {
		t.Fatalf("end session: %v", err)
	}
	ended, ok, err := s.SessionGrantAnchor("s-anchor")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, ended.Ended(), "an ended session must report that it has ended")
	assert.True(t, ended.EndedAt != 0, "ending a session stamps ended_at")
	if _, ok, err := s.SessionGrantAnchor("s-nope"); err != nil || ok {
		t.Errorf("SessionGrantAnchor(unknown) = ok %t, err %v; want false and no error", ok, err)
	}
	if _, ok, err := s.SessionGrantAnchor(""); err != nil || ok {
		t.Errorf("SessionGrantAnchor(empty) = ok %t, err %v; want false and no error", ok, err)
	}
}

// TestAuthAuditRoundTripAndFilters: 审计行只追加（最新的在前），并按主体 / 权限 / 授权 id 过滤，
// 条数带上限（接口 GET /v1/auth/audit、设计「审计与使用计数」）。写入本身是 T2.4 的事，这里只钉交付给
// 读路径的形状。
func TestAuthAuditRoundTripAndFilters(t *testing.T) {
	s := openTest(t)
	entries := []AuthAuditEntry{
		{Action: "grant.create", Actor: "default", Subject: "caller:alice", Permission: authz.PermTunnelOpen,
			GrantID: "grant-1", Reason: "container needs the device"},
		{Action: "request.denied", Actor: "alice", Subject: "caller:alice", Permission: authz.PermConfigWrite,
			GrantID: "grant-1", Method: "POST", Path: "/v1/config/reload", Status: 403, Reason: "permission required"},
		{Action: "grant.revoke", Actor: "default", Subject: "caller:bob", Permission: authz.PermTunnelOpen,
			GrantID: "grant-2"},
	}
	for _, e := range entries {
		assert.NoErr(t, s.InsertAuthAudit(e))
	}
	if err := s.InsertAuthAudit(AuthAuditEntry{Actor: "default"}); err == nil {
		t.Error("InsertAuthAudit(no action) = nil, want an error")
	}

	all, err := s.ListAuthAudit(AuthAuditFilter{})
	assert.NoErr(t, err)
	assert.Len(t, all, len(entries))
	for _, e := range all {
		assert.Gt(t, e.At, int64(0), "a stored entry always carries a time")
	}
	// Newest first: grant.create was written first, so it is last.
	assert.Eq(t, "grant.create", all[len(all)-1].Action)

	bySubject, err := s.ListAuthAudit(AuthAuditFilter{Subject: "caller:bob"})
	assert.NoErr(t, err)
	assert.Len(t, bySubject, 1)
	assert.Eq(t, "grant.revoke", bySubject[0].Action)
	byPermission, err := s.ListAuthAudit(AuthAuditFilter{Permission: authz.PermConfigWrite})
	assert.NoErr(t, err)
	assert.Len(t, byPermission, 1)
	assert.Eq(t, 403, byPermission[0].Status)
	assert.Eq(t, "/v1/config/reload", byPermission[0].Path)
	byGrant, err := s.ListAuthAudit(AuthAuditFilter{GrantID: "grant-1"})
	assert.NoErr(t, err)
	assert.Len(t, byGrant, 2)
	// Filters combine, and a combination that matches nothing is an empty list, not an error.
	both, err := s.ListAuthAudit(AuthAuditFilter{Subject: "caller:alice", Permission: authz.PermTunnelOpen})
	assert.NoErr(t, err)
	assert.Len(t, both, 1)
	none, err := s.ListAuthAudit(AuthAuditFilter{Subject: "caller:carol"})
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
	// The limit is honoured and clamped to the documented maximum.
	one, err := s.ListAuthAudit(AuthAuditFilter{Limit: 1})
	assert.NoErr(t, err)
	assert.Len(t, one, 1)
	huge, err := s.ListAuthAudit(AuthAuditFilter{Limit: MaxAuthAuditLimit * 100})
	assert.NoErr(t, err)
	assert.Len(t, huge, len(entries))
}
