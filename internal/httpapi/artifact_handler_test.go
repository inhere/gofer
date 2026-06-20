package httpapi

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// makeArtifacts writes artifacts/a.txt + artifacts/sub/b.bin under a job's
// result dir and returns the result dir. The job must already exist (its
// ResultDir is read from Get).
func makeArtifacts(t *testing.T, s *Server, id string) string {
	t.Helper()
	res, ok := s.jobs.Get(id)
	if !ok {
		t.Fatalf("job %s not found", id)
	}
	artDir := filepath.Join(res.ResultDir, "artifacts")
	if err := os.MkdirAll(filepath.Join(artDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "sub", "b.bin"), []byte("abcdefgh"), 0o600); err != nil {
		t.Fatalf("write b.bin: %v", err)
	}
	return res.ResultDir
}

// runDoneJob submits a trivial exec job and waits for it to finish, returning
// its id. Used to get a real result dir to drop artifacts into.
func runDoneJob(t *testing.T, s *Server) string {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("create returned no id")
	}
	waitDone(t, s, created.ID)
	return created.ID
}

// TestListArtifacts asserts the manifest endpoint lists both files (via live
// scan fallback when no manifest was captured) with slash-relative names.
func TestListArtifacts(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)
	makeArtifacts(t, s, id)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/artifacts", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Artifacts []job.ArtifactItem `json:"artifacts"`
	}
	decode(t, resp, &body)
	if len(body.Artifacts) != 2 {
		t.Fatalf("expected 2 artifacts, got %d: %+v", len(body.Artifacts), body.Artifacts)
	}
	names := map[string]int64{}
	for _, a := range body.Artifacts {
		names[a.Name] = a.Size
	}
	if names["a.txt"] != 5 || names["sub/b.bin"] != 8 {
		t.Fatalf("unexpected artifact names/sizes: %+v", body.Artifacts)
	}
}

// TestListArtifactsEmptyIsArray asserts a job with no artifacts dir returns a
// non-nil empty array, not null.
func TestListArtifactsEmptyIsArray(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/artifacts", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d, want 200", resp.StatusCode)
	}
	// Body must be {"artifacts":[]} (non-null).
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `"artifacts":[]`) {
		t.Fatalf("expected empty non-null artifacts array, got %s", raw)
	}
}

func TestListArtifactsUnknownJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/jobs/ghost/artifacts", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}
