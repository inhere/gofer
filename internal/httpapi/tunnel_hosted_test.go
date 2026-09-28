package httpapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tunnel"
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
	s.SetHostedForwarderDial(func(_ context.Context, _ string, _ tunnel.ForwardSpec) (tunnel.DialResult, error) {
		return tunnel.DialResult{}, nil
	})
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
	if got := listForwarders(t, s, testToken); len(got) != 1 || !got[0].Hosted || got[0].HostedName != "demo" {
		t.Fatalf("hosted registrations=%+v, want one hosted demo", got)
	}
	dup := do(t, s, http.MethodPost, "/v1/tunnels/hosted/demo", testToken, nil)
	if dup.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate hosted start status=%d, want 409; body=%s", dup.StatusCode, responseBody(t, dup))
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer busy.Close()
	_, port, _ := net.SplitHostPort(busy.Addr().String())
	put = do(t, s, http.MethodPut, "/v1/tunnels/presets/busy", testToken, map[string]any{
		"worker": "w-local", "specs": []string{"127.0.0.1:" + port + ":127.0.0.1:1"},
	})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("busy preset status=%d, want 200", put.StatusCode)
	}
	_ = put.Body.Close()
	busyStart := do(t, s, http.MethodPost, "/v1/tunnels/hosted/busy", testToken, nil)
	if busyStart.StatusCode != http.StatusConflict || len(listForwarders(t, s, testToken)) != 1 {
		t.Fatalf("busy hosted start status=%d or registration leaked", busyStart.StatusCode)
	}
	stop := do(t, s, http.MethodDelete, "/v1/tunnels/hosted/demo", testToken, nil)
	if stop.StatusCode != http.StatusOK {
		t.Fatalf("hosted stop status=%d, want 200; body=%s", stop.StatusCode, responseBody(t, stop))
	}
	if got := listForwarders(t, s, testToken); len(got) != 0 {
		t.Fatalf("stopped hosted forwarders=%+v, want empty", got)
	}
}

func TestForwardAutostart(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))
	s.SetHostedForwarderDial(func(_ context.Context, _ string, _ tunnel.ForwardSpec) (tunnel.DialResult, error) {
		return tunnel.DialResult{}, nil
	})
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
	s.StartHostedAutostart()
	if got := listForwarders(t, s, testToken); len(got) != 1 || !got[0].Hosted {
		t.Fatalf("autostart forwarders=%+v, want one hosted entry", got)
	}
	s.StopHostedForwarders()
	manual := do(t, s, http.MethodPut, "/v1/tunnels/presets/manual", testToken, map[string]any{
		"worker": "w-local", "specs": []string{"0:127.0.0.1:1"},
	})
	if manual.StatusCode != http.StatusOK {
		t.Fatalf("manual preset status=%d, want 200", manual.StatusCode)
	}
	_ = manual.Body.Close()
	if got := listForwarders(t, s, testToken); len(got) != 0 {
		t.Fatalf("non-autostart preset unexpectedly started: %+v", got)
	}
}

func TestUnpushedLocalPresetsListed(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))
	configDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, configDir)
	if err := config.SaveTunnels(&config.Tunnels{Forwards: map[string]config.TunnelProfile{
		"local-only": {Worker: "w-local", Specs: []string{"0:127.0.0.1:1"}, Note: "local", Autostart: true},
	}}); err != nil {
		t.Fatalf("save local tunnels: %v", err)
	}
	resp := do(t, s, http.MethodGet, "/v1/tunnels/local-presets", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local preset list status=%d, want 200; body=%s", resp.StatusCode, responseBody(t, resp))
	}
	_ = resp.Body.Close()
	imported := do(t, s, http.MethodPost, "/v1/tunnels/local-presets/local-only", testToken, nil)
	if imported.StatusCode != http.StatusOK {
		t.Fatalf("local preset import status=%d, want 200; body=%s", imported.StatusCode, responseBody(t, imported))
	}
	resp = do(t, s, http.MethodGet, "/v1/tunnels/local-presets", testToken, nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(responseBody(t, resp), "local-only") {
		t.Fatalf("imported local preset should disappear from local list, status=%d", resp.StatusCode)
	}

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
