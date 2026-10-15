package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tunnel"
)

// A socks preset runs as a hosted forward like any other: the registration shows the
// dynamic rule, and each connection dials the target its client asked for.
func TestServerHostedSOCKSForward(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))
	var mu sync.Mutex
	var dialed []string
	s.SetHostedForwarderDial(func(_ context.Context, _, worker, network, target string) (tunnel.DialResult, error) {
		mu.Lock()
		dialed = append(dialed, worker+"|"+network+"|"+target)
		mu.Unlock()
		return tunnel.DialResult{}, errors.New("worker offline")
	})
	t.Cleanup(s.StopHostedForwarders)

	put := do(t, s, http.MethodPut, "/v1/tunnels/presets/web", testToken, map[string]any{
		"worker": "w-local", "specs": []string{"socks/0"},
	})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("seed preset status=%d; body=%s", put.StatusCode, responseBody(t, put))
	}
	_ = put.Body.Close()
	start := do(t, s, http.MethodPost, "/v1/tunnels/hosted/web", testToken, nil)
	if start.StatusCode != http.StatusOK {
		t.Fatalf("hosted start status=%d; body=%s", start.StatusCode, responseBody(t, start))
	}
	_ = start.Body.Close()
	got := listForwarders(t, s, testToken)
	if len(got) != 1 || len(got[0].Specs) != 1 {
		t.Fatalf("registrations=%+v", got)
	}
	sp := got[0].Specs[0]
	if sp.Network != tunnel.NetworkSOCKS || sp.Target != tunnel.SOCKSTarget || sp.LocalPort == 0 {
		t.Fatalf("socks spec view=%+v", sp)
	}

	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(sp.LocalPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte{0x05, 0x01, 0x00, 0x05, 0x01, 0x00, 0x01, 10, 20, 1, 5, 0x01, 0xF6})
	reply := make([]byte, 12)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	if reply[3] != 0x01 {
		t.Fatalf("SOCKS reply % x, want general failure for an offline worker", reply)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 || dialed[0] != "w-local|tcp|10.20.1.5:502" {
		t.Fatalf("dialed %v", dialed)
	}
}

// A socks listener has no authentication, so a preset binding it to a non-loopback
// address is refused when it is saved, not when it is started.
func TestTunnelPresetRejectsNonLoopbackSOCKS(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))
	put := do(t, s, http.MethodPut, "/v1/tunnels/presets/open", testToken, map[string]any{
		"worker": "w-local", "specs": []string{"socks/0.0.0.0:1080"},
	})
	if put.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", put.StatusCode, responseBody(t, put))
	}
	_ = put.Body.Close()
}

func TestParseForwarderSpecsAcceptsSOCKS(t *testing.T) {
	got, err := parseForwarderSpecs([]forwarderSpecBody{{Network: "socks", LocalPort: 1080}, {Network: "tcp", LocalPort: 1502, Target: "10.20.1.5:502"}})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].IsSOCKS() || got[0].Target != tunnel.SOCKSTarget || got[0].Bind != "127.0.0.1" {
		t.Fatalf("socks spec=%+v", got[0])
	}
	reg := tunnel.ForwarderRegistration{Specs: got[:1]}
	if !forwarderCarries(reg, "10.20.1.9:443") {
		t.Fatal("a socks forwarder carries any target of its caller and worker")
	}
}
