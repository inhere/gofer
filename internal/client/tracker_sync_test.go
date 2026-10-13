package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncTrackerRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tracker/repos/tr%2F1/sync" && r.URL.Path != "/v1/tracker/repos/tr/1/sync" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"j1","tracker_id":"tr/1","project_key":"p","runner":"local","cwd":"a"}`))
	}))
	defer srv.Close()
	res, err := New(srv.URL, "t").SyncTrackerRepo("tr/1")
	if err != nil || res.JobID != "j1" || res.Cwd != "a" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestListTrackerSyncClients(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/tracker/repos/global/clients" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"clients":[{"tracker_id":"global","client_host":"h1","caller":"alice","last_sync_at":20,"summary":"s"}]}`))
	}))
	defer srv.Close()
	got, err := New(srv.URL, "t").ListTrackerSyncClients("global")
	if err != nil || len(got) != 1 || got[0].ClientHost != "h1" || got[0].Caller != "alice" || got[0].LastSyncAt != 20 {
		t.Fatalf("clients=%+v err=%v", got, err)
	}
}
