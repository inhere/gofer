package jobstore

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/authz"
)

// 会话授权的续期，存储半边（设计 §4「有效期」：会话授权最长 24 小时，到期后不重新授权就没得用）。
// 这里钉三件事：只动 expires_at、不生效的行被拒绝（原因可区分）、旧的 / 新的到期时刻进审计。

// renewTestNow 是这组用例里存储层看到的「现在」（unix 秒）：到期判定完全按它走，不依赖真实时钟。
const renewTestNow = int64(1_760_000_000)

// renewClockAt 让存储层的时间停在 unix 秒 at。
func renewClockAt(at int64) func() time.Time {
	return func() time.Time { return time.Unix(at, 0) }
}

// sessionGrantFor 登记一条会话语体的授权（带到期时间），返回写入的行。
func sessionGrantFor(t *testing.T, s *Store, sid string, expiresAt int64) AuthGrant {
	t.Helper()
	g, err := s.CreateAuthGrant(AuthGrant{
		Subject:            authz.SubjectOf(authz.SubjectSession, sid),
		Pattern:            authz.PermTunnelOpen,
		ExpiresAt:          expiresAt,
		SessionOwner:       "alice",
		SessionIncarnation: "inc-1",
		GrantedBy:          "root",
		Note:               "the device in the container",
	})
	assert.NoErr(t, err)
	return g
}

// TestRenewAuthGrantExtendsExpiryOnly: 续期把到期时间往后挪，别的列一个字都不动 —— 续期是「同一条授权
// 再往后挪」，不是新授权（模式、展开快照、owner、化身、授权人与授权时刻都留着），并递增授权表代号
// （入口层的缓存据此失效，设计「一致性」）。
func TestRenewAuthGrantExtendsExpiryOnly(t *testing.T) {
	s := openTest(t)
	s.SetClock(renewClockAt(renewTestNow))
	g := sessionGrantFor(t, s, "s-renew", renewTestNow+600)
	version, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)

	renewed, err := s.RenewAuthGrant(g.ID, renewTestNow+3600, "alice")
	assert.NoErr(t, err)
	assert.Eq(t, renewTestNow+3600, renewed.ExpiresAt)

	// 读回来与返回值一致，而除 expires_at 外的每一列都还是授权时那一份。
	stored, ok, err := s.GetAuthGrant(g.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, renewed.ExpiresAt, stored.ExpiresAt)
	assert.Eq(t, authz.SubjectSessionPrefix+"s-renew", stored.Subject)
	assert.Eq(t, authz.PermTunnelOpen, stored.Pattern)
	assert.Eq(t, g.Scope, stored.Scope)
	assert.Eq(t, g.SessionOwner, stored.SessionOwner)
	assert.Eq(t, g.SessionIncarnation, stored.SessionIncarnation)
	assert.Eq(t, g.GrantedBy, stored.GrantedBy)
	assert.Eq(t, g.GrantedAt, stored.GrantedAt)
	assert.Eq(t, g.ExpandSnapshot, stored.ExpandSnapshot, "续期不改权限范围")
	assert.Eq(t, g.Note, stored.Note)
	assert.Eq(t, int64(0), stored.RevokedAt)
	assert.Eq(t, int64(0), stored.OrphanedAt)
	assert.Eq(t, g.UseCount, stored.UseCount, "续期不动使用计数")

	// 代号动了：入口层的缓存据此重新读。
	after, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, version+1, after)

	// 续期过的授权对它的会话主体仍然生效（新的到期时间还在将来）。
	active, err := s.ListActiveAuthGrants(authz.SubjectSessionPrefix + "s-renew")
	assert.NoErr(t, err)
	assert.Len(t, active, 1)

	// 反复续期是允许的：每次都只挪到期时间，再进一次代号。
	again, err := s.RenewAuthGrant(stored.ID, renewTestNow+7200, "bob")
	assert.NoErr(t, err)
	assert.Eq(t, renewTestNow+7200, again.ExpiresAt)
	v3, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, after+1, v3)
}

// TestRenewAuthGrantRefusesRevokedAndExpired: 已撤销、已过期与不存在的行都拒绝，而且原因可区分、什么也
// 不改（行留在原样，代号不动）；到期判定与判定同口径：expires_at <= now 就是过期。
func TestRenewAuthGrantRefusesRevokedAndExpired(t *testing.T) {
	s := openTest(t)
	s.SetClock(renewClockAt(renewTestNow))

	// 已过期：到期时刻正好是「现在」也算过期（与 expandGrantsFor 的口径一致）。
	expired := sessionGrantFor(t, s, "s-expired", renewTestNow-10)
	boundary := sessionGrantFor(t, s, "s-boundary", renewTestNow)
	// 已撤销：撤销是终态，续期不会让它复活。
	revoked := sessionGrantFor(t, s, "s-revoked", renewTestNow+600)
	revokedRow, ok, err := s.RevokeAuthGrant(revoked.ID, "root")
	assert.NoErr(t, err)
	assert.True(t, ok)

	version, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	auditBefore, err := s.ListAuthAudit(AuthAuditFilter{})
	assert.NoErr(t, err)

	if _, err := s.RenewAuthGrant(expired.ID, renewTestNow+3600, "alice"); !errors.Is(err, ErrAuthGrantExpired) {
		t.Errorf("renew an expired grant = %v, want ErrAuthGrantExpired", err)
	}
	if _, err := s.RenewAuthGrant(boundary.ID, renewTestNow+3600, "alice"); !errors.Is(err, ErrAuthGrantExpired) {
		t.Errorf("renew a grant expiring right now = %v, want ErrAuthGrantExpired", err)
	}
	if _, err := s.RenewAuthGrant(revoked.ID, renewTestNow+3600, "alice"); !errors.Is(err, ErrAuthGrantRevoked) {
		t.Errorf("renew a revoked grant = %v, want ErrAuthGrantRevoked", err)
	}
	if _, err := s.RenewAuthGrant("grant-nope", renewTestNow+3600, "alice"); !errors.Is(err, ErrAuthGrantNotFound) {
		t.Errorf("renew an unknown grant = %v, want ErrAuthGrantNotFound", err)
	}
	// 明显的输入错误不是上面三种情形，但也不能静默写进去。
	if _, err := s.RenewAuthGrant("  ", renewTestNow+3600, "alice"); err == nil {
		t.Error("renew with an empty id = nil, want an error")
	}
	if _, err := s.RenewAuthGrant(revoked.ID, 0, "alice"); err == nil {
		t.Error("renew with expires_at = 0 = nil, want an error (a session grant always has a deadline)")
	}
	if _, err := s.RenewAuthGrant(revoked.ID, -1, "alice"); err == nil {
		t.Error("renew with a negative expires_at = nil, want an error")
	}

	// 一次拒绝都不写行、不动代号、不写审计。
	for id, want := range map[string]int64{
		expired.ID:  expired.ExpiresAt,
		boundary.ID: boundary.ExpiresAt,
		revoked.ID:  revokedRow.ExpiresAt,
	} {
		row, ok, err := s.GetAuthGrant(id)
		assert.NoErr(t, err)
		assert.True(t, ok)
		assert.Eq(t, want, row.ExpiresAt, "a refused renewal leaves expires_at alone")
	}
	after, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, version, after, "a refused renewal must not move the generation")
	auditAfter, err := s.ListAuthAudit(AuthAuditFilter{})
	assert.NoErr(t, err)
	assert.Len(t, auditAfter, len(auditBefore), "a refused renewal writes no audit row")
}

// TestRenewAuthGrantWritesAudit: 续期写一条审计（action `grant.renew`，与其他授权写路径同表同字段），
// 把旧的与新的到期时刻一起记下来，谁做的记在 actor 上。
func TestRenewAuthGrantWritesAudit(t *testing.T) {
	s := openTest(t)
	s.SetClock(renewClockAt(renewTestNow))
	g := sessionGrantFor(t, s, "s-audit", renewTestNow+600)

	if _, err := s.RenewAuthGrant(g.ID, renewTestNow+3600, "alice"); err != nil {
		t.Fatalf("renew: %v", err)
	}
	rows, err := s.ListAuthAudit(AuthAuditFilter{GrantID: g.ID})
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
	e := rows[0]
	assert.Eq(t, renewAuditAction, e.Action)
	assert.Eq(t, "alice", e.Actor, "who renewed is the audit actor")
	assert.Eq(t, authz.SubjectSessionPrefix+"s-audit", e.Subject)
	assert.Eq(t, authz.PermTunnelOpen, e.Permission, "the audit names the granted pattern")
	assert.Eq(t, g.ID, e.GrantID)
	assert.Eq(t, renewTestNow, e.At)
	// 旧的 / 新的到期时刻都在：两个数字都能在 reason 里找到（auth_audit 没有专门的两列）。
	assert.Contains(t, e.Reason, strconv.FormatInt(renewTestNow+600, 10), "the old deadline is recorded")
	assert.Contains(t, e.Reason, strconv.FormatInt(renewTestNow+3600, 10), "the new deadline is recorded")
	assert.True(t, strings.Contains(e.Reason, "->"), "reason shows old -> new, got %q", e.Reason)

	// 再续一次就再记一条（审计只追加，旧记录不变），这次 actor 与到期时刻都是新的。
	if _, err := s.RenewAuthGrant(g.ID, renewTestNow+7200, "bob"); err != nil {
		t.Fatalf("renew again: %v", err)
	}
	rows, err = s.ListAuthAudit(AuthAuditFilter{GrantID: g.ID})
	assert.NoErr(t, err)
	assert.Len(t, rows, 2)
	assert.Eq(t, "bob", rows[0].Actor, "newest first")
	assert.Contains(t, rows[0].Reason, strconv.FormatInt(renewTestNow+7200, 10))

	// 本来没有到期时间的行续期时，reason 的旧值写成 none，而不是一个会被读成「1970 年到期」的 0。
	// （会话授权必填到期时间，所以这条形状只出现在 users / caller 行上。）
	common, err := s.CreateAuthGrant(AuthGrant{
		Subject: "caller:alice", Pattern: authz.PermTunnelOpen, GrantedBy: "root"})
	assert.NoErr(t, err)
	if _, err := s.RenewAuthGrant(common.ID, renewTestNow+3600, "alice"); err != nil {
		t.Fatalf("renew a grant without a deadline: %v", err)
	}
	rows, err = s.ListAuthAudit(AuthAuditFilter{GrantID: common.ID})
	assert.NoErr(t, err)
	assert.Len(t, rows, 1)
	assert.Eq(t, "expires_at none -> "+strconv.FormatInt(renewTestNow+3600, 10), rows[0].Reason)
}
