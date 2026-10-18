package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// 会话授权续期的 HTTP 半边（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md
// §4「有效期」，计划 T2.3）。三个问题：
//
//   - 成功续期：只把 expires_at 往后挪，返回更新后的那一行（含 effective 与新的 expires_at），权限
//     照旧生效，而且没有写下第二条授权；
//   - 只对「当前生效」的会话授权：「不生效」的判定与列表接口显示 effective 的判定同源（撤销、过期、
//     会话结束、化身变了、owner 对不上都拒绝），需要重新授权而不是续期；
//   - 权限：仍然只有根管理员（auth.manage），非 session 主体、不存在的 id、配置推出的只读项各有明确的
//     4xx，worker 与 job 凭据、没有 auth.manage 的 user 一律 403。

// renewSessionGrant 以根管理员身份请求一条授权的续期，返回原始响应。
func renewSessionGrant(t *testing.T, s *Server, id string, expiresSec any) *http.Response {
	t.Helper()
	body := map[string]any{}
	if expiresSec != nil {
		body["expires_sec"] = expiresSec
	}
	return do(t, s, http.MethodPost, "/v1/auth/grants/"+id+"/renew", "tok-root", body)
}

// grantRowsOf 读出某个主体的全部授权行，直接问存储层。
func grantRowsOf(t *testing.T, st *jobstore.Store, subject string) []jobstore.AuthGrant {
	t.Helper()
	rows, err := st.ListAuthGrants()
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	var out []jobstore.AuthGrant
	for _, row := range rows {
		if row.Subject == subject {
			out = append(out, row)
		}
	}
	return out
}

// sessionAnchorOf 读出一个会话现在的 owner 与化身，直接问存储层。
func sessionAnchorOf(t *testing.T, st *jobstore.Store, sid string) jobstore.SessionGrantAnchor {
	t.Helper()
	a, ok, err := st.SessionGrantAnchor(sid)
	if err != nil || !ok {
		t.Fatalf("session anchor %s: ok=%v err=%v", sid, ok, err)
	}
	return a
}

// TestSessionGrantRenewExtendsALiveGrant: 续期把仍然生效的会话授权往后挪一段，返回的那一行是原来
// 同一条（范围、快照、owner、化身、授权人一个没动），权限立刻照旧生效，审计记下旧 / 新两个到期时刻。
func TestSessionGrantRenewExtendsALiveGrant(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	st := s.jobs.Meta()
	const perm = authz.PermSessionReplyOthers
	registerSessionAs(t, s, "tok-alice", "sess-renew")
	grant := createSessionGrant(t, s, "sess-renew", perm, 60)
	original, ok, err := st.GetAuthGrant(grant.ID)
	if err != nil || !ok {
		t.Fatalf("read the grant before the renewal: ok=%v err=%v", ok, err)
	}

	const extendSec = 4 * 3600
	before := time.Now().Unix()
	resp := renewSessionGrant(t, s, grant.ID, extendSec)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("renew = %d, want 200 (%s)", resp.StatusCode, readBody(t, resp))
	}
	var renewed authGrantView
	decode(t, resp, &renewed)

	if renewed.ID != grant.ID {
		t.Errorf("the renewal answered with %s, want the same row %s", renewed.ID, grant.ID)
	}
	if renewed.ExpiresAt < before+extendSec || renewed.ExpiresAt > time.Now().Unix()+extendSec {
		t.Errorf("expires_at = %d, want about now+%d", renewed.ExpiresAt, extendSec)
	}
	if !renewed.Effective || renewed.State != grantStateActive {
		t.Errorf("the renewed grant = %+v, want it in force right away", renewed)
	}
	// 续期是「同一条授权往后挪」，不是新授权：除了到期时间，一个字段都不该变（设计 §2）。
	if renewed.Pattern != grant.Pattern || renewed.SessionOwner != grant.SessionOwner ||
		renewed.SessionIncarnation != grant.SessionIncarnation || renewed.GrantedBy != grant.GrantedBy ||
		renewed.Scope != grant.Scope || !slices.Equal(renewed.Snapshot, grant.Snapshot) {
		t.Errorf("the renewal changed the grant beyond its deadline:\n got %+v\nwas %+v", renewed, grant)
	}

	// 一条行，不是两条；到期时间就是接口回报的那个。
	rows := grantRowsOf(t, st, "session:sess-renew")
	if len(rows) != 1 {
		t.Fatalf("the session has %d grant rows, want the one renewed", len(rows))
	}
	if rows[0].ExpiresAt != renewed.ExpiresAt {
		t.Errorf("the stored expires_at = %d, want the renewed %d", rows[0].ExpiresAt, renewed.ExpiresAt)
	}
	if rows[0].Pattern != original.Pattern || !slices.Equal(rows[0].ExpandSnapshot, original.ExpandSnapshot) {
		t.Errorf("the stored row changed its pattern or snapshot: %+v (was %+v)", rows[0], original)
	}

	// 权限照旧生效，而且列表接口看到的是同一行、同一个到期时间。
	if !sessionGrantHeld(t, s, "tok-alice", "sess-renew", perm) {
		t.Errorf("alice lost %s over the renewal", perm)
	}
	if got := authGrantByID(t, s, grant.ID); got.ExpiresAt != renewed.ExpiresAt || !got.Effective {
		t.Errorf("the listing shows %+v, want the renewed row in force", got)
	}

	// 审计：action grant.renew，actor 是续期的人，reason 里同时有旧的和新的到期时刻。
	entries, err := st.ListAuthAudit(jobstore.AuthAuditFilter{GrantID: grant.ID})
	if err != nil {
		t.Fatalf("list the audit: %v", err)
	}
	var renew *jobstore.AuthAuditEntry
	for i := range entries {
		if entries[i].Action == "grant.renew" {
			renew = &entries[i]
		}
	}
	if renew == nil {
		t.Fatalf("the renewal wrote no grant.renew audit entry: %+v", entries)
	}
	if renew.Actor != "root" || renew.Subject != "session:sess-renew" {
		t.Errorf("the renewal audit = %+v, want the root administrator as the actor of the session", *renew)
	}
	if !strings.Contains(renew.Reason, strconv.FormatInt(grant.ExpiresAt, 10)) ||
		!strings.Contains(renew.Reason, strconv.FormatInt(renewed.ExpiresAt, 10)) {
		t.Errorf("the renewal audit reason = %q, want the old (%d) and the new (%d) deadline",
			renew.Reason, grant.ExpiresAt, renewed.ExpiresAt)
	}
}

// TestSessionGrantRenewRefusals: 续期只对「当前生效」的会话授权 —— 撤销 / 过期 / 会话结束 / 化身变化 /
// owner 变化都是一句话「需要重新授权」的 409，非 session 主体 400，不存在的 id 404，配置推出的只读项
// 409；每一条都什么都不写（到期时间原样）。
func TestSessionGrantRenewRefusals(t *testing.T) {
	t.Parallel()
	const perm = authz.PermSessionReplyOthers

	// live 是「经接口授权一条生效的会话授权」的公共前戏（会话已由每个用例先登记）。
	live := func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
		t.Helper()
		return createSessionGrant(t, s, sid, perm, 3600).ID
	}

	cases := []struct {
		name       string
		sid        string
		setup      func(t *testing.T, s *Server, st *jobstore.Store, sid string) string
		wantStatus int
		wantDetail string
	}{
		{
			name: "revoked", sid: "sess-renew-revoked",
			setup: func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
				id := live(t, s, st, sid)
				if resp := do(t, s, http.MethodDelete, "/v1/auth/grants/"+id, "tok-root", nil); resp.StatusCode != http.StatusOK {
					t.Fatalf("revoke = %d (%s)", resp.StatusCode, readBody(t, resp))
				}
				return id
			},
			wantStatus: http.StatusConflict, wantDetail: "was revoked",
		},
		{
			name: "expired", sid: "sess-renew-expired",
			setup: func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
				anchor := sessionAnchorOf(t, st, sid)
				row, err := st.CreateAuthGrant(jobstore.AuthGrant{
					Subject: "session:" + sid, Pattern: perm, GrantedBy: "root",
					SessionOwner: anchor.Owner, SessionIncarnation: anchor.Incarnation,
					ExpiresAt: time.Now().Unix() - 1,
				})
				if err != nil {
					t.Fatalf("write the expired grant: %v", err)
				}
				s.invalidateDBGrants()
				return row.ID
			},
			wantStatus: http.StatusConflict, wantDetail: "expired at",
		},
		{
			name: "the session has ended", sid: "sess-renew-ended",
			setup: func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
				id := live(t, s, st, sid)
				if _, err := st.SetSessionState(sid, jobstore.SessionEnded); err != nil {
					t.Fatalf("end the session: %v", err)
				}
				return id
			},
			wantStatus: http.StatusConflict, wantDetail: "has ended",
		},
		{
			name: "the session is a new life", sid: "sess-renew-revive",
			setup: func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
				id := live(t, s, st, sid)
				if _, err := st.SetSessionState(sid, jobstore.SessionEnded); err != nil {
					t.Fatalf("end the session: %v", err)
				}
				// 同一个 id 重新登记就是一次新会话：化身轮换，旧授权的化身对不上（设计 §4）。
				if _, err := st.UpsertAgentSession(jobstore.AgentSession{
					SessionID: sid, Agent: "claude", CallerID: "alice",
				}); err != nil {
					t.Fatalf("revive the session: %v", err)
				}
				return id
			},
			wantStatus: http.StatusConflict, wantDetail: "incarnation",
		},
		{
			name: "the session belongs to somebody else", sid: "sess-renew-owner",
			setup: func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
				anchor := sessionAnchorOf(t, st, sid)
				row, err := st.CreateAuthGrant(jobstore.AuthGrant{
					Subject: "session:" + sid, Pattern: perm, GrantedBy: "root",
					SessionOwner: "bob", SessionIncarnation: anchor.Incarnation,
					ExpiresAt: time.Now().Unix() + 3600,
				})
				if err != nil {
					t.Fatalf("write the grant bound to another owner: %v", err)
				}
				s.invalidateDBGrants()
				return row.ID
			},
			wantStatus: http.StatusConflict, wantDetail: "belongs to alice",
		},
		{
			name: "not a session grant", sid: "sess-renew-caller",
			setup: func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
				resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", map[string]any{
					"subject": "caller:alice", "pattern": authz.PermTunnelOpen,
				})
				if resp.StatusCode != http.StatusCreated {
					t.Fatalf("create the caller grant: %d (%s)", resp.StatusCode, readBody(t, resp))
				}
				var created authGrantView
				decode(t, resp, &created)
				return created.ID
			},
			wantStatus: http.StatusBadRequest, wantDetail: "only a session",
		},
		{
			name: "an id that is no row", sid: "sess-renew-unknown",
			setup: func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
				return "grant-nope"
			},
			wantStatus: http.StatusNotFound, wantDetail: "does not exist",
		},
		{
			name: "a config-derived entry", sid: "sess-renew-config",
			setup: func(t *testing.T, s *Server, st *jobstore.Store, sid string) string {
				return configGrantPrefix + jobstore.SubjectUsers
			},
			wantStatus: http.StatusConflict, wantDetail: "read-only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sessionGrantServer(t)
			st := s.jobs.Meta()
			registerSessionAs(t, s, "tok-alice", tc.sid)
			id := tc.setup(t, s, st, tc.sid)
			original, exists, err := st.GetAuthGrant(id)
			if err != nil {
				t.Fatalf("read the row before the renewal: %v", err)
			}

			resp := renewSessionGrant(t, s, id, 3600)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("renew = %d, want %d (%s)", resp.StatusCode, tc.wantStatus, readBody(t, resp))
			}
			if body := readBody(t, resp); !strings.Contains(body, tc.wantDetail) {
				t.Errorf("the refusal does not say %q: %s", tc.wantDetail, body)
			}

			// 被拒的续期什么都不写：行还在，到期时间一点没动。
			if !exists {
				return
			}
			after, ok, err := st.GetAuthGrant(id)
			if err != nil || !ok {
				t.Fatalf("the refused renewal removed the row: ok=%v err=%v", ok, err)
			}
			if after.ExpiresAt != original.ExpiresAt || after.RevokedAt != original.RevokedAt {
				t.Errorf("the refused renewal wrote the row: %+v (was %+v)", after, original)
			}
		})
	}
}

// TestSessionGrantRenewExpiresBounds: expires_sec 必填、1 秒到 24 小时（与新建会话授权同一个上限，闭
// 区间），越界一律 400 且什么都不写。
func TestSessionGrantRenewExpiresBounds(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	st := s.jobs.Meta()
	registerSessionAs(t, s, "tok-alice", "sess-renew-bounds")
	grant := createSessionGrant(t, s, "sess-renew-bounds", authz.PermSessionReplyOthers, 60)

	cases := []struct {
		name       string
		expiresSec any
		want       int
	}{
		{"missing", nil, http.StatusBadRequest},
		{"zero", 0, http.StatusBadRequest},
		{"negative", -60, http.StatusBadRequest},
		{"one second more than the ceiling", authSessionGrantMaxSec + 1, http.StatusBadRequest},
		{"the ceiling itself", authSessionGrantMaxSec, http.StatusOK},
		{"one hour", 3600, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := renewSessionGrant(t, s, grant.ID, tc.expiresSec)
			if resp.StatusCode != tc.want {
				t.Fatalf("renew(%v) = %d, want %d (%s)", tc.expiresSec, resp.StatusCode, tc.want, readBody(t, resp))
			}
			if tc.want != http.StatusOK {
				return
			}
			var renewed authGrantView
			decode(t, resp, &renewed)
			if now := time.Now().Unix(); renewed.ExpiresAt <= now || renewed.ExpiresAt > now+authSessionGrantMaxSec {
				t.Errorf("expires_at = %d, want a moment within the next 24 hours", renewed.ExpiresAt)
			}
		})
	}
	// 无论续期几次，都还是那一条行。
	if rows := grantRowsOf(t, st, "session:sess-renew-bounds"); len(rows) != 1 {
		t.Errorf("the session has %d grant rows, want the one that was renewed", len(rows))
	}
}

// TestSessionGrantRenewPermissionMatrix: 续期路由仍然只有根管理员 —— 声明是 Require(auth.manage)，
// 没有该权限的 user 403（且写明缺哪个权限），job 凭据与 worker 令牌一律 403。
func TestSessionGrantRenewPermissionMatrix(t *testing.T) {
	t.Parallel()
	const key = "POST /v1/auth/grants/{id}/renew"
	decl, declared := routeDecls[key]
	if !declared || decl.user.kind != userRequire || !slices.Contains(decl.user.perms, authz.PermAuthManage) {
		t.Errorf("%s must be declared Require(%s), got %+v (declared=%v)", key, authz.PermAuthManage, decl, declared)
	}

	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice"},
		},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
	})
	jobToken := seedJobToken(t, s, "job-renew", jobstore.JobCredentialMember, "")
	registerSessionAs(t, s, "tok-alice", "sess-renew-perm")
	grant := createSessionGrant(t, s, "sess-renew-perm", authz.PermSessionReplyOthers, 3600)

	for _, tc := range []struct{ name, token string }{
		{"a user without auth.manage", "tok-alice"},
		{"a job credential", jobToken},
		{"a worker token", "tok-w1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/auth/grants/"+grant.ID+"/renew", tc.token,
				map[string]any{"expires_sec": 3600})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("renew as %s = %d, want 403 (%s)", tc.name, resp.StatusCode, readBody(t, resp))
			}
			if tc.token != "tok-alice" {
				return
			}
			body := readBody(t, resp)
			if !strings.Contains(body, authz.PermAuthManage) || !strings.Contains(body, "gofer auth whoami") {
				t.Errorf("the 403 does not name %s and `gofer auth whoami`: %s", authz.PermAuthManage, body)
			}
		})
	}

	// 三次被拒的请求都什么都没写。
	row, ok, err := s.jobs.Meta().GetAuthGrant(grant.ID)
	if err != nil || !ok {
		t.Fatalf("read the grant after the refusals: ok=%v err=%v", ok, err)
	}
	if row.ExpiresAt != grant.ExpiresAt {
		t.Errorf("a refused renewal moved expires_at from %d to %d", grant.ExpiresAt, row.ExpiresAt)
	}
}
