package httpapi

// 用户管理接口（配置入库 P1，计划 T6）：
//
//   - the routes are root-only, and the caller-kind half refuses a job credential on the GET and
//     the audit route too (SEC-01 lets a job read every GET route);
//   - a caller added through the API is usable on the NEXT request — no reload, no restart, and
//     the configuration file is not touched;
//   - disabling it (through the API or through Core.UpdateCallers) is a 401 on the next request:
//     the published snapshot is rebuilt from the caller rows, not from a file difference (the
//     Disabled / TokenHash fields are `yaml:"-"`, so nothing about them can show up in a diff);
//   - the token appears in the creation / rotation response and NOWHERE else (SR1301);
//   - the write rules hold: a ceiling is required or explicitly waived, a file caller cannot be
//     rotated / disabled / enabled, an id is never reused, and a caller a file entry took over
//     may only be enabled after a rotation.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/jobstore"
)

// callerAdminServer builds a Server over a REAL Core (the wiring serve uses): the caller routes
// write auth_callers rows through Core.UpdateCallers, and the server's auth snapshot follows the
// Core's reload hook exactly as it does in serve. The fixture file declares one root
// administrator and one plain caller.
func callerAdminServer(t *testing.T) (*Server, *core.Core, string) {
	t.Helper()
	root := filepath.ToSlash(t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	text := fmt.Sprintf(`server:
  callers:
    - id: root
      token: tok-root
      can_admin: true
    - id: alice
      token: tok-alice

storage:
  root: %s
  db_path: %s/gofer.db

projects:
  self:
    host_path: %s
    allowed_agents: [exec]
    allowed_runners: [local]
    allow_exec: true
`, root, root, root)
	if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	cr, err := core.Build(cfg, core.WithConfigPath(cfgPath), core.WithAgentDetector(agent.NoopDetector{}))
	if err != nil {
		t.Fatalf("build core: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })
	drainOnCleanup(t, cr.Jobs)
	srv := New(&cfg.Server, "", false, cr.Jobs, cr.Workflow(), cr.Projects, cr.Agents, nil, cfg.Runners, nil, nil)
	srv.SetConfigWriter(cr)
	srv.SetCallers(cr)
	cr.SetReloadHook(func(next *config.Config) {
		srv.SetServerConfig(&next.Server)
		srv.SetRunners(next.Runners)
	})
	return srv, cr, cfgPath
}

// addCaller posts one caller and returns the decoded change plus the status.
func addCaller(t *testing.T, s *Server, body any) (config.CallerChange, int) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/callers", "tok-root", body)
	var out config.CallerChange
	if resp.StatusCode == http.StatusCreated {
		decode(t, resp, &out)
	} else {
		_ = resp.Body.Close()
	}
	return out, resp.StatusCode
}

// ceilingCase is the body helper of the one-ceiling rule.
func ceilingCase(projects ...string) *config.CallerCeiling {
	return &config.CallerCeiling{Projects: projects}
}

// TestCallerAdminLifecycleIsLiveWithoutReload is the acceptance case of the plan's T6: a caller
// added through the API authenticates on the next request, its token can be rotated and the
// rotated one wins at once, disabling it is a 401 immediately, enabling it works again — all
// without touching the configuration file or restarting anything.
func TestCallerAdminLifecycleIsLiveWithoutReload(t *testing.T) {
	s, cr, cfgPath := callerAdminServer(t)
	before, err := os.ReadFile(cfgPath)
	assert.NoErr(t, err)

	created, status := addCaller(t, s, map[string]any{
		"id": "ci", "name": "CI runner", "ceiling": map[string]any{"projects": []string{"self"}},
	})
	assert.Eq(t, http.StatusCreated, status, "a caller with a ceiling is created")
	assert.True(t, strings.HasPrefix(created.Token, "gct_"), "the token carries its prefix: %q", created.Token)
	assert.Eq(t, config.CallerSourceDB, created.Caller.Source)
	assert.Eq(t, config.CallerStateActive, created.Caller.State)

	// Usable at once: no reload, no restart.
	resp := do(t, s, http.MethodGet, "/v1/projects", created.Token, nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode, "a new caller must be usable on the next request")
	_ = resp.Body.Close()

	// The token is nowhere but in that answer.
	assert.NoErr(t, auditHoldsNoToken(t, cr, created.Token))

	// Rotation: the new token wins, the old one is a 401.
	rot := do(t, s, http.MethodPost, "/v1/callers/ci/rotate", "tok-root", nil)
	assert.Eq(t, http.StatusOK, rot.StatusCode)
	var rotated config.CallerChange
	decode(t, rot, &rotated)
	assert.True(t, rotated.Token != "" && rotated.Token != created.Token, "a rotation mints a new token")
	resp = do(t, s, http.MethodGet, "/v1/projects", rotated.Token, nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode, "the rotated token authenticates")
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/projects", created.Token, nil)
	assert.Eq(t, http.StatusUnauthorized, resp.StatusCode, "the previous token is dead")
	_ = resp.Body.Close()

	// Disable: 401 immediately, and the list says why.
	resp = do(t, s, http.MethodPost, "/v1/callers/ci/disable", "tok-root", nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/projects", rotated.Token, nil)
	assert.Eq(t, http.StatusUnauthorized, resp.StatusCode, "a disabled caller must not authenticate")
	_ = resp.Body.Close()
	var listed callersView
	decode(t, do(t, s, http.MethodGet, "/v1/callers", "tok-root", nil), &listed)
	state := ""
	for _, v := range listed.Callers {
		if v.ID == "ci" {
			state = v.State
			assert.Eq(t, config.CallerSourceDB, v.Source)
			assert.True(t, v.TokenSet, "a stored caller holds a credential")
			assert.True(t, strings.HasPrefix(v.TokenHint, "gct_"), "the hint is shown, the token is not: %q", v.TokenHint)
		}
	}
	assert.Eq(t, config.CallerStateDisabled, state)

	// Enable: the same token works again (disabling is not revoking).
	resp = do(t, s, http.MethodPost, "/v1/callers/ci/enable", "tok-root", nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/projects", rotated.Token, nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode, "re-enabling restores the same credential")
	_ = resp.Body.Close()

	// The configuration file never changed: this API writes the database only.
	after, err := os.ReadFile(cfgPath)
	assert.NoErr(t, err)
	assert.Eq(t, string(before), string(after), "the caller API must not rewrite the configuration file")

	// Every write left an audit line whose values are masked.
	var audit callerAuditView
	decode(t, do(t, s, http.MethodGet, "/v1/callers/audit?limit=50", "tok-root", nil), &audit)
	assert.True(t, audit.Count >= 4, "add + rotate + disable + enable are audited: %+v", audit.Entries)
	for _, e := range audit.Entries {
		assert.False(t, strings.Contains(e.BeforeMasked+e.AfterMasked, "gct_"+rotated.Token[4:]),
			"an audit line must never carry the token")
	}
}

// TestCallerAdminDisableThroughCoreIsImmediate pins the review's requirement: disabling a caller
// through Core.UpdateCallers (not the API) is a 401 on the very next request. `Disabled` and
// `TokenHash` are `yaml:"-"`, so nothing in the stored configuration differs — the snapshot must
// be rebuilt from the caller ROWS, and an implementation that only reacts to a config diff would
// leave the token working.
func TestCallerAdminDisableThroughCoreIsImmediate(t *testing.T) {
	s, cr, _ := callerAdminServer(t)
	created, status := addCaller(t, s, map[string]any{"id": "ci", "no_ceiling": true})
	assert.Eq(t, http.StatusCreated, status)
	resp := do(t, s, http.MethodGet, "/v1/projects", created.Token, nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	_, err := cr.UpdateCallers(func(_ *config.Config, tx jobstore.CallerTx) error {
		row, ok, err := tx.GetCaller("ci")
		if err != nil || !ok {
			return fmt.Errorf("read the row: %v (found=%t)", err, ok)
		}
		row.DisabledAt = tx.Now()
		return tx.UpdateCallerTx(row)
	})
	assert.NoErr(t, err)

	resp = do(t, s, http.MethodGet, "/v1/projects", created.Token, nil)
	assert.Eq(t, http.StatusUnauthorized, resp.StatusCode,
		"a caller disabled through Core.UpdateCallers must not authenticate on the next request")
	_ = resp.Body.Close()
}

// TestCallerAdminOnlyRootMayManage: the route gate requires auth.manage (the root's own
// permission) and every handler repeats the caller-kind half, so a plain caller, a worker token
// and a job credential all get a 403 — on the reads as well.
func TestCallerAdminOnlyRootMayManage(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice"},
		},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
	})
	s.SetCallers(newMemCallers())
	jobToken := seedJobToken(t, s, "job-callers", jobstore.JobCredentialMember, "")

	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/callers", nil},
		{http.MethodGet, "/v1/callers/audit", nil},
		{http.MethodPost, "/v1/callers", map[string]any{"id": "ci", "no_ceiling": true}},
		{http.MethodPost, "/v1/callers/ci/rotate", nil},
		{http.MethodPost, "/v1/callers/ci/disable", nil},
		{http.MethodPost, "/v1/callers/ci/enable", nil},
		{http.MethodPut, "/v1/callers/ci/ceiling", map[string]any{"no_ceiling": true}},
		{http.MethodPut, "/v1/callers/ci/limits", map[string]any{"rate_limit": 1}},
	}
	for _, tc := range routes {
		if resp := do(t, s, tc.method, tc.path, "tok-root", tc.body); resp.StatusCode == http.StatusForbidden {
			t.Errorf("%s %s as the root = 403, want past the gate (%s)", tc.method, tc.path, readBody(t, resp))
		}
		for _, tok := range []string{"tok-alice", "tok-w1", jobToken} {
			resp := do(t, s, tc.method, tc.path, tok, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s as %s = %d, want 403", tc.method, tc.path, tok, resp.StatusCode)
			}
			_ = resp.Body.Close()
		}
	}
}

// TestCallerAdminWriteRules is the refusal table: the two forms of the ceiling, the ids that may
// not be reused, and the file caller this API may not rotate or switch.
func TestCallerAdminWriteRules(t *testing.T) {
	s, cr, _ := callerAdminServer(t)
	// A worker binding and a stored caller the configuration file takes over.
	_ = cr // the store is reached through the routes below
	cases := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{"no ceiling and no no_ceiling", http.MethodPost, "/v1/callers",
			map[string]any{"id": "noc"}, http.StatusBadRequest},
		{"empty ceiling", http.MethodPost, "/v1/callers",
			map[string]any{"id": "empty", "ceiling": map[string]any{}}, http.StatusBadRequest},
		{"ceiling and no_ceiling", http.MethodPost, "/v1/callers",
			map[string]any{"id": "both", "ceiling": map[string]any{"projects": []string{"self"}}, "no_ceiling": true},
			http.StatusBadRequest},
		{"bad glob", http.MethodPost, "/v1/callers",
			map[string]any{"id": "glob", "ceiling": map[string]any{"projects": []string{"["}}}, http.StatusBadRequest},
		{"reserved id", http.MethodPost, "/v1/callers",
			map[string]any{"id": "default", "no_ceiling": true}, http.StatusBadRequest},
		{"file caller id", http.MethodPost, "/v1/callers",
			map[string]any{"id": "alice", "no_ceiling": true}, http.StatusConflict},
		{"unknown caller", http.MethodPost, "/v1/callers/nobody/rotate", nil, http.StatusNotFound},
		{"rotate a file caller", http.MethodPost, "/v1/callers/alice/rotate", nil, http.StatusConflict},
		{"disable a file caller", http.MethodPost, "/v1/callers/alice/disable", nil, http.StatusConflict},
		{"enable a file caller", http.MethodPost, "/v1/callers/alice/enable", nil, http.StatusConflict},
		{"empty ceiling on set", http.MethodPut, "/v1/callers/alice/ceiling",
			map[string]any{"ceiling": map[string]any{}}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		resp := do(t, s, tc.method, tc.path, "tok-root", tc.body)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: %s %s = %d, want %d (%s)", tc.name, tc.method, tc.path, resp.StatusCode, tc.want, readBody(t, resp))
			continue
		}
		_ = resp.Body.Close()
	}
}

// TestCallerAdminFileCallerCeilingWritesTheFile: a file caller's ceiling is the one thing this
// API may change on it, and it goes through the ordinary config write transaction (D12) — the
// file really changes, and the audit line names the caller.
func TestCallerAdminFileCallerCeilingWritesTheFile(t *testing.T) {
	s, cr, cfgPath := callerAdminServer(t)
	before, err := os.ReadFile(cfgPath)
	assert.NoErr(t, err)

	resp := do(t, s, http.MethodPut, "/v1/callers/alice/ceiling", "tok-root",
		map[string]any{"ceiling": map[string]any{"projects": []string{"self"}}})
	assert.Eq(t, http.StatusOK, resp.StatusCode, readBody(t, resp))
	after, err := os.ReadFile(cfgPath)
	assert.NoErr(t, err)
	assert.True(t, string(before) != string(after), "a file caller's ceiling must be written to the file")
	assert.True(t, strings.Contains(string(after), "ceiling"), "the file now carries the ceiling")
	entries, err := cr.Store.ListConfigAudit("server.callers.alice", 0)
	assert.NoErr(t, err)
	assert.Len(t, entries, 1, "the file write is audited")
	assert.True(t, strings.Contains(entries[0].AfterMasked, "self"), "the audit names the new ceiling")
}

// TestCallerAdminEnableAfterTakeoverNeedsRotation (D10) is the API's walk of the rule, in the
// order an operator meets it:
//
//	file declares the id → reload takes the row over
//	enable (file entry still there) → 409, the id is TAKEN (the other rule)
//	file entry removed → reload; enable → 409 naming the ROTATION (the D10 rule)
//	rotate, enable → the new token works at once, the pre-takeover token stays 401
func TestCallerAdminEnableAfterTakeoverNeedsRotation(t *testing.T) {
	s, cr, cfgPath := callerAdminServer(t)
	created, status := addCaller(t, s, map[string]any{
		"id": "taken", "ceiling": map[string]any{"projects": []string{"self"}},
	})
	assert.Eq(t, http.StatusCreated, status)

	// The operator declares the same id in the file: the next RELOAD merges the file and takes
	// the row over (the merge is the reload's job, not the write transaction's).
	original, err := os.ReadFile(cfgPath)
	assert.NoErr(t, err)
	declared := strings.Replace(string(original), "    - id: alice\n      token: tok-alice\n",
		"    - id: alice\n      token: tok-alice\n    - id: taken\n      token: file-token\n", 1)
	assert.True(t, declared != string(original), "the fixture gained the same-id entry")
	assert.NoErr(t, os.WriteFile(cfgPath, []byte(declared), 0o600))
	_, err = cr.ReloadConfigReport()
	assert.NoErr(t, err)
	row, ok, err := cr.Store.GetStoredCaller("taken")
	assert.NoErr(t, err)
	assert.True(t, ok && row.SupersededAt != 0, "the file entry takes the stored row over")

	// While the file still declares the id, the 409 is about the id being taken (a row enabled
	// here would be hidden again by the next merge).
	resp := do(t, s, http.MethodPost, "/v1/callers/taken/enable", "tok-root", nil)
	assert.Eq(t, http.StatusConflict, resp.StatusCode)
	assert.True(t, strings.Contains(readBody(t, resp), "declared in the configuration file"),
		"the 409 while the file entry exists is the id-taken refusal")

	// The file entry is removed; the row is STILL taken over, so enable must now be refused by
	// the rotation rule itself — and the pre-takeover token must stay dead.
	assert.NoErr(t, os.WriteFile(cfgPath, original, 0o600))
	_, err = cr.ReloadConfigReport()
	assert.NoErr(t, err)
	resp = do(t, s, http.MethodPost, "/v1/callers/taken/enable", "tok-root", nil)
	assert.Eq(t, http.StatusConflict, resp.StatusCode,
		"without a rotation the taken-over caller must not be enabled")
	assert.True(t, strings.Contains(readBody(t, resp), "rotate"),
		"the 409 after the file entry is gone must name the rotation")
	resp = do(t, s, http.MethodGet, "/v1/projects", created.Token, nil)
	assert.Eq(t, http.StatusUnauthorized, resp.StatusCode,
		"the pre-takeover token must not work while the row is taken over")
	_ = resp.Body.Close()

	// Rotate + enable: the new token works at once, the pre-takeover one stays dead.
	rot := do(t, s, http.MethodPost, "/v1/callers/taken/rotate", "tok-root", nil)
	assert.Eq(t, http.StatusOK, rot.StatusCode)
	var rotated config.CallerChange
	decode(t, rot, &rotated)
	resp = do(t, s, http.MethodPost, "/v1/callers/taken/enable", "tok-root", nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode, readBody(t, resp))
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/projects", rotated.Token, nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode, "the rotated token works after enabling")
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/projects", created.Token, nil)
	assert.Eq(t, http.StatusUnauthorized, resp.StatusCode, "the pre-takeover token must never come back")
	_ = resp.Body.Close()
}

// TestCallerAdminWorkerIDRemovalUnblocksTheCaller: a stored caller whose id a worker binding uses
// is hidden and blocked, not deleted. Removing the worker through the API, then rotating and
// enabling, must make the caller usable at once — the caller write re-merges from the rows, so no
// reload is needed.
func TestCallerAdminWorkerIDRemovalUnblocksTheCaller(t *testing.T) {
	s, cr, cfgPath := callerAdminServer(t)
	created, status := addCaller(t, s, map[string]any{
		"id": "w-ci", "ceiling": map[string]any{"projects": []string{"self"}},
	})
	assert.Eq(t, http.StatusCreated, status)

	// A worker binding with the same id appears in the file (the CLI / a hand edit); the reload
	// merges it and hides the row.
	original, err := os.ReadFile(cfgPath)
	assert.NoErr(t, err)
	declared := strings.Replace(string(original), "\nstorage:",
		"\n  workers:\n    w-ci:\n      token: worker-token\n\nstorage:", 1)
	assert.True(t, declared != string(original), "the fixture gained a worker binding")
	assert.NoErr(t, os.WriteFile(cfgPath, []byte(declared), 0o600))
	_, err = cr.ReloadConfigReport()
	assert.NoErr(t, err)
	row, ok, err := cr.Store.GetStoredCaller("w-ci")
	assert.NoErr(t, err)
	assert.True(t, ok && row.SupersededAt != 0, "a worker's id hides the stored caller")
	assert.True(t, cr.Config().OwnerBlocked("w-ci"), "the worker collision blocks the id as a job owner")
	resp := do(t, s, http.MethodGet, "/v1/projects", created.Token, nil)
	assert.Eq(t, http.StatusUnauthorized, resp.StatusCode, "a hidden caller authenticates nothing")
	_ = resp.Body.Close()

	// The worker is removed through the API (a config write through Core.Update).
	resp = do(t, s, http.MethodDelete, "/v1/workers/w-ci", "tok-root", nil)
	assert.Eq(t, http.StatusNoContent, resp.StatusCode, readBody(t, resp))
	resp = do(t, s, http.MethodPost, "/v1/callers/w-ci/rotate", "tok-root", nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode)
	var rotated config.CallerChange
	decode(t, resp, &rotated)
	resp = do(t, s, http.MethodPost, "/v1/callers/w-ci/enable", "tok-root", nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode, readBody(t, resp))
	_ = resp.Body.Close()

	// Immediately usable: the enable re-merged the row into the published snapshot.
	resp = do(t, s, http.MethodGet, "/v1/projects", rotated.Token, nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode,
		"after the worker is gone, rotate + enable must make the caller usable without a reload")
	_ = resp.Body.Close()
}

// TestCallerAdminConcurrentAddSameID: two adds of one id race; exactly one wins (the caller
// transaction serialises them, and the id is never reused).
func TestCallerAdminConcurrentAddSameID(t *testing.T) {
	s, _, _ := callerAdminServer(t)
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = addCallerStatus(t, s, map[string]any{
				"id": "racer", "ceiling": map[string]any{"projects": []string{"self"}},
			})
		}(i)
	}
	wg.Wait()
	created, conflict := 0, 0
	for _, st := range statuses {
		switch st {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		}
	}
	assert.Eq(t, 1, created, "exactly one add of the same id succeeds: %v", statuses)
	assert.Eq(t, 1, conflict, "the other one is refused: %v", statuses)
}

// addCallerStatus posts one caller from a goroutine and answers the status only.
func addCallerStatus(t *testing.T, s *Server, body any) int {
	resp := do(t, s, http.MethodPost, "/v1/callers", "tok-root", body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// auditHoldsNoToken scans everything a caller write stores — the row and every config_audit
// line — for the token: it must be nowhere but in the response (SR1301).
func auditHoldsNoToken(t *testing.T, cr *core.Core, token string) error {
	t.Helper()
	rows, err := cr.Store.ListStoredCallers()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if strings.Contains(row.TokenHash+row.TokenHint+row.LimitsJSON+row.CeilingYAML, token) {
			return fmt.Errorf("the token reached the auth_callers row of %s", row.ID)
		}
	}
	entries, err := cr.Store.ListConfigAudit("", 0)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.Contains(e.BeforeMasked+e.AfterMasked, token) {
			return fmt.Errorf("the token reached the config_audit line %s (%d)", e.Path, e.ID)
		}
	}
	return nil
}

// memCallers is a stand-in CallerAdmin for the permission matrix: those cases must be refused
// BEFORE the seam is called, so what it answers does not matter (it still has to record whether
// it was reached, which the matrix asserts indirectly: the root gets a non-403).
type memCallers struct{}

func newMemCallers() CallerAdmin { return memCallers{} }

func (memCallers) AddCaller(config.CallerSpec, string) (config.CallerChange, error) {
	return config.CallerChange{Token: "gct_test", Caller: config.CallerView{ID: "ci"}}, nil
}
func (memCallers) RotateCaller(id, _ string) (config.CallerChange, error) {
	return config.CallerChange{Caller: config.CallerView{ID: id}}, nil
}
func (memCallers) DisableCaller(id, _ string) (config.CallerChange, error) {
	return config.CallerChange{Caller: config.CallerView{ID: id}}, nil
}
func (memCallers) EnableCaller(id, _ string) (config.CallerChange, error) {
	return config.CallerChange{Caller: config.CallerView{ID: id}}, nil
}
func (memCallers) SetCallerCeiling(id string, _ *config.CallerCeiling, _ bool, _ string) (config.CallerChange, error) {
	return config.CallerChange{Caller: config.CallerView{ID: id}}, nil
}
func (memCallers) SetCallerLimits(id string, _ config.CallerLimits, _ string) (config.CallerChange, error) {
	return config.CallerChange{Caller: config.CallerView{ID: id}}, nil
}
func (memCallers) ListCallers() ([]config.CallerView, error) { return nil, nil }
func (memCallers) ListCallerAudit(int) ([]jobstore.ConfigAuditEntry, error) {
	return nil, nil
}
