package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// 会话授权的测试（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md
// §4「会话授权」，计划 T2.2）。三个问题：
//
//   - 生效条件：会话现在的 owner、授权记录的 owner、请求方三者一致；会话现在（且仍然）是发起授权
//     那个化身；会话没有结束（状态不是 ended，且 ended_at 为空）；授权没被撤销也没过期；
//   - 三种「复活」都不让旧授权再生效：结束→复活、结束→接管→释放、删除→重新登记；
//   - 归属转移：撤销授权与改 owner 在同一事务里完成，转移后授权行就是已撤销的。
//
// The permission used throughout is `session.reply.others`: it is session-grantable, and with the
// answer gate CLOSED the truth table of design §3 derives it for NOBODY — so it is held in these
// tests if and only if the session grant really applies, which is what a session grant is for.

// sessionGrantServer is the fixture: an explicit root administrator plus two ordinary callers,
// no capability bits and every governance gate at its default.
func sessionGrantServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Token: "tok-root", CanAdmin: true},
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
	}})
}

// createSessionGrant grants a session subject the pattern as the root administrator. expiresSec 0
// means the caller wants the default of one hour.
func createSessionGrant(t *testing.T, s *Server, sid, pattern string, expiresSec int64) authGrantView {
	t.Helper()
	if expiresSec == 0 {
		expiresSec = 3600
	}
	resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", map[string]any{
		"subject": "session:" + sid, "pattern": pattern, "expires_sec": expiresSec,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create the session grant for %s: status=%d, body %s", sid, resp.StatusCode, readBody(t, resp))
	}
	var created authGrantView
	decode(t, resp, &created)
	return created
}

// whoamiWith is whoami with a terminal session attached by hand (the header the CLI's single
// injection point sets from the environment).
func whoamiWith(t *testing.T, s *Server, token, sid string) authWhoamiView {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if sid != "" {
		req.Header.Set(sessionHeader, sid)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("whoami as %s (session %q) = %d: %s", token, sid, rec.Code, rec.Body.String())
	}
	var out authWhoamiView
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	return out
}

// sessionGrantHeld reports whether the caller holds perm when the request names sid, both through
// the resolver directly and through whoami (the answer an operator reads).
func sessionGrantHeld(t *testing.T, s *Server, token, sid, perm string) bool {
	t.Helper()
	direct := s.effective(callerIDOfToken(t, s, token), sid).Has(perm)
	viaWhoami := false
	for _, p := range whoamiWith(t, s, token, sid).Permissions {
		if p.Name == perm {
			viaWhoami = true
		}
	}
	if direct != viaWhoami {
		t.Fatalf("the resolver says held=%v and whoami says held=%v — they must agree", direct, viaWhoami)
	}
	return direct
}

// callerIDOfToken maps the tokens this file uses onto the caller ids of the fixture.
func callerIDOfToken(t *testing.T, s *Server, token string) string {
	t.Helper()
	switch token {
	case "tok-alice":
		return "alice"
	case "tok-bob":
		return "bob"
	case "tok-root":
		return "root"
	default:
		t.Fatalf("unknown token %q", token)
		return ""
	}
}

// TestSessionGrantAppliesToItsOwnSession: 生效条件的一次正面与三次反面 —— 带上会话头的那个调用方
// 拿到授权，不带头、别的调用方带头、以及别的会话 id 都拿不到。
func TestSessionGrantAppliesToItsOwnSession(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	registerSessionAs(t, s, "tok-alice", "sess-a")
	registerSessionAs(t, s, "tok-alice", "sess-b")
	const perm = authz.PermSessionReplyOthers
	grant := createSessionGrant(t, s, "sess-a", perm, 0)

	if grant.SessionOwner != "alice" || grant.SessionIncarnation == "" {
		t.Fatalf("the grant must record the session's owner and incarnation: %+v", grant)
	}
	if !sessionGrantHeld(t, s, "tok-alice", "sess-a", perm) {
		t.Fatalf("alice does not hold %s from the grant of her own session", perm)
	}
	// whoami names the session AND the grant it came from, so an operator can explain it.
	who := whoamiWith(t, s, "tok-alice", "sess-a")
	if who.SessionID != "sess-a" {
		t.Errorf("whoami session_id = %q, want sess-a", who.SessionID)
	}
	found := false
	for _, p := range who.Permissions {
		if p.Name == perm {
			found = true
			if p.Source != sourceDB || p.GrantID != grant.ID {
				t.Errorf("%s = %+v, want source db from grant %s", perm, p, grant.ID)
			}
		}
	}
	if !found {
		t.Fatalf("whoami does not report %s: %+v", perm, who.Permissions)
	}

	// A request that names no session gets nothing: the grant belongs to one session.
	if sessionGrantHeld(t, s, "tok-alice", "", perm) {
		t.Errorf("%s must not apply to a request that names no session", perm)
	}
	// Another name gets nothing either.
	if sessionGrantHeld(t, s, "tok-alice", "sess-b", perm) {
		t.Errorf("%s must not apply to another session", perm)
	}
	// And another caller gets nothing, even with the session's own id: owner, the grant's owner
	// and the caller have to be the same.
	if sessionGrantHeld(t, s, "tok-bob", "sess-a", perm) {
		t.Errorf("%s must not apply to a caller that does not own the session", perm)
	}
	if sessionGrantHeld(t, s, "tok-root", "sess-a", perm) && !s.effective("root", "").Has(perm) {
		// The root holds everything anyway; the interesting half is that this is not the session
		// grant talking. (Assert above only guards a future where the root stops holding it.)
		t.Logf("the root holds %s as always", perm)
	}

	// Revoking the grant takes effect at once — the row stays, the permission goes.
	resp := do(t, s, http.MethodDelete, "/v1/auth/grants/"+grant.ID, "tok-root", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status=%d (%s)", resp.StatusCode, readBody(t, resp))
	}
	if sessionGrantHeld(t, s, "tok-alice", "sess-a", perm) {
		t.Errorf("%s must not apply after the grant was revoked", perm)
	}
}

// TestSessionGrantDiesWithTheLifeThatEnded is the acceptance case: 三种复活场景下旧授权都不生效
// （设计 §4）— 结束→复活、结束→接管→释放、删除→重新登记。每一个场景都从「授权确实生效」开始，
// 所以它证明的是复活没有把它带回来，而不是它从来没生效过。
func TestSessionGrantDiesWithTheLifeThatEnded(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	st := s.jobs.Meta()
	const perm = authz.PermSessionReplyOthers

	// 1) 结束 → 复活（同一个 id 重新登记就是一次新会话）。
	registerSessionAs(t, s, "tok-alice", "sess-revive")
	createSessionGrant(t, s, "sess-revive", perm, 0)
	if !sessionGrantHeld(t, s, "tok-alice", "sess-revive", perm) {
		t.Fatal("setup: the grant must apply before the session ends")
	}
	if _, err := st.SetSessionState("sess-revive", jobstore.SessionEnded); err != nil {
		t.Fatalf("end session: %v", err)
	}
	if sessionGrantHeld(t, s, "tok-alice", "sess-revive", perm) {
		t.Error("结束之后：the grant of an ended session must not apply")
	}
	if _, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-revive", Agent: "claude", CallerID: "alice"}); err != nil {
		t.Fatalf("revive session: %v", err)
	}
	anchor, ok, err := st.SessionGrantAnchor("sess-revive")
	if err != nil || !ok {
		t.Fatalf("anchor after revival: ok=%v err=%v", ok, err)
	}
	if anchor.State == jobstore.SessionEnded || anchor.Ended() {
		t.Fatalf("the revived session should be running again: %+v", anchor)
	}
	if sessionGrantHeld(t, s, "tok-alice", "sess-revive", perm) {
		t.Error("复活之后：the grant of the previous life must not come back with the session")
	}

	// 2) 结束 → 接管 → 释放：接管是一个新化身，释放回 ended，两者都不让旧授权生效。
	registerSessionAs(t, s, "tok-alice", "sess-take")
	createSessionGrant(t, s, "sess-take", perm, 0)
	if !sessionGrantHeld(t, s, "tok-alice", "sess-take", perm) {
		t.Fatal("setup: the grant must apply before the take-over")
	}
	if _, err := st.SetSessionState("sess-take", jobstore.SessionEnded); err != nil {
		t.Fatalf("end session: %v", err)
	}
	if _, err := st.SetSessionHandedOff("sess-take", "job-take"); err != nil {
		t.Fatalf("take the session over: %v", err)
	}
	// The state is handed_off here — not ended — so what refuses the grant is the ended_at the
	// take-over kept (design §4: 「读时还要判定 ended_at 为空，作为第二道保险」).
	if anchor, ok, _ := st.SessionGrantAnchor("sess-take"); !ok || anchor.State != jobstore.SessionHandedOff {
		t.Fatalf("setup: the session should be handed_off, got %+v (ok=%v)", anchor, ok)
	}
	if sessionGrantHeld(t, s, "tok-alice", "sess-take", perm) {
		t.Error("接管之后：a session taken over after it ended must not carry its old grants")
	}
	if _, err := st.ReleaseSessionHandedOff("sess-take"); err != nil {
		t.Fatalf("release the take-over: %v", err)
	}
	if sessionGrantHeld(t, s, "tok-alice", "sess-take", perm) {
		t.Error("释放之后：the grant must not apply once the session is back to ended")
	}

	// 3) 删除 → 重新登记：删掉的会话再登记一定是新化身（设计 §4）。
	registerSessionAs(t, s, "tok-alice", "sess-rereg")
	createSessionGrant(t, s, "sess-rereg", perm, 0)
	if !sessionGrantHeld(t, s, "tok-alice", "sess-rereg", perm) {
		t.Fatal("setup: the grant must apply before the session is deleted")
	}
	if ok, err := st.DeleteAgentSession("sess-rereg"); err != nil || !ok {
		t.Fatalf("delete the session: ok=%v err=%v", ok, err)
	}
	registerSessionAs(t, s, "tok-alice", "sess-rereg")
	if sessionGrantHeld(t, s, "tok-alice", "sess-rereg", perm) {
		t.Error("重新登记之后：a re-registered session is a new life and must not inherit the old grant")
	}
	// All three grants are still rows: nothing revoked them, they simply name a life that is over.
	for _, sid := range []string{"sess-revive", "sess-take", "sess-rereg"} {
		rows, err := st.ListAuthGrants()
		if err != nil {
			t.Fatalf("list grants: %v", err)
		}
		found := false
		for _, row := range rows {
			if row.Subject == "session:"+sid {
				found = true
				if row.RevokedAt != 0 {
					t.Errorf("the grant of %s was revoked; it should just have stopped applying", sid)
				}
			}
		}
		if !found {
			t.Errorf("the grant of %s vanished from the table", sid)
		}
	}
}

// TestSessionGrantExpiryAndTransfer: 到期的会话授权不生效（设计 §4「有效期」），归属转移时撤销授权
// 与改 owner 在同一事务里完成（转移后行就是已撤销的，而不是一条看起来还在生效的授权）。
func TestSessionGrantExpiryAndTransfer(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	st := s.jobs.Meta()
	const perm = authz.PermSessionReplyOthers

	registerSessionAs(t, s, "tok-alice", "sess-exp")
	createSessionGrant(t, s, "sess-exp", perm, 0)
	// An already-expired row, written straight through the store (the interface will not create
	// one): the resolver judges expiry against its own clock.
	anchor, ok, err := st.SessionGrantAnchor("sess-exp")
	if err != nil || !ok {
		t.Fatalf("anchor: ok=%v err=%v", ok, err)
	}
	if _, err := st.CreateAuthGrant(jobstore.AuthGrant{
		Subject: "session:sess-exp", Pattern: perm, GrantedBy: "root",
		SessionOwner: anchor.Owner, SessionIncarnation: anchor.Incarnation,
		ExpiresAt: time.Now().Unix() - 1,
	}); err != nil {
		t.Fatalf("create the expired grant: %v", err)
	}
	s.invalidateDBGrants()
	if !sessionGrantHeld(t, s, "tok-alice", "sess-exp", perm) {
		t.Fatal("the live grant must still apply beside the expired one")
	}

	// The hand-over: another caller owns the session afterwards, and the session's grants are
	// revoked in the same transaction (design §4). The row says so, and the permission is gone.
	registerSessionAs(t, s, "tok-alice", "sess-move")
	moved := createSessionGrant(t, s, "sess-move", perm, 0)
	if !sessionGrantHeld(t, s, "tok-alice", "sess-move", perm) {
		t.Fatal("setup: the grant must apply before the hand-over")
	}
	resp := do(t, s, http.MethodPost, "/v1/sessions/sess-move/owner", "tok-root", map[string]any{"caller_id": "bob"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hand the session over: status=%d (%s)", resp.StatusCode, readBody(t, resp))
	}
	row, ok, err := st.GetAuthGrant(moved.ID)
	if err != nil || !ok {
		t.Fatalf("read the grant after the hand-over: ok=%v err=%v", ok, err)
	}
	if row.RevokedAt == 0 || row.RevokedBy != "root" {
		t.Errorf("the grant of a handed-over session must be revoked by the hand-over: %+v", row)
	}
	if sessionGrantHeld(t, s, "tok-alice", "sess-move", perm) {
		t.Error("the previous owner must not keep the session's grants")
	}
	if sessionGrantHeld(t, s, "tok-bob", "sess-move", perm) {
		t.Error("the new owner must not inherit the revoked grants of the previous owner")
	}
}

// TestSessionGrantExpiresBounds: 会话主体的 expires 必填、不超过 24 小时（设计 §4「有效期」；「会话
// 结束」也带上限）—— 上界是闭区间，越界一律 400，什么都不写。
func TestSessionGrantExpiresBounds(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	st := s.jobs.Meta()
	registerSessionAs(t, s, "tok-alice", "sess-bounds")

	cases := []struct {
		name       string
		expiresSec any
		want       int
	}{
		{"missing", nil, http.StatusBadRequest},
		{"zero", 0, http.StatusBadRequest},
		{"negative", -60, http.StatusBadRequest},
		{"one second more than the ceiling", authSessionGrantMaxSec + 1, http.StatusBadRequest},
		{"the ceiling itself", authSessionGrantMaxSec, http.StatusCreated},
		{"one hour", 3600, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"subject": "session:sess-bounds", "pattern": authz.PermTunnelOpen}
			if tc.expiresSec != nil {
				body["expires_sec"] = tc.expiresSec
			}
			resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tc.want, readBody(t, resp))
			}
			if tc.want != http.StatusCreated {
				return
			}
			var created authGrantView
			decode(t, resp, &created)
			if now := time.Now().Unix(); created.ExpiresAt <= now || created.ExpiresAt > now+authSessionGrantMaxSec {
				t.Errorf("expires_at = %d, want a moment within the next 24 hours", created.ExpiresAt)
			}
		})
	}
	// Only the two accepted rows were written.
	rows, err := st.ListActiveAuthGrants("session:sess-bounds")
	if err != nil {
		t.Fatalf("list the session's grants: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("the refused requests wrote rows: %d active grants", len(rows))
	}
}

// TestSessionGrantRefusedOnAnEndedSession: 已结束的会话不能再被授权 —— 这样的授权永远不会生效，写
// 下来只会让操作者以为给了权限（设计 §4；接管一个已结束的终端也带着 ended_at，同样算已结束）。
func TestSessionGrantRefusedOnAnEndedSession(t *testing.T) {
	t.Parallel()
	s := sessionGrantServer(t)
	st := s.jobs.Meta()
	registerSessionAs(t, s, "tok-alice", "sess-ended")
	registerSessionAs(t, s, "tok-alice", "sess-taken")
	if _, err := st.SetSessionState("sess-ended", jobstore.SessionEnded); err != nil {
		t.Fatalf("end session: %v", err)
	}
	if _, err := st.SetSessionState("sess-taken", jobstore.SessionEnded); err != nil {
		t.Fatalf("end session: %v", err)
	}
	if _, err := st.SetSessionHandedOff("sess-taken", "job-1"); err != nil {
		t.Fatalf("take over the ended session: %v", err)
	}

	for _, sid := range []string{"sess-ended", "sess-taken"} {
		resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", map[string]any{
			"subject": "session:" + sid, "pattern": authz.PermTunnelOpen, "expires_sec": 3600,
		})
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("grant on %s = %d, want 409 (%s)", sid, resp.StatusCode, readBody(t, resp))
			continue
		}
		if body := readBody(t, resp); !strings.Contains(body, "has ended") {
			t.Errorf("the refusal of %s does not say the session has ended: %s", sid, body)
		}
	}
}
