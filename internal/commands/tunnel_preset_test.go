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

// TUN-03 acceptance for the CLI: presets come from the SERVER first (with the local
// file as a documented fallback), `tun presets push` migrates them, and `tun ls`
// shows the forwarding processes next to the connections.

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

// TestCLIPresetPrefersServerFallsBackLocal: the server is the source of truth after
// TUN-03, so a same-named preset there wins; a preset only the LOCAL file knows is
// still honoured, with the nudge that `gofer tun presets push` would upload it. An
// unreachable server falls back the same way instead of failing the command.
func TestCLIPresetPrefersServerFallsBackLocal(t *testing.T) {
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

	// The server's copy wins, silently.
	got, err := resolveForward(cli, "demo", "", nil)
	if err != nil {
		t.Fatalf("resolveForward(server preset): %v", err)
	}
	if got.worker != "srv-w" || len(got.specs) != 2 {
		t.Fatalf("resolveForward(server preset) = %+v, want the server's worker and rules", got)
	}
	if got.note != "" {
		t.Errorf("a server preset must not print a fallback hint, got %q", got.note)
	}

	// Only the local file knows this one: honour it and say how to upload it.
	got, err = resolveForward(cli, "field", "", nil)
	if err != nil {
		t.Fatalf("resolveForward(local preset): %v", err)
	}
	if got.worker != "local-w" || len(got.specs) != 1 {
		t.Fatalf("resolveForward(local preset) = %+v, want the local worker and rules", got)
	}
	if !strings.Contains(got.note, "gofer tun presets push") {
		t.Errorf("a local fallback must point at `gofer tun presets push`, got %q", got.note)
	}

	// An unreachable server degrades to the same local answer rather than an error.
	dead := client.New("http://127.0.0.1:1", "")
	got, err = resolveForward(dead, "field", "", nil)
	if err != nil {
		t.Fatalf("resolveForward with an unreachable server: %v", err)
	}
	if got.worker != "local-w" || !strings.Contains(got.note, "gofer tun presets push") {
		t.Fatalf("unreachable server fallback = %+v", got)
	}

	// A name nobody knows is still an error.
	if _, err := resolveForward(cli, "ghost", "", nil); err == nil {
		t.Fatal("an unknown preset must fail")
	}
}

// TestPresetsPushSkipsConflicts: `tun presets push` uploads the local file and, by
// default, SKIPS a name the server already has — listing it rather than overwriting a
// preset somebody else may be using. --force is the deliberate overwrite.
func TestPresetsPushSkipsConflicts(t *testing.T) {
	st := newTunnelStub(t)
	st.presets["demo"] = map[string]any{"name": "demo", "worker": "old-w", "specs": []string{"1502:10.0.0.5:502"}}
	// Point the CLI at the stub FIRST: it also pins the config directory, so the local
	// file below lands where LoadTunnels will look for it.
	pointTunnelCLIAt(t, st.URL)
	if err := config.SaveTunnels(&config.Tunnels{Forwards: map[string]config.TunnelProfile{
		"demo":  {Worker: "w-hw", Specs: []string{"1502:192.168.0.205:502"}},
		"field": {Worker: "w-hw", Specs: []string{"1600:10.0.0.9:502"}},
	}}); err != nil {
		t.Fatalf("save local presets: %v", err)
	}

	push := bindCmd(findSub(t, findSub(t, NewTunnelCmd(), "presets"), "push"))
	out := captureOutput(t, func() {
		tunnelOpts.force = false
		if err := runTunnelPresetsPush(push, nil); err != nil {
			t.Fatalf("presets push: %v", err)
		}
	})
	if !strings.Contains(out, "pushed 1") {
		t.Errorf("push must report the uploaded count, got:\n%s", out)
	}
	if !strings.Contains(out, "demo") || !strings.Contains(out, "force") {
		t.Errorf("push must list the skipped conflict and how to force it, got:\n%s", out)
	}
	if st.presets["demo"]["worker"] != "old-w" {
		t.Errorf("a conflicting preset must not be overwritten without --force, got %#v", st.presets["demo"])
	}
	if _, ok := st.presets["field"]; !ok {
		t.Error("the non-conflicting preset must be uploaded")
	}

	// --force overwrites the conflict.
	out = captureOutput(t, func() {
		tunnelOpts.force = true
		if err := runTunnelPresetsPush(push, nil); err != nil {
			t.Fatalf("presets push --force: %v", err)
		}
	})
	if st.presets["demo"]["worker"] != "w-hw" {
		t.Errorf("--force must overwrite the server's preset, got %#v", st.presets["demo"])
	}
	if strings.Contains(out, "skipped") {
		t.Errorf("--force must not skip anything, got:\n%s", out)
	}
}

// TestTunnelForgetRemovesBothCopies: `tun forget` must leave the preset unresolvable.
// Deleting only the server's copy would leave the local fallback to answer
// `tun forward -n <name>` — the opposite of forgetting it.
func TestTunnelForgetRemovesBothCopies(t *testing.T) {
	st := newTunnelStub(t)
	st.presets["demo"] = map[string]any{"name": "demo", "worker": "srv-w", "specs": []string{"1502:10.0.0.5:502"}}
	pointTunnelCLIAt(t, st.URL)
	if err := config.SaveTunnels(&config.Tunnels{Forwards: map[string]config.TunnelProfile{
		"demo": {Worker: "local-w", Specs: []string{"1502:10.0.0.5:502"}},
	}}); err != nil {
		t.Fatalf("save local presets: %v", err)
	}

	forget := bindCmd(findSub(t, NewTunnelCmd(), "forget"))
	forget.Arg("name").WithValue("demo")
	out := captureOutput(t, func() {
		if err := runTunnelForget(forget, nil); err != nil {
			t.Fatalf("tun forget: %v", err)
		}
	})
	if !strings.Contains(out, "server and local") {
		t.Errorf("forget must report both copies, got:\n%s", out)
	}
	if _, ok := st.presets["demo"]; ok {
		t.Error("the server's copy must be gone")
	}
	local, err := config.LoadTunnels()
	if err != nil {
		t.Fatalf("load local presets: %v", err)
	}
	if _, ok := local.Forwards["demo"]; ok {
		t.Error("a leftover local copy would still answer `tun forward -n demo`")
	}

	// Nothing left anywhere is an error, not a silent success.
	if err := runTunnelForget(forget, nil); err == nil {
		t.Fatal("forgetting a preset that no longer exists must report it")
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
