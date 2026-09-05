package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListRunners(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/runners", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"runners": []any{
			map[string]any{"name": "local", "type": "local", "status": "up", "capabilities": map[string]any{
				"agent_caps": []any{map[string]any{"key": "exec", "type": "exec", "available": true}},
			}},
		}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	runners, err := New(ts.URL, "").ListRunners()
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}
	if len(runners) != 1 || runners[0].Name != "local" || runners[0].Capabilities == nil {
		t.Fatalf("unexpected runners: %+v", runners)
	}
	if len(runners[0].Capabilities.AgentCaps) != 1 || runners[0].Capabilities.AgentCaps[0].Key != "exec" {
		t.Fatalf("unexpected capabilities: %+v", runners[0].Capabilities)
	}
}
