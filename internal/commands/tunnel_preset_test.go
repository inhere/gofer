package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// TUN-03 acceptance for the CLI: presets come from the SERVER, and `tun ls` shows the
// forwarding processes next to the connections.

// tunnelStub is a stub hub for the tunnel CLI tests: presets, online forwarders and
// active connections, plus the request log the assertions read.
type tunnelStub struct {
	*httptest.Server
	presets     map[string]map[string]any
	forwarders  []map[string]any
	connections []map[string]any
	requests    []string
}

func newTunnelStub(t *testing.T) *tunnelStub {
	t.Helper()
	st := &tunnelStub{presets: map[string]map[string]any{}}
	st.Server = httptest.NewServer(http.HandlerFunc(st.handle))
	t.Cleanup(st.Server.Close)
	return st
}

func (s *tunnelStub) handle(w http.ResponseWriter, r *http.Request) {
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/v1/tunnels")
	writeStub := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	fail := func(status int, msg string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
	}
	switch {
	case path == "/forwarders" && r.Method == http.MethodGet:
		writeStub(map[string]any{"forwarders": s.forwarders})
	case path == "" && r.Method == http.MethodGet:
		writeStub(map[string]any{"tunnels": s.connections})
	case path == "/presets" && r.Method == http.MethodGet:
		out := make([]map[string]any, 0, len(s.presets))
		for _, p := range s.presets {
			out = append(out, p)
		}
		writeStub(map[string]any{"presets": out})
	case strings.HasPrefix(path, "/presets/"):
		name := strings.TrimPrefix(path, "/presets/")
		switch r.Method {
		case http.MethodGet:
			p, ok := s.presets[name]
			if !ok {
				fail(http.StatusNotFound, "tunnel preset not found")
				return
			}
			writeStub(map[string]any{"preset": p})
		case http.MethodPut:
			var body struct {
				Worker string   `json:"worker"`
				Specs  []string `json:"specs"`
				Note   string   `json:"note"`
				Force  bool     `json:"force"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				fail(http.StatusBadRequest, "bad body")
				return
			}
			if _, exists := s.presets[name]; exists && !body.Force {
				fail(http.StatusConflict, `tunnel preset "`+name+`" already exists (use --force to overwrite)`)
				return
			}
			p := map[string]any{"name": name, "worker": body.Worker, "specs": body.Specs, "note": body.Note}
			s.presets[name] = p
			writeStub(map[string]any{"preset": p})
		case http.MethodDelete:
			if _, ok := s.presets[name]; !ok {
				fail(http.StatusNotFound, "tunnel preset not found")
				return
			}
			delete(s.presets, name)
			writeStub(map[string]any{"deleted": true})
		default:
			fail(http.StatusMethodNotAllowed, "unsupported")
		}
	default:
		fail(http.StatusNotFound, "not stubbed: "+r.Method+" "+r.URL.Path)
	}
}

// pointTunnelCLIAt makes tunnelClient() talk to url: the stub address arrives through
// the same -s/--server flag a user would pass, and the workspace's ambient
// GOFER_SERVER_* / job-token env is cleared so the test can never reach the real hub.
func pointTunnelCLIAt(t *testing.T, url string) {
	t.Helper()
	isolateConfigEnv(t)
	t.Setenv("GOFER_SERVER_ADDR", "")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	t.Setenv("GOFER_JOB_TOKEN", "")
	config.InputCfgFile = ""
	jobConnOpts.server, jobConnOpts.token = url, ""
	t.Cleanup(func() {
		config.InputCfgFile = ""
		jobConnOpts.server, jobConnOpts.token = "", ""
		tunnelOpts.force, tunnelOpts.name, tunnelOpts.worker = false, "", ""
	})
}

// TestCLIPresetComesFromServerOnly: the server is the only place presets live. A name
// it has resolves; a name it does not have, or a server that cannot answer, is an error
// even when a legacy local tunnels.yaml still holds the name.
func TestCLIPresetComesFromServerOnly(t *testing.T) {
	pointTunnelCLIAt(t, "")
	if err := config.SaveTunnels(&config.Tunnels{Forwards: map[string]config.TunnelProfile{
		"demo":  {Worker: "local-w", Specs: []string{"1502:10.0.0.5:502"}},
		"field": {Worker: "local-w", Specs: []string{"1502:10.0.0.6:502"}},
	}}); err != nil {
		t.Fatalf("save local presets: %v", err)
	}

	st := newTunnelStub(t)
	st.presets["demo"] = map[string]any{
		"name": "demo", "worker": "srv-w",
		"specs": []string{"1502:192.168.0.205:502", "11217:127.0.0.1:1217"},
	}
	cli := client.New(st.URL, "")

	got, err := resolveForward(cli, "demo", "", nil)
	if err != nil {
		t.Fatalf("resolveForward(server preset): %v", err)
	}
	if got.worker != "srv-w" || len(got.specs) != 2 {
		t.Fatalf("resolveForward(server preset) = %+v, want the server's worker and rules", got)
	}

	// Only the local file knows this one: it is not consulted any more.
	if _, err := resolveForward(cli, "field", "", nil); err == nil {
		t.Fatal("a preset only the local file knows must not resolve")
	}
	// An unreachable server is an error, not a silent local answer.
	dead := client.New("http://127.0.0.1:1", "")
	if _, err := resolveForward(dead, "field", "", nil); err == nil {
		t.Fatal("an unreachable server must fail the lookup")
	}
	// No server configured at all.
	if _, err := resolveForward(nil, "field", "", nil); err == nil {
		t.Fatal("no server must fail the lookup")
	}
	if _, err := resolveForward(cli, "ghost", "", nil); err == nil {
		t.Fatal("an unknown preset must fail")
	}
}

// TestTunnelForgetDeletesOnServer: `tun forget` removes the server's preset and reports
// a name that is not there.
func TestTunnelForgetDeletesOnServer(t *testing.T) {
	st := newTunnelStub(t)
	st.presets["demo"] = map[string]any{"name": "demo", "worker": "srv-w", "specs": []string{"1502:10.0.0.5:502"}}
	pointTunnelCLIAt(t, st.URL)

	forget := bindCmd(findSub(t, NewTunnelCmd(), "forget"))
	forget.Arg("name").WithValue("demo")
	out := captureOutput(t, func() {
		if err := runTunnelForget(forget, nil); err != nil {
			t.Fatalf("tun forget: %v", err)
		}
	})
	if !strings.Contains(out, "deleted preset demo") {
		t.Errorf("forget must report the deletion, got:\n%s", out)
	}
	if _, ok := st.presets["demo"]; ok {
		t.Error("the server's copy must be gone")
	}
	if err := runTunnelForget(forget, nil); err == nil {
		t.Fatal("forgetting a preset that no longer exists must report it")
	}
}

// TestTunnelSaveNeedsServer: `tun save` writes to the server; with none reachable it
// fails instead of keeping a local copy.
func TestTunnelSaveNeedsServer(t *testing.T) {
	pointTunnelCLIAt(t, "http://127.0.0.1:1")
	save := bindCmd(findSub(t, NewTunnelCmd(), "save"))
	save.Arg("name").WithValue("demo")
	save.Arg("spec").WithValue([]string{"1502:10.0.0.5:502"})
	tunnelOpts.worker = "w-hw"
	if err := runTunnelSave(save, nil); err == nil {
		t.Fatal("save with an unreachable server must fail")
	}
	if local, err := config.LoadTunnels(); err != nil || len(local.Forwards) != 0 {
		t.Fatalf("save must not write a local copy: %v %+v", err, local)
	}
}

// TestTunLsShowsForwarders: `tun ls` reports the forwarding PROCESSES first (which is
// what a user missing a listener needs to see) and the active connections after it.
func TestTunLsShowsForwarders(t *testing.T) {
	pointTunnelCLIAt(t, "")

	st := newTunnelStub(t)
	st.forwarders = []map[string]any{{
		"id": "fw-1a2b3c4d", "caller_id": "default", "worker": "w-hw",
		"specs": []map[string]any{
			{"network": "udp", "bind": "127.0.0.1", "local_port": 21845, "target": "192.168.0.253:21845"},
			{"network": "tcp", "bind": "127.0.0.1", "local_port": 1502, "target": "192.168.0.205:502"},
		},
		"host": "workshop-pc", "pid": 4242,
		"started_at": "2026-09-24T10:00:00Z", "last_seen_at": "2026-09-24T10:00:20Z",
		"connections": 2, "bytes_up": 1024, "bytes_down": 2048,
	}}
	st.connections = []map[string]any{{
		"id": "t-abc123", "caller_id": "default", "worker_id": "w-hw",
		"target": "192.168.0.205:502", "client_remote": "127.0.0.1:50123",
		"started_at": "2026-09-24T10:00:05Z", "bytes_up": 100, "bytes_down": 200,
	}}
	pointTunnelCLIAt(t, st.URL)

	out := captureOutput(t, func() {
		if err := runTunnelList(bindCmd(findSub(t, NewTunnelCmd(), "ls")), nil); err != nil {
			t.Fatalf("tun ls: %v", err)
		}
	})
	for _, want := range []string{
		"FORWARDERS", "fw-1a2b3c4d", "w-hw", "workshop-pc", "4242",
		"udp/21845 -> 192.168.0.253:21845", "1502 -> 192.168.0.205:502",
		"CONNECTIONS", "t-abc123", "127.0.0.1:50123",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tun ls output must contain %q, got:\n%s", want, out)
		}
	}
}
