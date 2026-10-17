package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEnrollClientAnonymousCalls(t *testing.T) {
	type seen struct{ method, path, query, auth, secret, body string }
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get(enrollSecretHeader), string(b)})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/worker-enroll":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"enroll_id":"we_1","pairing_code":"ABCD2345","decide_until":42}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"state":"accepted","worker_id":"gpu1","worker_token":"gw_x","collect_until":7}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	cli := New(srv.URL, "admin-token") // a token is configured but must not be sent anonymously

	secret, hash, err := NewEnrollSecret()
	if err != nil || len(hash) != 64 || hash != EnrollSecretHash(secret) {
		t.Fatalf("NewEnrollSecret = %q %q %v", secret, hash, err)
	}
	tk, err := cli.Enroll(EnrollRequest{RequestedID: "gpu1", Host: EnrollHost{Hostname: "h"}, SecretHash: hash})
	if err != nil || tk.EnrollID != "we_1" || tk.PairingCode != "ABCD2345" {
		t.Fatalf("Enroll = %+v %v", tk, err)
	}
	p, err := cli.PollEnroll("we_1", secret, 25*time.Second)
	if err != nil || p.State != "accepted" || p.WorkerToken != "gw_x" {
		t.Fatalf("PollEnroll = %+v %v", p, err)
	}
	if err := cli.ConfirmEnroll("we_1", secret); err != nil {
		t.Fatal(err)
	}
	if err := cli.CancelEnroll("we_1", secret); err != nil {
		t.Fatal(err)
	}
	for i, g := range got {
		if g.auth != "" {
			t.Errorf("call %d sent an Authorization header", i)
		}
	}
	if !strings.Contains(got[0].body, hash) || strings.Contains(got[0].body, secret) {
		t.Errorf("enroll body must carry the hash only: %s", got[0].body)
	}
	if got[1].path != "/v1/worker-enroll/we_1" || got[1].query != "wait=25" || got[1].secret != secret {
		t.Errorf("poll = %+v", got[1])
	}
	if got[2].path != "/v1/worker-enroll/we_1/confirm" || got[2].secret != secret || got[3].path != "/v1/worker-enroll/we_1/cancel" {
		t.Errorf("confirm/cancel = %+v %+v", got[2], got[3])
	}
}

func TestEnrollClientErrorsAndAdminCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/worker-enroll":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found","detail":"worker enrollment is disabled on this server"}`))
		case "/v1/worker-enrollments":
			if r.Header.Get("Authorization") != "Bearer admin" {
				t.Errorf("list without the admin token")
			}
			_, _ = w.Write([]byte(`{"enabled":true,"pending_count":1,"items":[{"id":"we_1","state":"pending","labels":[],"projects":[],"host":{"hostname":"h"},"source":"1.2.3.4","created_at":1,"decide_until":2}]}`))
		case "/v1/worker-enrollments/we_1/accept":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["pairing_code"] != "ABCD2345" || b["worker_id"] != "gpu1" {
				t.Errorf("accept body = %v", b)
			}
			_, _ = w.Write([]byte(`{"worker_id":"gpu1","collect_until":9}`))
		case "/v1/worker-enrollments/we_1/reject":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["reason"] != "no" || b["block_source_min"] != float64(10) {
				t.Errorf("reject body = %v", b)
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	cli := New(srv.URL, "admin")

	_, err := cli.Enroll(EnrollRequest{SecretHash: strings.Repeat("a", 64)})
	if StatusOf(err) != http.StatusNotFound || !strings.Contains(err.Error(), "disabled on this server") {
		t.Fatalf("Enroll on a disabled server: %v", err)
	}
	l, err := cli.ListEnrollments()
	if err != nil || !l.Enabled || l.PendingCount != 1 || len(l.Items) != 1 || l.Items[0].Source != "1.2.3.4" {
		t.Fatalf("ListEnrollments = %+v %v", l, err)
	}
	a, err := cli.AcceptEnrollment("we_1", "ABCD2345", "gpu1", nil, []string{"p"})
	if err != nil || a.WorkerID != "gpu1" || a.CollectUntil != 9 {
		t.Fatalf("AcceptEnrollment = %+v %v", a, err)
	}
	if err := cli.RejectEnrollment("we_1", "no", 10); err != nil {
		t.Fatal(err)
	}
}
