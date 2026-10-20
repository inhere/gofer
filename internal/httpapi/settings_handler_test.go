package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
)

// settingsTestServer is a server with the settings service over its own job store (so the
// audit rows land in the same metadata database).
func settingsTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, testToken, false)
	s.SetSettings(settings.NewService(s.jobs.Meta()))
	return s
}

// TestSettingsRoutesRequireTheService: a server without the settings service answers 503
// instead of panicking, like the other optional services.
func TestSettingsRoutesRequireTheService(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/settings", testToken, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/settings without the service = %d, want 503", resp.StatusCode)
	}
}

// TestSettingsCrud: the listing shows every registered key with its default, a PUT stores
// a validated value, the audit row carries the key and a digest, and a DELETE brings the
// default back.
func TestSettingsCrud(t *testing.T) {
	s := settingsTestServer(t)

	resp := do(t, s, http.MethodGet, "/v1/settings", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d, want 200", resp.StatusCode)
	}
	var list struct {
		Settings []settings.Item `json:"settings"`
	}
	decode(t, resp, &list)
	if len(list.Settings) != len(settings.Definitions()) {
		t.Fatalf("list holds %d keys, the registry has %d", len(list.Settings), len(settings.Definitions()))
	}
	for _, item := range list.Settings {
		if item.Modified {
			t.Errorf("%s is modified on a fresh server", item.Key)
		}
	}

	resp = do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, testToken, map[string]any{"value": 4})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d, want 200", resp.StatusCode)
	}
	var item settings.Item
	decode(t, resp, &item)
	if !item.Modified || item.Value != float64(4) {
		t.Fatalf("put item = %+v, want modified 4", item)
	}

	// The audit trail names the key, the old default and the new value.
	rows, err := s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{Subject: settings.KeyWorkDraftMinPrompts})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if rows[0].Action != "settings.set" || !strings.Contains(rows[0].Reason, "new=4") || !strings.Contains(rows[0].Reason, "old=2") {
		t.Fatalf("audit row = %+v, want action settings.set with old=2 new=4", rows[0])
	}

	// A prompt write keeps only its length and head in the audit trail.
	long := strings.Repeat("a very long prompt line ", 10)
	resp = do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyPromptWorkSummarizerExtra, testToken, map[string]any{"value": long})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put prompt = %d, want 200", resp.StatusCode)
	}
	rows, err = s.jobs.Meta().ListAuthAudit(jobstore.AuthAuditFilter{Subject: settings.KeyPromptWorkSummarizerExtra})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Reason, "len=") || strings.Contains(rows[0].Reason, long) {
		t.Fatalf("prompt audit = %+v, want a length + head digest, not the whole text", rows)
	}

	resp = do(t, s, http.MethodDelete, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d, want 200", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get after delete = %d, want 200", resp.StatusCode)
	}
	decode(t, resp, &item)
	if item.Modified || item.Value != float64(2) {
		t.Fatalf("after delete = %+v, want the default 2, not modified", item)
	}

	// An unknown key is a 404, an invalid value a 400 with the reason.
	if resp := do(t, s, http.MethodGet, "/v1/settings/work.nope", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown key = %d, want 404", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, testToken, map[string]any{"value": 99})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("out-of-range put = %d, want 400", resp.StatusCode)
	}
	// A regex that does not compile is refused with the reason.
	resp = do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkDraftExcludeTitlePatterns, testToken, map[string]any{"value": []string{"("}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("broken regex = %d, want 400", resp.StatusCode)
	}
}

// TestSettingsExportImport: the export carries the modified keys only; an import preview
// reports the differences without writing, and the real import writes the valid keys.
func TestSettingsExportImport(t *testing.T) {
	s := settingsTestServer(t)
	if resp := do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, testToken, map[string]any{"value": 3}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d", resp.StatusCode)
	}

	resp := do(t, s, http.MethodGet, "/v1/settings/export", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export = %d, want 200", resp.StatusCode)
	}
	doc, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if !strings.Contains(string(doc), "work.draft.min_prompts: 3") {
		t.Fatalf("export = %q, want the modified key", doc)
	}

	// Import preview with a raw YAML body: the harness JSON-encodes a body, so send the
	// document as a JSON string is not valid YAML for the filter — use the raw helper.
	preview := doRawYAML(t, s, http.MethodPost, "/v1/settings/import?dry_run=1", testToken, "work.draft.min_prompts: 7\n")
	if preview.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d, want 200", preview.StatusCode)
	}
	var result settings.ImportResult
	decode(t, preview, &result)
	if !result.DryRun || result.Applied || len(result.Changes) != 1 || result.Changes[0].Action != "set" {
		t.Fatalf("preview = %+v, want one set change, dry run", result)
	}
	resp = do(t, s, http.MethodGet, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, testToken, nil)
	var item settings.Item
	decode(t, resp, &item)
	if item.Value != float64(3) {
		t.Fatalf("dry run wrote %v, want the stored 3", item.Value)
	}

	applied := doRawYAML(t, s, http.MethodPost, "/v1/settings/import", testToken,
		"work.draft.min_prompts: 7\nwork.short_session.max_prompts: 500\n")
	if applied.StatusCode != http.StatusOK {
		t.Fatalf("import = %d, want 200", applied.StatusCode)
	}
	decode(t, applied, &result)
	if result.Applied || result.Errors != 1 {
		t.Fatalf("import = %+v, want one refused key", result)
	}
	resp = do(t, s, http.MethodGet, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, testToken, nil)
	decode(t, resp, &item)
	if item.Value != float64(7) {
		t.Fatalf("imported value = %v, want 7", item.Value)
	}
}

// TestSettingsPermissions: reading needs settings.read, writing needs settings.write, and a
// restricted caller with every permission still cannot write a server-global setting.
func TestSettingsPermissions(t *testing.T) {
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "ro", Token: "tok-ro", Ceiling: &config.CallerCeiling{Projects: []string{"p1"}, ReadOnly: boolptr(true)}},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}, func(cfg *config.Config) {
		cfg.Projects["p1"] = config.ProjectConfig{
			HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"},
			AllowedRunners: []string{"local"}, AllowExec: true,
		}
	})
	s.SetSettings(settings.NewService(s.jobs.Meta()))

	// settings.read is seeded to `users`, so a plain caller reads without anyone granting
	// anything; settings.write is not seeded, so writing is refused.
	if resp := do(t, s, http.MethodGet, "/v1/settings", "tok-alice", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("seeded read = %d, want 200 (settings.read is seeded to users)", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, "tok-alice", map[string]any{"value": 4}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("unseeded write = %d, want 403", resp.StatusCode)
	}
	// The root administrator holds every permission.
	if resp := do(t, s, http.MethodGet, "/v1/settings", "tok-root", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("root read = %d, want 200", resp.StatusCode)
	}

	// No grant at all (the seeded row replaced by an empty set): neither reading nor writing.
	s.setDBGrants(grantsFor(nil))
	if resp := do(t, s, http.MethodGet, "/v1/settings", "tok-alice", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("read without settings.read = %d, want 403", resp.StatusCode)
	}

	// settings.read alone: reading yes, writing no.
	s.setDBGrants(grantsFor([]string{authz.PermSettingsRead}))
	if resp := do(t, s, http.MethodGet, "/v1/settings", "tok-alice", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("read with settings.read = %d, want 200", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, "tok-alice", map[string]any{"value": 5}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("write without settings.write = %d, want 403", resp.StatusCode)
	}

	// settings.write too: the write passes.
	s.setDBGrants(grantsFor([]string{authz.PermSettingsRead, authz.PermSettingsWrite}))
	if resp := do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, "tok-alice", map[string]any{"value": 5}); resp.StatusCode != http.StatusOK {
		t.Errorf("write with settings.write = %d, want 200", resp.StatusCode)
	}

	// A restricted (read-only ceiling) caller holding the whole catalog reads but does not
	// write: the route is server-global, so the ceiling gate refuses the write.
	grantAll(s)
	if resp := do(t, s, http.MethodGet, "/v1/settings", "tok-ro", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("restricted read = %d, want 200", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkDraftMinPrompts, "tok-ro", map[string]any{"value": 6}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("restricted write = %d, want 403", resp.StatusCode)
	}
}

// TestSettingsUnrestrictedCallerUnchanged is the regression assertion the task asks for: a
// caller without a ceiling behaves exactly as before — the token holder reads and writes
// with its own permissions, nothing is filtered.
func TestSettingsUnrestrictedCallerUnchanged(t *testing.T) {
	s := settingsTestServer(t)
	resp := do(t, s, http.MethodPut, "/v1/settings/"+settings.KeyWorkShortSessionMaxMinutes, testToken, map[string]any{"value": 15})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unrestricted write = %d, want 200", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/settings/"+settings.KeyWorkShortSessionMaxMinutes, testToken, nil)
	var item settings.Item
	decode(t, resp, &item)
	if item.Value != float64(15) || !item.Modified {
		t.Fatalf("unrestricted read = %+v, want the value it just wrote", item)
	}
}

// doRaw sends a body verbatim (the import route takes a YAML document, not JSON).
func doRawYAML(t *testing.T, s *Server, method, path, token, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}
