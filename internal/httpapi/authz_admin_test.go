package httpapi

// 配置领域的旁路用例与路由门对照（设计
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 的目录行、§3 的
// 「admin 组」，计划 T1.5h）。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/workerupgrade"
)

// adminCaseWorkerID is the worker the reverse-case harness configures, so POST
// /v1/workers/{id}/upgrade walks past its target lookup and reaches the permission its body
// decides. Every other worker id is unknown to that deployment, which is where the harness's
// one-click update stops.
const adminCaseWorkerID = "w-case"

// setAdminDomainSeams wires the seams the two InHandler routes of plan T1.5h read before they
// reach their permission checks: the worker upgrade staging area and hub (POST
// /v1/workers/{id}/upgrade looks the worker up and needs a staging area first) and the release
// checker plus the apply seams of the one-click update (POST /v1/update/apply reads a checked-in
// release first). The harness server of TestInHandlerReverseCases mounts none of them, so its
// T1.5h cases call this before the requests run. With them each case walks to the second
// permission it declares and stops at the resource it never matched — a worker that is offline,
// an unknown worker id — so neither an upgrade nor an update is ever started.
func setAdminDomainSeams(t *testing.T, s *Server) {
	t.Helper()
	s.SetWorkerUpgrades(workerupgrade.New(t.TempDir()))
	s.SetWorkerUpgrader(&fakeUpgrader{})
	// The unknown worker id the update case names makes the handler refuse the request before
	// it starts anything, so this managed upgrade must never run.
	checker, _ := releaseMirror(t)
	s.SetUpdateChecker(checker)
	s.SetUpdateApply(t.TempDir(), func(context.Context, string) error {
		t.Error("a reverse case must never start a real update")
		return nil
	})
}

// adminGroupConfig is the deployment the domain's cases run against: one plain caller, one root
// administrator (can_admin), the worker the upgrade case targets, and the
// require_admin_capability switch in the position under test.
func adminGroupConfig(requireAdminCapability bool) config.ServerConfig {
	return config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "op", Token: "tok-op"},
			{ID: "root", Token: "tok-root", CanAdmin: true},
		},
		Workers: map[string]config.WorkerAuthConfig{adminCaseWorkerID: {Token: "tok-case-worker"}},
		Governance: config.GovernanceConfig{
			RequireAdminCapability: requireAdminCapability,
		},
	}
}

// TestAdminGroupCellsMatchCallerMayAdmin pins the grid the route gate has to reproduce for the
// design §3「admin 组」— the whole domain's write surface — cell by cell against the
// callerMayAdmin test the handlers carried before the migration:
//
//	require_admin_capability OFF → the `users` subject derives the group, so a plain caller
//	                                passes (callerMayAdmin said yes for every user-class caller);
//	require_admin_capability ON  → only the root administrator holds it, so the same caller is
//	                                refused with the permission named (callerMayAdmin asked
//	                                CallerCanAdmin and said no).
//
// One route per permission stands for its group member; the answer asserted for a caller that
// passes is deliberately only "not a 403" — these endpoints are mounted without their writer,
// library, hub or release checker in this deployment, and their own refusals (503/404/400/409)
// are the pre-migration answers this migration must not change. `worker.upgrade` and
// `worker.upgrade.upload` are the two permissions the domain's InHandler routes decide
// themselves, so their rows also pin that the HANDLER writes the same 403 the gate writes.
func TestAdminGroupCellsMatchCallerMayAdmin(t *testing.T) {
	t.Parallel()
	gateOff := newTestServerCfg(t, adminGroupConfig(false))
	gateOn := newTestServerCfg(t, adminGroupConfig(true))

	cases := []struct {
		name         string
		permission   string
		method, path string
		body         any
	}{
		{"config write", authz.PermConfigWrite, http.MethodPut, "/v1/config/agents/x", map[string]any{"command": "x"}},
		{"project write", authz.PermProjectWrite, http.MethodPost, "/v1/projects", map[string]any{"key": "p"}},
		{"rule write", authz.PermRulesWrite, http.MethodPut, "/v1/rules/dev", map[string]any{"content": "---\ndescription: d\n---\n\nbody"}},
		{"skill write", authz.PermSkillsWrite, http.MethodPost, "/v1/skills/import", map[string]any{"source": "git+https://example.invalid/x.git"}},
		{"worker manage", authz.PermWorkerManage, http.MethodPost, "/v1/workers", map[string]any{"worker_id": "w-new"}},
		{"worker upgrade", authz.PermWorkerUpgrade, http.MethodPost, "/v1/workers/" + adminCaseWorkerID + "/upgrade", map[string]any{"force": true}},
		{"worker upgrade upload", authz.PermWorkerUpgradeUpload, http.MethodPut, "/v1/workers/" + adminCaseWorkerID + "/upgrade/file", "binary"},
		{"update apply", authz.PermUpdateApply, http.MethodPost, "/v1/update/apply", map[string]any{}},
		{"update cancel", authz.PermUpdateApply, http.MethodDelete, "/v1/update/apply", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Switch OFF: the plain caller holds the permission the configuration derives.
			assertNotForbidden(t, gateOff, tc.method, tc.path, "tok-op", tc.body, "the plain caller with the switch off")
			assertNotForbidden(t, gateOff, tc.method, tc.path, "tok-root", tc.body, "the root administrator with the switch off")

			// Switch ON: 仅根, so the plain caller is refused and the refusal names the
			// permission (design §3「403 响应写明缺哪个权限」).
			resp := do(t, gateOn, tc.method, tc.path, "tok-op", tc.body)
			answer := readBody(t, resp)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("the plain caller with the switch on = %d, want 403 (body %s)", resp.StatusCode, answer)
			}
			if !strings.Contains(answer, tc.permission) {
				t.Fatalf("the 403 of %s must name %s: %s", tc.path, tc.permission, answer)
			}
			assertNotForbidden(t, gateOn, tc.method, tc.path, "tok-root", tc.body, "the root administrator with the switch on")
		})
	}
}

// assertNotForbidden asserts the request is not refused for a missing permission. Any other
// answer is the endpoint's own (503 without its writer, 400 for an incomplete body, …): this test
// is about WHO may reach the route, and the route's own answers are unchanged by the migration.
func assertNotForbidden(t *testing.T, s *Server, method, path, token string, body any, what string) {
	t.Helper()
	resp := do(t, s, method, path, token, body)
	answer := readBody(t, resp)
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("%s on %s %s was refused: %s", what, method, path, answer)
	}
}

// TestAdminDomainNonUserArmsAnswerAsBeforeTheMigration pins the arm this domain's handlers give a
// caller that is NOT governed by permissions (design §2「只对 user 类调用方计算」): the
// pre-migration refusal, never a catalog lookup. These routes are administrative writes of a
// person, so the caller kind is a person-only allowlist (adminUserOnly, authz_admin_rules.go) —
// the half of callerMayAdmin the route gate cannot carry, because the gate deliberately leaves a
// non-user caller alone. The credentials are refused by their own tables first (a worker token by
// workercredential.go's whitelist, a job credential by SEC-01), which is why what this test pins
// is the ANSWER: 403, exactly as before the migration.
func TestAdminDomainNonUserArmsAnswerAsBeforeTheMigration(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, adminGroupConfig(false))
	jobToken := seedJobToken(t, s, "job-admin-domain", jobstore.JobCredentialMember, "")

	cases := []struct {
		name         string
		method, path string
		body         any
	}{
		{"config write", http.MethodPut, "/v1/config/agents/x", map[string]any{"command": "x"}},
		{"project write", http.MethodPost, "/v1/projects", map[string]any{"key": "p"}},
		{"rule write", http.MethodPut, "/v1/rules/dev", map[string]any{"content": "---\ndescription: d\n---\n\nbody"}},
		{"skill write", http.MethodPost, "/v1/skills/import", map[string]any{"source": "git+https://example.invalid/x.git"}},
		{"worker manage", http.MethodPost, "/v1/workers", map[string]any{"worker_id": "w-new"}},
		{"worker reload", http.MethodPost, "/v1/workers/" + adminCaseWorkerID + "/reload", map[string]any{}},
		{"worker upgrade", http.MethodPost, "/v1/workers/" + adminCaseWorkerID + "/upgrade", map[string]any{"source": "staged"}},
		{"worker upgrade upload", http.MethodPut, "/v1/workers/" + adminCaseWorkerID + "/upgrade/file", "binary"},
		{"update check", http.MethodPost, "/v1/update/check", nil},
		{"update apply", http.MethodPost, "/v1/update/apply", map[string]any{}},
		{"update cancel", http.MethodDelete, "/v1/update/apply", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, token := range []string{"tok-case-worker", jobToken} {
				resp := do(t, s, tc.method, tc.path, token, tc.body)
				answer := readBody(t, resp)
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("%s as a non-user credential = %d, want 403 (body %s)",
						tc.path, resp.StatusCode, answer)
				}
			}
		})
	}
}
