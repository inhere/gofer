package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// A config still carrying the retired supervisor / presence sections is saved through
// PUT /v1/config/*: the response carries the ignore warnings, GET /v1/config shows them,
// and the saved file keeps both sections.
func TestPutConfigCarriesRetiredSectionWarnings(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	yamlText += "\nsupervisor:\n  enabled: true\n  leader:\n    enabled: true\n    agent: mytool\n\npresence:\n  ttl_sec: 30\n"
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, nil)

	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"max_job_timeout_sec": 1800,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /v1/config/server status=%d, want 200", resp.StatusCode)
	}
	var out configWriteResp
	decode(t, resp, &out)
	joined := strings.Join(out.Warnings, "\n")
	if len(out.Warnings) != 2 || !strings.Contains(joined, "top-level `leader:`") || !strings.Contains(joined, "presence") {
		t.Fatalf("PUT warnings = %q", out.Warnings)
	}

	saved := string(readFile(t, cfgPath))
	for _, want := range []string{"supervisor:\n  enabled: true", "presence:\n  ttl_sec: 30"} {
		if !strings.Contains(saved, want) {
			t.Fatalf("saved config lost %q:\n%s", want, saved)
		}
	}

	if v := getConfigView(t, s, adminToken); len(v.Warnings) != 2 {
		t.Fatalf("GET /v1/config warnings = %q", v.Warnings)
	}
}

// A clean config has no warnings key at all.
func TestPutConfigNoWarningsWhenClean(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, _ := newConfigWriteTestServer(t, yamlText, nil)
	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"max_job_timeout_sec": 1800,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out configWriteResp
	decode(t, resp, &out)
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings = %q", out.Warnings)
	}
}
