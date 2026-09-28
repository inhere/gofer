package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func responseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(b)
}

// TUN-05 fixed acceptance tests. They intentionally exercise the HTTP contract so
// the first commit is a real red test against the pre-feature server.
func TestServerHostedForwardStartStop(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))
	put := do(t, s, http.MethodPut, "/v1/tunnels/presets/demo", testToken, map[string]any{
		"worker": "w-local", "specs": []string{"0:127.0.0.1:1"}, "autostart": false,
	})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("seed preset status=%d, want 200", put.StatusCode)
	}
	_ = put.Body.Close()

	start := do(t, s, http.MethodPost, "/v1/tunnels/hosted/demo", testToken, nil)
	if start.StatusCode != http.StatusOK {
		t.Fatalf("hosted start status=%d, want 200; body=%s", start.StatusCode, responseBody(t, start))
	}
	stop := do(t, s, http.MethodDelete, "/v1/tunnels/hosted/demo", testToken, nil)
	if stop.StatusCode != http.StatusOK {
		t.Fatalf("hosted stop status=%d, want 200; body=%s", stop.StatusCode, responseBody(t, stop))
	}
}

func TestForwardAutostart(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))
	resp := do(t, s, http.MethodPut, "/v1/tunnels/presets/auto", testToken, map[string]any{
		"worker": "w-local", "specs": []string{"0:127.0.0.1:1"}, "autostart": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("autostart preset status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Preset struct {
			Autostart bool `json:"autostart"`
		} `json:"preset"`
	}
	decode(t, resp, &body)
	if !body.Preset.Autostart {
		t.Fatalf("autostart=false, want true")
	}
}

func TestUnpushedLocalPresetsListed(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))
	resp := do(t, s, http.MethodGet, "/v1/tunnels/local-presets", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local preset list status=%d, want 200; body=%s", resp.StatusCode, responseBody(t, resp))
	}
	_ = resp.Body.Close()

	tok := seedJobToken(t, s, "job-hosted", jobstore.JobCredentialMember, "")
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		path := "/v1/tunnels/hosted/demo"
		got := do(t, s, method, path, tok, nil)
		if got.StatusCode != http.StatusForbidden {
			body := responseBody(t, got)
			t.Errorf("job caller %s %s status=%d, want 403; body=%s", method, path, got.StatusCode, body)
		} else {
			body := responseBody(t, got)
			if !strings.Contains(body, "job credential") {
				t.Errorf("job caller refusal body=%q, want job credential wording", body)
			}
		}
	}
}
