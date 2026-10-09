package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tunnel"
)

// TUN-03 acceptance: forwarder registration / heartbeat / expiry and the active
// connections grouped under the forwarder they belong to.

// forwarderBody is the registration body `gofer tun forward` sends.
func forwarderBody(worker, host string, pid int, specs ...map[string]any) map[string]any {
	return map[string]any{"worker": worker, "specs": specs, "host": host, "pid": pid}
}

// tcpSpecBody is one entry of that body's `specs` array.
func tcpSpecBody(localPort int, target, network string) map[string]any {
	return map[string]any{"network": network, "bind": "127.0.0.1", "local_port": localPort, "target": target}
}

// forwarderViewBody is the per-forwarder shape GET /v1/tunnels/forwarders returns,
// including the connection roll-up.
type forwarderViewBody struct {
	ID          string    `json:"id"`
	CallerID    string    `json:"caller_id"`
	Worker      string    `json:"worker"`
	Host        string    `json:"host"`
	PID         int       `json:"pid"`
	Hosted      bool      `json:"hosted"`
	HostedName  string    `json:"hosted_name"`
	StartedAt   time.Time `json:"started_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	Connections int       `json:"connections"`
	BytesUp     int64     `json:"bytes_up"`
	BytesDown   int64     `json:"bytes_down"`
	Specs       []struct {
		Network   string `json:"network"`
		Bind      string `json:"bind"`
		LocalPort int    `json:"local_port"`
		Target    string `json:"target"`
	} `json:"specs"`
}

// listForwarders reads the online forwarder list.
func listForwarders(t *testing.T, s *Server, token string) []forwarderViewBody {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/tunnels/forwarders", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET forwarders status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Forwarders []forwarderViewBody `json:"forwarders"`
	}
	decode(t, resp, &out)
	return out.Forwarders
}

// TestForwarderRegisterHeartbeatExpire pins the forwarder lifecycle: a registration
// becomes visible, a heartbeat keeps it alive PAST its original expiry, a silent
// forwarder disappears, and an explicit DELETE removes it at once.
//
// The registry TTL is shrunk to hundreds of milliseconds (the production default is
// 90s) by handing the server a registry of its own — the same seam
// server.tunnel.forwarder_ttl_sec drives, so the handler path under test is the real
// one.
func TestForwarderRegisterHeartbeatExpire(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	const ttl = 300 * time.Millisecond
	s.forwarders = tunnel.NewForwarderRegistry(func() time.Duration { return ttl })

	resp := do(t, s, http.MethodPost, "/v1/tunnels/forwarders", testToken,
		forwarderBody("w-hw", "workshop-pc", 4242, tcpSpecBody(1502, "192.168.0.205:502", "tcp")))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status=%d, want 200", resp.StatusCode)
	}
	var created struct {
		Forwarder forwarderViewBody `json:"forwarder"`
	}
	decode(t, resp, &created)
	id := created.Forwarder.ID
	if !strings.HasPrefix(id, "fw-") || len(id) != len("fw-")+8 {
		t.Fatalf("forwarder id = %q, want fw-<8hex>", id)
	}
	if created.Forwarder.CallerID != "default" {
		t.Fatalf("caller_id = %q, want the authenticated caller", created.Forwarder.CallerID)
	}

	if got := listForwarders(t, s, testToken); len(got) != 1 || got[0].ID != id {
		t.Fatalf("a fresh registration must be listed, got %#v", got)
	}

	// A heartbeat midway through the TTL keeps it alive past the original deadline.
	time.Sleep(ttl / 2)
	resp = do(t, s, http.MethodPut, "/v1/tunnels/forwarders/"+id, testToken, map[string]any{
		"specs": []map[string]any{tcpSpecBody(1502, "192.168.0.205:502", "tcp"), tcpSpecBody(11217, "127.0.0.1:1217", "tcp")},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status=%d, want 200", resp.StatusCode)
	}
	time.Sleep(ttl - ttl/4)
	got := listForwarders(t, s, testToken)
	if len(got) != 1 {
		t.Fatalf("a heartbeat must keep the forwarder alive past the original TTL, got %#v", got)
	}
	if len(got[0].Specs) != 2 {
		t.Fatalf("a heartbeat may carry the latest rules, got %#v", got[0].Specs)
	}

	// Silence past the TTL drops it (a killed forwarder leaves nothing behind).
	time.Sleep(ttl + ttl/2)
	if got := listForwarders(t, s, testToken); len(got) != 0 {
		t.Fatalf("a silent forwarder must expire, got %#v", got)
	}

	// The explicit goodbye removes it immediately, without waiting for the TTL.
	resp = do(t, s, http.MethodPost, "/v1/tunnels/forwarders", testToken,
		forwarderBody("w-hw", "workshop-pc", 4242, tcpSpecBody(1502, "192.168.0.205:502", "tcp")))
	decode(t, resp, &created)
	resp = do(t, s, http.MethodDelete, "/v1/tunnels/forwarders/"+created.Forwarder.ID, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status=%d, want 200", resp.StatusCode)
	}
	if got := listForwarders(t, s, testToken); len(got) != 0 {
		t.Fatalf("DELETE must remove the forwarder at once, got %#v", got)
	}

	// Unknown ids are a 404, not a silent success or a 500.
	resp = do(t, s, http.MethodDelete, "/v1/tunnels/forwarders/fw-deadbeef", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE unknown id status=%d, want 404", resp.StatusCode)
	}

	// A malformed registration is refused before anything is stored.
	resp = do(t, s, http.MethodPost, "/v1/tunnels/forwarders", testToken,
		forwarderBody("w-hw", "workshop-pc", 4242, tcpSpecBody(70000, "192.168.0.205:502", "tcp")))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("register with a bad local port status=%d, want 400", resp.StatusCode)
	}
	if got := listForwarders(t, s, testToken); len(got) != 0 {
		t.Fatalf("a refused registration must not be stored, got %#v", got)
	}
}

// TestForwarderListGroupsConnections: the two GET surfaces are joined on
// (caller, worker, target), so a forwarder that is actually carrying traffic shows
// how many sessions and how many bytes — the "is my tunnel being used?" question the
// plain active-tunnel list cannot answer for a running forwarder.
func TestForwarderListGroupsConnections(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})

	for _, tc := range []struct {
		id       string
		worker   string
		target   string
		up, down int64
	}{
		{id: "t-1", worker: "w-hw", target: "192.168.0.205:502", up: 100, down: 200},
		{id: "t-2", worker: "w-hw", target: "192.168.0.205:502", up: 300, down: 500},
		// A different worker must not be folded into the registration below.
		{id: "t-3", worker: "w-other", target: "192.168.0.205:502", up: 7, down: 7},
	} {
		upd, _ := s.tunnels.Activate(tunnel.Info{
			ID: tc.id, CallerID: "default", WorkerID: tc.worker, Target: tc.target,
			ClientRemote: "127.0.0.1:5000", StartedAt: time.Now(),
		})
		upd(tc.up, tc.down)
	}

	resp := do(t, s, http.MethodPost, "/v1/tunnels/forwarders", testToken,
		forwarderBody("w-hw", "workshop-pc", 4242, tcpSpecBody(1502, "192.168.0.205:502", "tcp")))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status=%d, want 200", resp.StatusCode)
	}

	got := listForwarders(t, s, testToken)
	if len(got) != 1 {
		t.Fatalf("want one forwarder, got %#v", got)
	}
	if got[0].Connections != 2 {
		t.Errorf("connections = %d, want 2 (the two matching sessions)", got[0].Connections)
	}
	if got[0].BytesUp != 400 || got[0].BytesDown != 700 {
		t.Errorf("bytes = %d/%d, want 400/700 (summed over the matching sessions)", got[0].BytesUp, got[0].BytesDown)
	}
}

// TestJobCallerCannotWriteForwarders: SEC-01 — the forwarder and preset writes are
// ordinary write routes, so a job credential is refused by the default-deny gate even
// though its reads pass. A job must never be able to advertise or edit tunnel rules.
func TestJobCallerCannotWriteForwarders(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))
	tok := seedJobToken(t, s, "job-forwarders", jobstore.JobCredentialMember, "")

	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/v1/tunnels/forwarders", nil, http.StatusOK},
		{http.MethodGet, "/v1/tunnels/presets", nil, http.StatusOK},
		{http.MethodPost, "/v1/tunnels/forwarders", forwarderBody("w-hw", "pc", 1, tcpSpecBody(1502, "192.168.0.205:502", "tcp")), http.StatusForbidden},
		{http.MethodPut, "/v1/tunnels/forwarders/fw-1a2b3c4d", map[string]any{}, http.StatusForbidden},
		{http.MethodDelete, "/v1/tunnels/forwarders/fw-1a2b3c4d", nil, http.StatusForbidden},
		{http.MethodPost, "/v1/tunnels/forwarders/fw-1a2b3c4d/stop", nil, http.StatusForbidden},
		{http.MethodPut, "/v1/tunnels/presets/demo", map[string]any{"worker": "w-hw", "specs": []string{"1502:192.168.0.205:502"}}, http.StatusForbidden},
		{http.MethodDelete, "/v1/tunnels/presets/demo", nil, http.StatusForbidden},
	} {
		resp := do(t, s, tc.method, tc.path, tok, tc.body)
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s with a job credential = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
		if tc.want == http.StatusForbidden {
			var body map[string]any
			decode(t, resp, &body)
			if msg, _ := body["error"].(string); !strings.Contains(msg, "job credential may not") ||
				(strings.HasSuffix(tc.path, "/stop") && !strings.Contains(msg, "stop a tunnel forwarder")) {
				t.Errorf("%s %s refusal body = %v, want the SEC-01 wording", tc.method, tc.path, body)
			}
		} else {
			_ = resp.Body.Close()
		}
	}
}

// TestPresetCRUDValidates: the server-side preset store takes the same validation the
// local file does (an unrunnable rule is a 400 and changes nothing), splits a
// comma-joined rule list on the way in, and is a plain CRUD surface otherwise.
func TestPresetCRUDValidates(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	s.SetTunnelPresets(openTestStore(t, t.TempDir()))

	resp := do(t, s, http.MethodPut, "/v1/tunnels/presets/demo", testToken,
		map[string]any{"worker": "w-hw", "specs": []string{"not-a-spec"}, "note": "现场"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT with a malformed spec status=%d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/tunnels/presets/demo", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a refused preset must not be stored: GET status=%d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// A comma-joined list is accepted and stored SPLIT (design §三): the store never
	// holds a compound rule.
	resp = do(t, s, http.MethodPut, "/v1/tunnels/presets/demo", testToken,
		map[string]any{"worker": "w-hw", "specs": []string{"1502:192.168.0.205:502,11217:127.0.0.1:1217"}, "note": "现场"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	type presetBody struct {
		Name      string   `json:"name"`
		Worker    string   `json:"worker"`
		Specs     []string `json:"specs"`
		Note      string   `json:"note"`
		UpdatedBy string   `json:"updated_by"`
	}
	var got struct {
		Preset presetBody `json:"preset"`
	}
	resp = do(t, s, http.MethodGet, "/v1/tunnels/presets/demo", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET preset status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &got)
	if len(got.Preset.Specs) != 2 || got.Preset.Specs[0] != "1502:192.168.0.205:502" || got.Preset.Specs[1] != "11217:127.0.0.1:1217" {
		t.Fatalf("stored specs = %#v, want the two rules split apart", got.Preset.Specs)
	}
	if got.Preset.Worker != "w-hw" || got.Preset.Note != "现场" || got.Preset.UpdatedBy != "default" {
		t.Fatalf("stored preset = %+v", got.Preset)
	}

	// Without `force`, an existing name is refused (the `tun save` rule); with it, the
	// write replaces the preset.
	body := map[string]any{"worker": "w-2", "specs": []string{"1600:10.0.0.9:502"}}
	resp = do(t, s, http.MethodPut, "/v1/tunnels/presets/demo", testToken, body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT over an existing preset status=%d, want 409", resp.StatusCode)
	}
	_ = resp.Body.Close()
	body["force"] = true
	resp = do(t, s, http.MethodPut, "/v1/tunnels/presets/demo", testToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forced PUT status=%d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	var list struct {
		Presets []presetBody `json:"presets"`
	}
	resp = do(t, s, http.MethodGet, "/v1/tunnels/presets", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET presets status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &list)
	if len(list.Presets) != 1 || list.Presets[0].Worker != "w-2" {
		t.Fatalf("preset list = %#v, want the forced replacement", list.Presets)
	}

	resp = do(t, s, http.MethodDelete, "/v1/tunnels/presets/demo", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status=%d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/tunnels/presets/demo", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE status=%d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodDelete, "/v1/tunnels/presets/demo", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE an absent preset status=%d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// registerForwarderAs registers one forwarder with the given token (and caps) and
// returns its id.
func registerForwarderAs(t *testing.T, s *Server, token string, caps ...string) string {
	t.Helper()
	body := forwarderBody("w-hw", "workshop-pc", 4242, tcpSpecBody(1502, "192.168.0.205:502", "tcp"))
	if caps != nil {
		body["caps"] = caps
	}
	resp := do(t, s, http.MethodPost, "/v1/tunnels/forwarders", token, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status=%d, want 200", resp.StatusCode)
	}
	var created struct {
		Forwarder struct {
			ID   string   `json:"id"`
			Caps []string `json:"caps"`
		} `json:"forwarder"`
	}
	decode(t, resp, &created)
	if len(caps) > 0 && len(created.Forwarder.Caps) != len(caps) {
		t.Fatalf("caps not echoed: %#v", created.Forwarder.Caps)
	}
	return created.Forwarder.ID
}

// TestForwarderRemoteStop: POST .../stop marks the registration (202, listed with
// stop_requested), the owner's next heartbeat answers 410 and removes it; an unknown id
// is 404, an old forwarder without the stop cap is 409 naming its host.
func TestForwarderRemoteStop(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken})
	id := registerForwarderAs(t, s, testToken, "stop")

	resp := do(t, s, http.MethodPost, "/v1/tunnels/forwarders/"+id+"/stop", testToken, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status=%d, want 202", resp.StatusCode)
	}
	var stopped struct {
		Forwarder struct {
			ID            string `json:"id"`
			StopRequested bool   `json:"stop_requested"`
		} `json:"forwarder"`
	}
	decode(t, resp, &stopped)
	if stopped.Forwarder.ID != id || !stopped.Forwarder.StopRequested {
		t.Fatalf("202 body = %#v, want the marked entry", stopped)
	}
	resp = do(t, s, http.MethodGet, "/v1/tunnels/forwarders", testToken, nil)
	var listed struct {
		Forwarders []struct {
			ID            string `json:"id"`
			StopRequested bool   `json:"stop_requested"`
		} `json:"forwarders"`
	}
	decode(t, resp, &listed)
	if len(listed.Forwarders) != 1 || !listed.Forwarders[0].StopRequested {
		t.Fatalf("pending stop must be listed with stop_requested, got %#v", listed)
	}

	resp = do(t, s, http.MethodPut, "/v1/tunnels/forwarders/"+id, testToken, map[string]any{})
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("heartbeat after stop status=%d, want 410", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if got := listForwarders(t, s, testToken); len(got) != 0 {
		t.Fatalf("the 410 heartbeat must remove the entry, got %#v", got)
	}

	resp = do(t, s, http.MethodPost, "/v1/tunnels/forwarders/fw-deadbeef/stop", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stop unknown id status=%d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()

	old := registerForwarderAs(t, s, testToken)
	resp = do(t, s, http.MethodPost, "/v1/tunnels/forwarders/"+old+"/stop", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stop without cap status=%d, want 409", resp.StatusCode)
	}
	var body map[string]any
	decode(t, resp, &body)
	if msg, _ := body["error"].(string); !strings.Contains(msg, "workshop-pc") || !strings.Contains(msg, "Ctrl+C") {
		t.Fatalf("409 body = %v, want the host + Ctrl+C advice", body)
	}
	// The refused request leaves the old forwarder's heartbeat working.
	resp = do(t, s, http.MethodPut, "/v1/tunnels/forwarders/"+old, testToken, map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat after refused stop status=%d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestForwarderRemoteStopOwnership: another plain caller gets 403; a can_admin caller
// may stop it (the owner-or-admin rule job deletion uses); a worker token is 403.
func TestForwarderRemoteStopOwnership(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: testToken, Callers: []config.CallerConfig{
		{ID: "bob", Token: "bob-token"},
		{ID: "root", Token: "root-token", CanAdmin: true},
	}})
	id := registerForwarderAs(t, s, testToken, "stop")

	resp := do(t, s, http.MethodPost, "/v1/tunnels/forwarders/"+id+"/stop", "bob-token", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign stop status=%d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/tunnels/forwarders/"+id+"/stop", "root-token", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("admin stop status=%d, want 202", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
