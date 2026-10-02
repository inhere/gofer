package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterAndRemoveWorker(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"worker_id":"w1","worker_token":"tok","worker_connect_url":"ws://x/v1/workers/connect"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	cli := New(srv.URL, "admin")
	reg, err := cli.RegisterWorker("w1", []string{"linux"}, []string{"p"})
	if err != nil || reg.WorkerToken != "tok" {
		t.Fatalf("RegisterWorker=%+v err=%v", reg, err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/workers" {
		t.Fatalf("request=%s %s", gotMethod, gotPath)
	}
	if err := cli.RemoveWorker("w1"); err != nil {
		t.Fatalf("RemoveWorker: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v1/workers/w1" {
		t.Fatalf("remove request=%s %s", gotMethod, gotPath)
	}
}
