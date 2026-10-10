package commands

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestMemoryCandidateCommands (gofer-3nxa.2): `memory candidates` lists over HTTP with
// its filters, `memory accept` sends key / kind (default note) / scope, `memory reject`
// posts the decision, and a missing --key never reaches the server.
func TestMemoryCandidateCommands(t *testing.T) {
	type call struct {
		method, path, query string
		body                map[string]any
	}
	var calls []call
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		c := call{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		if r.Method == http.MethodPost && r.ContentLength > 0 {
			_ = json.NewDecoder(r.Body).Decode(&c.body)
		}
		calls = append(calls, c)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/memory-candidates":
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []jobstore.MemoryCandidate{{ID: 7, JobID: "j1", ProjectKey: "p", Text: "lesson", Status: "pending"}}})
		default:
			mem := jobstore.ScopedMemory{Scope: "project", ScopeKey: "p", Key: "k"}
			_ = json.NewEncoder(w).Encode(job.MemoryCandidateDecision{Candidate: jobstore.MemoryCandidate{ID: 7, Status: "accepted"}, Memory: &mem})
		}
	})
	reset := func() {
		memoryCandidateOpts = struct {
			project, job       string
			all, asJSON        bool
			key, kind, summary string
			global             bool
		}{}
	}
	reset()
	t.Cleanup(reset)

	if code := NewApp("test").Run([]string{"memory", "candidates", "--server", ts.URL, "-p", "p", "--all"}); code != 0 {
		t.Fatalf("candidates exit=%d", code)
	}
	if len(calls) != 1 || calls[0].method != http.MethodGet || calls[0].query != "project=p&status=all" {
		t.Fatalf("list call = %+v", calls)
	}
	reset()
	if code := NewApp("test").Run([]string{"memory", "accept", "--server", ts.URL, "#7", "--key", "k"}); code != 0 {
		t.Fatalf("accept exit=%d", code)
	}
	got := calls[len(calls)-1]
	if got.path != "/v1/memory-candidates/7/accept" || got.body["key"] != "k" || got.body["kind"] != "note" {
		t.Fatalf("accept call = %+v", got)
	}
	reset()
	if code := NewApp("test").Run([]string{"memory", "accept", "--server", ts.URL, "7", "--key", "k", "--kind", "rule", "--global"}); code != 0 {
		t.Fatalf("accept --global exit=%d", code)
	}
	if got := calls[len(calls)-1]; got.body["kind"] != "rule" || got.body["global"] != true {
		t.Fatalf("accept --global call = %+v", got)
	}
	reset()
	n := len(calls)
	if code := NewApp("test").Run([]string{"memory", "accept", "--server", ts.URL, "7"}); code == 0 || len(calls) != n {
		t.Fatalf("accept without --key: exit=%d calls=%d", code, len(calls)-n)
	}
	reset()
	if code := NewApp("test").Run([]string{"memory", "reject", "--server", ts.URL, "7"}); code != 0 {
		t.Fatalf("reject exit=%d", code)
	}
	if got := calls[len(calls)-1]; got.path != "/v1/memory-candidates/7/reject" || got.method != http.MethodPost {
		t.Fatalf("reject call = %+v", got)
	}
}
