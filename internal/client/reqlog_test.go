package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// requestLogRows points the client request log at a temp file for the duration of
// the test and returns a reader for the JSONL rows written so far.
func requestLogRows(t *testing.T) func() []map[string]any {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run", "client.log")
	SetRequestLogPath(path)
	t.Cleanup(func() { SetRequestLogPath("") })
	return func() []map[string]any {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("read client log: %v", err)
		}
		var rows []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line == "" {
				continue
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("client log line is not JSON: %v (%q)", err, line)
			}
			rows = append(rows, row)
		}
		return rows
	}
}

// TestRequestFailureIsLogged (real call path): a server-side 5xx reached through
// GetJob leaves one ERROR line with method/path/status/elapsed and no query
// string, body or token. The evidence for LG3 was a request that failed with no
// trace on either side.
func TestRequestFailureIsLogged(t *testing.T) {
	rows := requestLogRows(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("path should carry no query, got %q", r.URL.RawQuery)
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "secret-token")
	if _, err := c.GetJob("job-1"); err == nil {
		t.Fatal("want an error from the 500")
	}

	got := rows()
	if len(got) != 1 {
		t.Fatalf("want exactly one client log row, got %d: %v", len(got), got)
	}
	row := got[0]
	if row["event"] != "client.request_failed" {
		t.Fatalf("event=%v", row["event"])
	}
	if row["method"] != "GET" || row["path"] != "/v1/jobs/job-1" {
		t.Fatalf("method/path = %v %v", row["method"], row["path"])
	}
	if row["status"] != float64(500) {
		t.Fatalf("status=%v", row["status"])
	}
	if row["error"] != "Internal Server Error" {
		t.Fatalf("error summary=%v", row["error"])
	}
	if _, ok := row["elapsed_ms"]; !ok {
		t.Fatalf("elapsed_ms missing: %v", row)
	}
	if row["level"] != "ERROR" {
		t.Fatalf("level=%v", row["level"])
	}
	assertNoSecrets(t, row)
}

// TestRequestTransportFailureIsLogged: a request that never got an answer (dead
// port) logs the transport cause with status 0 and no URL/query in the message —
// this is the "context deadline exceeded" case from the report.
func TestRequestTransportFailureIsLogged(t *testing.T) {
	rows := requestLogRows(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close() // nothing listens there any more

	c := New(addr, "secret-token")
	if _, err := c.ListJobs(job.ListOpts{Project: "proj-a"}); err == nil {
		t.Fatal("want a transport error from a closed port")
	}
	got := rows()
	if len(got) == 0 {
		t.Fatal("a transport failure must leave a line")
	}
	row := got[len(got)-1]
	if row["status"] != float64(0) {
		t.Fatalf("status=%v", row["status"])
	}
	msg, _ := row["error"].(string)
	if msg == "" {
		t.Fatalf("error summary missing: %v", row)
	}
	if strings.Contains(msg, "http://") || strings.Contains(msg, "?") {
		t.Fatalf("error summary must not carry a URL/query: %q", msg)
	}
	if row["path"] != "/v1/jobs" {
		t.Fatalf("path must be query-less, got %v", row["path"])
	}
	assertNoSecrets(t, row)
}

// TestRequestBusiness4xxNotLoggedBut401Is: a 404 is an answer the caller prints,
// not an incident; 401/403 is worth a WARN.
func TestRequestBusiness4xxNotLoggedBut401Is(t *testing.T) {
	rows := requestLogRows(t)
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	if _, err := c.GetJob("job-1"); err == nil {
		t.Fatal("want a 404 error")
	}
	if got := rows(); len(got) != 0 {
		t.Fatalf("a business 4xx must not be logged: %v", got)
	}

	status = http.StatusForbidden
	if _, err := c.GetJob("job-1"); err == nil {
		t.Fatal("want a 403 error")
	}
	got := rows()
	if len(got) != 1 {
		t.Fatalf("a 403 must be logged once, got %v", got)
	}
	if got[0]["level"] != "WARN" || got[0]["status"] != float64(403) {
		t.Fatalf("403 row = %v", got[0])
	}
}

// TestSuccessfulRequestLoggedOnlyAtDebug: successes are recorded only with
// GOFER_LOG_LEVEL=debug (the operator's opt-in trace), never at the default
// level — a healthy command must stay quiet.
func TestSuccessfulRequestLoggedOnlyAtDebug(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.log")
	SetRequestLogPath(path)
	t.Cleanup(func() { SetRequestLogPath("") })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"job-1"}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "").GetJob("job-1"); err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if raw, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		t.Fatalf("success logged at the default level: %q", raw)
	}

	t.Setenv("GOFER_LOG_LEVEL", "debug")
	// Re-open the sink: it reads GOFER_LOG_LEVEL when it is opened.
	SetRequestLogPath("")
	SetRequestLogPath(path)
	if _, err := New(srv.URL, "").GetJob("job-1"); err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read client log: %v", err)
	}
	if !strings.Contains(string(raw), "client.request_succeeded") || !strings.Contains(string(raw), `"status":200`) {
		t.Fatalf("debug success line missing: %q", raw)
	}
}

// TestRequestFailureCountsConsecutiveRetries: the `retry` field is how many
// failures of the SAME method+path already came before this one, so an operator
// can tell a one-off blip from a call that keeps failing.
func TestRequestFailureCountsConsecutiveRetries(t *testing.T) {
	rows := requestLogRows(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	for i := 0; i < 3; i++ {
		if _, err := c.GetJob("job-1"); err == nil {
			t.Fatal("want an error")
		}
	}
	got := rows()
	if len(got) != 3 {
		t.Fatalf("want three rows, got %d", len(got))
	}
	for i, row := range got {
		if row["retry"] != float64(i) {
			t.Fatalf("row %d retry=%v, want %d", i, row["retry"], i)
		}
	}
}

// TestRequestLogOpenFailureIsIgnored: a diagnostic sink that cannot be opened
// must not break the request it was describing.
func TestRequestLogOpenFailureIsIgnored(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetRequestLogPath(filepath.Join(blocked, "client.log"))
	t.Cleanup(func() { SetRequestLogPath("") })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "").GetJob("job-1")
	if StatusOf(err) != http.StatusInternalServerError {
		t.Fatalf("request outcome changed: %v", err)
	}
}

// assertNoSecrets fails when a logged row carries the bearer token.
func assertNoSecrets(t *testing.T, row map[string]any) {
	t.Helper()
	for k, v := range row {
		if s, ok := v.(string); ok && strings.Contains(s, "secret-token") {
			t.Fatalf("field %q leaks the token: %q", k, s)
		}
	}
}
