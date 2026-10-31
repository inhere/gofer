package httpapi

import (
	"net/http"
	"testing"
)

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
