package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestSecretScanOwnerScopeAndAdmin(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
		{ID: "admin", Token: "tok-admin", CanAdmin: true},
	}})
	for _, item := range []struct{ id, owner string }{{"scan-http-alice", "alice"}, {"scan-http-bob", "bob"}} {
		job := jobstore.JobRecord{ID: item.id, ProjectKey: "proj", Agent: "exec", Runner: "local", Status: "done", ResultDir: t.TempDir(), RequestJSON: `{"title":"http-secret-77"}`, StartedAt: 100, EndedAt: 200, UpdatedAt: 200, CallerID: item.owner}
		if err := s.jobs.Meta().UpsertJob(job); err != nil {
			t.Fatal(err)
		}
	}
	body := map[string]any{"literals": []string{"http-secret-77"}}
	for _, tc := range []struct {
		name, token string
		want        int
	}{{"owner", "tok-alice", 1}, {"other-owner", "tok-bob", 1}, {"admin", "tok-admin", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/jobs/secret-scan", tc.token, body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d, want 200", resp.StatusCode)
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "http-secret-77") {
				t.Fatalf("response echoed secret: %s", data)
			}
			if !strings.Contains(string(data), `"jobs"`) || !strings.Contains(string(data), `scan-http-`) {
				t.Fatalf("response missing aggregate jobs: %s", data)
			}
			if strings.Count(string(data), `"job_id"`) != tc.want {
				t.Fatalf("job count in response=%d, want %d: %s", strings.Count(string(data), `"job_id"`), tc.want, data)
			}
		})
	}
}
