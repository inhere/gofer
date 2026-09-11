package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTunnelListTunnelsJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tunnels": []any{map[string]any{"id": "t1", "caller_id": "c", "worker_id": "w", "target": "127.0.0.1:1", "client_remote": "x", "started_at": "2026-01-01T00:00:00Z", "bytes_up": 3, "bytes_down": 4}}})
	}))
	defer ts.Close()
	got, e := New(ts.URL, "").ListTunnels()
	if e != nil || len(got) != 1 {
		t.Fatalf("%v %#v", e, got)
	}
	if got[0].CallerID != "c" || got[0].WorkerID != "w" || got[0].BytesDown != 4 || !got[0].StartedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("bad %#v", got[0])
	}
}
