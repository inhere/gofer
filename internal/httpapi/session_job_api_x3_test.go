package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

func TestSessionJobHTTPSTurnAndEnd(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"},
	})
	var result job.JobResult
	decode(t, created, &result)
	_ = waitDone(t, s, result.ID)
	for _, action := range []string{"say", "end"} {
		resp := do(t, s, http.MethodPost, "/v1/jobs/"+result.ID+"/"+action, testToken, map[string]string{"message": "next"})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s on non-session job status=%d, want 409", action, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestSessionJobSayEndCallerScope(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs/no-such-session/say", testToken, map[string]string{"message": "next"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session say status=%d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
