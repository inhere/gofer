package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkerListReadsServerMeta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/meta" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"workers": []any{
			map[string]any{"id": "w2", "connected": false, "labels": []string{"gpu"}, "projects": []string{"demo"}, "agents": []string{"claude"}},
		}})
	}))
	defer server.Close()
	jobConnOpts.server = server.URL
	jobConnOpts.token = ""
	defer func() { jobConnOpts.server, jobConnOpts.token = "", "" }()

	c := bindCmd(NewWorkerListCmd())
	if err := runWorkerList(c, nil); err != nil {
		t.Fatalf("worker list: %v", err)
	}
}
