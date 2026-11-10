package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// holdListStub answers GET /v1/jobs with one job and records the raw query of each
// call (newClient also probes other paths, which are not this test's subject).
func holdListStub(t *testing.T, queries *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/jobs" {
			_, _ = w.Write([]byte("{}"))
			return
		}
		mu.Lock()
		*queries = append(*queries, r.URL.RawQuery)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{
			"id": "job-hold-1", "project_key": "self", "agent": "exec", "runner": "local",
			"status": "cancelled", "started_at": 100,
		}}})
	}))
}

// resetJobListHold clears the shared --hold flag value before and after a test, so a
// previous run's value does not leak in (the flag struct is package-global).
func resetJobListHold(t *testing.T) {
	t.Helper()
	jobListOpts.hold = ""
	t.Cleanup(func() { jobListOpts.hold = "" })
}

// TestJobListHoldFlagReachesServer runs `job ls --hold decided` through the real
// command dispatch and checks the stub server received hold=decided.
func TestJobListHoldFlagReachesServer(t *testing.T) {
	var queries []string
	ts := holdListStub(t, &queries)
	defer ts.Close()
	resetJobListHold(t)

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "ls", "--hold", "decided", "--server", ts.URL}); code != 0 {
			t.Fatalf("job ls --hold decided exit code=%d", code)
		}
	})
	if !strings.Contains(out, "job-hold-1") {
		t.Fatalf("output missing the listed job:\n%s", out)
	}
	if len(queries) != 1 || queries[0] != "hold=decided" {
		t.Fatalf("server queries = %v, want [hold=decided]", queries)
	}
}

// TestJobListWithoutHoldSendsNoHold: no --hold means the query carries no hold key.
func TestJobListWithoutHoldSendsNoHold(t *testing.T) {
	var queries []string
	ts := holdListStub(t, &queries)
	defer ts.Close()
	resetJobListHold(t)

	app := NewApp("test")
	captureOutput(t, func() {
		if code := app.Run([]string{"job", "ls", "--server", ts.URL}); code != 0 {
			t.Fatalf("job ls exit code=%d", code)
		}
	})
	if len(queries) != 1 || strings.Contains(queries[0], "hold") {
		t.Fatalf("server queries = %v, want no hold key", queries)
	}
}

// TestJobListInvalidHoldFailsLocally: --hold bad is refused before any list request.
func TestJobListInvalidHoldFailsLocally(t *testing.T) {
	var queries []string
	ts := holdListStub(t, &queries)
	defer ts.Close()
	resetJobListHold(t)

	app := NewApp("test")
	captureOutput(t, func() {
		if code := app.Run([]string{"job", "ls", "--hold", "bad", "--server", ts.URL}); code == 0 {
			t.Fatalf("job ls --hold bad exited 0, want a local error")
		}
	})
	if len(queries) != 0 {
		t.Fatalf("an invalid --hold reached the server: %v", queries)
	}
}
