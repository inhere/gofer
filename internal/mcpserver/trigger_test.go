package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/trigger"
)

const mcpTriggerView = `{"name":"source-update","enabled":true,"owner_caller":"automation","project":"demo","template":"tpl","provider":"generic",
"auth":{"kind":"hmac-sha256","secret_env":"GOFER_TRIGGER_SRC","secret_configured":true,"timestamp_header":"X-T","signature_header":"X-S","replay_window_sec":300},
"dedup":{"ttl_sec":86400},"rate":{},"limits":{"max_body_bytes":1000,"max_var_bytes":100},"available":true}`

func triggerMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/triggers", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"triggers":[` + mcpTriggerView + `]}`))
	})
	mux.HandleFunc("/v1/triggers/source-update", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(mcpTriggerView))
	})
	mux.HandleFunc("/v1/triggers/source-update/deliveries", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "3" {
			t.Errorf("deliveries limit = %q, want 3", r.URL.Query().Get("limit"))
		}
		_, _ = w.Write([]byte(`{"trigger":"source-update","deliveries":[{"event_hash":"0123456789abcdef","status":"submitted","job_id":"job-1","received_at":1}]}`))
	})
	mux.HandleFunc("/v1/triggers/denied", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"trigger read refused","detail":"a job credential cannot read trigger configuration","code":"trigger_read_job_credential"}`))
	})
	return mux
}

func TestTriggerToolsReturnTheRedactedViews(t *testing.T) {
	session := connectTo(t, newServer(mockBackend(t, triggerMux(t)), ""))
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gofer_trigger_list"})
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Triggers []trigger.View `json:"triggers"`
	}
	structured(t, res, &list)
	if len(list.Triggers) != 1 || list.Triggers[0].Name != "source-update" || !list.Triggers[0].Auth.SecretConfigured || list.Triggers[0].Auth.SecretEnv != "GOFER_TRIGGER_SRC" {
		t.Fatalf("list = %+v", list)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gofer_trigger_show", Arguments: map[string]any{"name": "source-update", "deliveries": 3}})
	if err != nil {
		t.Fatal(err)
	}
	var show triggerShowOutput
	structured(t, res, &show)
	if show.Trigger.Name != "source-update" || len(show.Deliveries) != 1 || show.Deliveries[0].JobID != "job-1" {
		t.Fatalf("show = %+v", show)
	}
	raw, _ := json.Marshal(show)
	if strings.Contains(string(raw), "secret_value") {
		t.Fatalf("view carries secret material: %s", raw)
	}

	// deliveries: 0 asks for none (no request to the deliveries route is made, the stub would
	// fail the limit check otherwise).
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gofer_trigger_show", Arguments: map[string]any{"name": "source-update", "deliveries": 0}})
	if err != nil {
		t.Fatal(err)
	}
	structured(t, res, &show)
	if len(show.Deliveries) != 0 {
		t.Fatalf("deliveries = %+v, want none", show.Deliveries)
	}
}

func TestTriggerToolsSurfaceTheServersRefusal(t *testing.T) {
	session := connectTo(t, newServer(mockBackend(t, triggerMux(t)), ""))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_trigger_show", Arguments: map[string]any{"name": "denied"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a refused read must be an error result")
	}
	if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_trigger_show", Arguments: map[string]any{"name": " "}}); err == nil && !res.IsError {
		t.Fatal("a blank name must be refused")
	}
}

// TestTriggerToolsNeedAServerAndAreOperatorOnly: the standalone (local) MCP has no
// triggers to show, and a project-scoped MCP does not register the tools at all.
func TestTriggerToolsNeedAServerAndAreOperatorOnly(t *testing.T) {
	jobs, projects, agents := testCore(t)
	local := newLocalBackend(jobs, projects, agents)
	if _, err := local.ListTriggers(); err == nil {
		t.Error("the local backend must refuse ListTriggers")
	}
	if _, err := local.GetTrigger("x"); err == nil {
		t.Error("the local backend must refuse GetTrigger")
	}
	scopedSession := connectTo(t, newServer(local, "self"))
	tools, err := scopedSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if strings.HasPrefix(tl.Name, "gofer_trigger_") {
			t.Errorf("a project-scoped MCP registers %s", tl.Name)
		}
	}
}
