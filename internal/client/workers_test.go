package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListWorkers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/meta", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"workers": []any{
			map[string]any{"id": "w1", "connected": true, "labels": []string{"docker"}, "agents": []string{"claude"}},
		}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	workers, err := New(ts.URL, "").ListWorkers()
	if err != nil {
		t.Fatalf("ListWorkers: %v", err)
	}
	if len(workers) != 1 || workers[0].ID != "w1" || !workers[0].Connected || len(workers[0].Agents) != 1 {
		t.Fatalf("unexpected workers: %+v", workers)
	}
}
