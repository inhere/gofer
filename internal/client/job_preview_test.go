package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inhere/gofer/internal/preview"
)

// The list the server writes (preview.Lease, marshalled as the handler does) is what the
// client decodes: `gofer job preview ls` prints id / port / state / expiry from it.
func TestListJobPreviewsDecodesTheServersLeases(t *testing.T) {
	lease := preview.Lease{ID: "pv-1", JobID: "job-1", CallerID: "alice", WorkerID: "local", Port: 18101, State: "active", CreatedAt: 1700000000, ExpiresAt: 1700001800}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"previews": []preview.Lease{lease}})
	}))
	defer srv.Close()

	got, err := New(srv.URL, "").ListJobPreviews("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/jobs/job-1/previews" || len(got) != 1 {
		t.Fatalf("path=%s got=%+v", path, got)
	}
	if g := got[0]; g.ID != lease.ID || g.JobID != lease.JobID || g.Port != lease.Port || g.State != lease.State || g.CreatedAt != lease.CreatedAt || g.ExpiresAt != lease.ExpiresAt {
		t.Fatalf("decoded %+v, want the lease %+v", g, lease)
	}
}

func TestRenewJobPreviewPostsTTL(t *testing.T) {
	var method, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"id":"pv-1","state":"active","expires_at":1700003600,"port":18101}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "").RenewJobPreview("job-1", "pv-1", 1800)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/v1/jobs/job-1/previews/pv-1/renew" || body != `{"ttl_sec":1800}` {
		t.Fatalf("sent %s %s %s", method, path, body)
	}
	if got.ID != "pv-1" || got.State != "active" || got.ExpiresAt != 1700003600 {
		t.Fatalf("got %+v", got)
	}
}
