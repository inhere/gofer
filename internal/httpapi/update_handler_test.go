package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/updatecheck"
)

// gofer-6x2n: GET /v1/update reads the last check, POST /v1/update/check runs one;
// a job credential may read but not trigger a check.
func TestUpdateEndpoints(t *testing.T) {
	t.Parallel()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("name: gofer\nversion: 9.9.9\n"))
	}))
	defer mirror.Close()
	s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: "tok-user"}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)

	resp := do(t, s, http.MethodGet, "/v1/update", "tok-user", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no checker: status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	s.SetUpdateChecker(updatecheck.New("0.1.0", func() config.UpdateConfig {
		return config.UpdateConfig{LatestURL: mirror.URL + "/latest.yaml"}
	}))
	var st updatecheck.Status
	resp = do(t, s, http.MethodGet, "/v1/update", "tok-user", nil)
	decode(t, resp, &st)
	if st.CheckedAt != 0 || st.Current != "0.1.0" || st.Source != "mirror" {
		t.Fatalf("before a check: %+v", st)
	}
	resp = do(t, s, http.MethodPost, "/v1/update/check", "tok-user", nil)
	decode(t, resp, &st)
	if st.CheckedAt == 0 || !st.Newer || st.Latest != "9.9.9" || st.Error != "" {
		t.Fatalf("after a check: %+v", st)
	}
	resp = do(t, s, http.MethodGet, "/v1/update", "tok-user", nil)
	decode(t, resp, &st)
	if st.Latest != "9.9.9" {
		t.Fatalf("the check was not kept: %+v", st)
	}

	j := submitExecJob(t, s, "tok-user")
	tok := seedJobToken(t, s, j.ID, jobstore.JobCredentialMember, "")
	resp = do(t, s, http.MethodGet, "/v1/update", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job read status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/update/check", tok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("job check status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}
