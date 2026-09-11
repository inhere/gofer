package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tunnel"
)

type tunnelTestHub struct {
	live    bool
	openErr error
}

func (h *tunnelTestHub) Accept(http.ResponseWriter, *http.Request, string)       {}
func (h *tunnelTestHub) LiveInstance(string) (string, bool)                      { return "inst-1", h.live }
func (h *tunnelTestHub) OpenTunnel(string, string, string, string, string) error { return h.openErr }

func TestTunnelHandlerAuthAndValidation(t *testing.T) {
	cases := []struct {
		name, path string
		want       int
	}{
		{"missing token", "/v1/tunnels/connect?worker=w1&target=127.0.0.1:80", http.StatusUnauthorized},
		{"worker token", "/v1/tunnels/connect?worker=w1&target=127.0.0.1:80", http.StatusForbidden},
		{"bad target", "/v1/tunnels/connect?worker=w1&target=bad", http.StatusBadRequest},
		{"udp", "/v1/tunnels/connect?worker=w1&target=127.0.0.1:80&network=udp", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.ServerConfig{Token: "api-token", Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "worker-token"}}}
			s := newTestServerCfg(t, cfg)
			h := &tunnelTestHub{live: true}
			s.hub = h
			s.router = s.buildRouter()
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.name == "worker token" {
				r.Header.Set("Authorization", "Bearer worker-token")
			}
			if tc.name != "missing token" && tc.name != "worker token" {
				r.Header.Set("Authorization", "Bearer api-token")
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want %d", w.Code, tc.want)
			}
		})
	}
}

func TestTunnelHandlerOpenErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{tunnel.ErrWorkerOffline, http.StatusNotFound}, {tunnel.ErrUnsupported, http.StatusConflict}, {errors.New("boom"), http.StatusBadGateway}} {
		cfg := config.ServerConfig{Token: "api-token"}
		s := newTestServerCfg(t, cfg)
		s.hub = &tunnelTestHub{live: true, openErr: tc.err}
		s.router = s.buildRouter()
		r := httptest.NewRequest(http.MethodGet, "/v1/tunnels/connect?worker=w1&target=127.0.0.1:80", nil)
		r.Header.Set("Authorization", "Bearer api-token")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("status=%d want %d", w.Code, tc.want)
		}
	}
}

func TestTunnelHandlerCapabilityRequired(t *testing.T) {
	cfg := config.ServerConfig{Token: "api-token", Governance: config.GovernanceConfig{RequireTunnelCapability: true}, Callers: []config.CallerConfig{{ID: "operator", Token: "caller-token", CanTunnel: true}}}
	s := newTestServerCfg(t, cfg)
	s.hub = &tunnelTestHub{live: true, openErr: tunnel.ErrWorkerOffline}
	s.router = s.buildRouter()
	for _, tc := range []struct {
		token string
		want  int
	}{{"api-token", http.StatusForbidden}, {"caller-token", http.StatusNotFound}} {
		r := httptest.NewRequest(http.MethodGet, "/v1/tunnels/connect?worker=w1&target=127.0.0.1:80", nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("status=%d want %d", w.Code, tc.want)
		}
	}
}

func TestTunnelHandlerRendezvousTimeout(t *testing.T) {
	old := tunnelRendezvousTimeout
	tunnelRendezvousTimeout = 10 * time.Millisecond
	defer func() { tunnelRendezvousTimeout = old }()
	s := newTestServerCfg(t, config.ServerConfig{Token: "api-token"})
	s.hub = &tunnelTestHub{live: true}
	s.router = s.buildRouter()
	r := httptest.NewRequest(http.MethodGet, "/v1/tunnels/connect?worker=w1&target=127.0.0.1:80", nil)
	r.Header.Set("Authorization", "Bearer api-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d want %d", w.Code, http.StatusGatewayTimeout)
	}
}
