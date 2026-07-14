package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// registerWorker stands up a hub behind an httptest server, dials it as workerID
// and sends the given register frame, returning the hub once the worker is live.
// The connection is closed on test cleanup.
func registerWorker(t *testing.T, workerID string, reg wsproto.Register) *wshub.Hub {
	t.Helper()
	hub := wshub.New(map[string]string{workerID: workerID})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Accept(w, r, workerID)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })

	payload, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal register: %v", err)
	}
	if err := wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegister, Payload: payload}); err != nil {
		t.Fatalf("write register: %v", err)
	}
	var ack wsproto.Envelope
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		t.Fatalf("read registered: %v", err)
	}
	got, _ := wsproto.As[wsproto.Registered](ack)
	if !got.Accepted {
		t.Fatalf("register rejected: %s", got.Reason)
	}
	return hub
}

// workerRegister is the capability report the test worker announces.
var workerRegister = wsproto.Register{
	WorkerID:        "w1",
	InstanceID:      "inst-1",
	ProtocolVersion: wsproto.CurrentProtocolVersion,
	PtyCapable:      true,
	Labels:          []string{"gpu"},
	Projects:        []string{"alpha", "beta"},
	Agents:          []string{"claude", "exec"},
	AgentCaps: []wsproto.AgentBrief{
		{Key: "claude", Type: "cli-agent", Interactive: true},
		{Key: "exec", Type: "exec"},
	},
}

// TestHubWorkerSelectorCarriesCapabilities (federation P2 / T2.2): the hub→job
// projection must carry the worker's reported Projects/Agents. They used to be
// dropped here, so the job layer had no capability view of a remote worker.
func TestHubWorkerSelectorCarriesCapabilities(t *testing.T) {
	hub := registerWorker(t, "w1", workerRegister)
	sel := &hubWorkerSelector{
		hub:     hub,
		allowed: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
	}

	cands := sel.Candidates()
	if len(cands) != 1 {
		t.Fatalf("Candidates() = %d entries, want 1", len(cands))
	}
	c := cands[0]
	if c.WorkerID != "w1" || !c.PtyCapable || len(c.Labels) != 1 || c.Labels[0] != "gpu" {
		t.Fatalf("Candidates()[0] base fields = %+v", c)
	}
	if strings.Join(c.Projects, ",") != "alpha,beta" {
		t.Fatalf("Candidates()[0].Projects = %v, want [alpha beta]", c.Projects)
	}
	if strings.Join(c.Agents, ",") != "claude,exec" {
		t.Fatalf("Candidates()[0].Agents = %v, want [claude exec]", c.Agents)
	}

	one, ok := sel.Candidate("w1")
	if !ok {
		t.Fatal("Candidate(w1) not found")
	}
	if strings.Join(one.Projects, ",") != "alpha,beta" || strings.Join(one.Agents, ",") != "claude,exec" {
		t.Fatalf("Candidate(w1) caps = %v / %v", one.Projects, one.Agents)
	}
	if one.WorkerID != "w1" || !one.PtyCapable {
		t.Fatalf("Candidate(w1) base fields = %+v", one)
	}
}

// TestHubWorkerSelectorUnknownWorker: an id outside server.workers (or offline)
// yields no candidate — the capability view is absent, never empty-but-ok.
func TestHubWorkerSelectorUnknownWorker(t *testing.T) {
	hub := registerWorker(t, "w1", workerRegister)
	sel := &hubWorkerSelector{
		hub:     hub,
		allowed: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}},
	}
	if _, ok := sel.Candidate("w2"); ok {
		t.Fatal("Candidate(w2) must be false for an unregistered worker id")
	}
}
