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

// TestDownloadArtifactTopLevel downloads artifacts/a.txt and asserts the body,
// the 200 status and the Content-Disposition header.
func TestDownloadArtifactTopLevel(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)
	makeArtifacts(t, s, id)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/artifacts/a.txt", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download status=%d, want 200", resp.StatusCode)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "a.txt") {
		t.Fatalf("unexpected Content-Disposition: %q", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("download body=%q, want %q", body, "hello")
	}
}

// TestDownloadArtifactSubpath downloads artifacts/sub/b.bin, proving the
// {name:.+} catch-all route carries a slash-containing relative name.
func TestDownloadArtifactSubpath(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)
	makeArtifacts(t, s, id)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/artifacts/sub/b.bin", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subpath download status=%d, want 200 (rux {name:.+} must match subpaths)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "abcdefgh" {
		t.Fatalf("subpath download body=%q, want %q", body, "abcdefgh")
	}
}

func TestDownloadArtifactNotFound(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)
	makeArtifacts(t, s, id)

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"/artifacts/missing.txt", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for missing artifact", resp.StatusCode)
	}
}

// TestDownloadArtifactTraversalRejected exercises the path-safety gate over the
// HTTP boundary: a "../" name (URL-encoded so it survives to the handler) must
// not escape the artifacts dir. Depending on how rux/net-http normalises the
// path it is either rejected as 400 (escape) or 404 (resolved to a non-file);
// the load-bearing assertion is that the secret file is NEVER served.
func TestDownloadArtifactTraversalRejected(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := runDoneJob(t, s)
	res, _ := s.jobs.Get(id)
	// Plant a secret a sibling level above artifacts/ (inside the result dir).
	secret := filepath.Join(res.ResultDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	makeArtifacts(t, s, id)

	// %2e%2e%2f == "../"; target ../secret.txt relative to artifacts/.
	resp := do(t, s, http.MethodGet,
		"/v1/jobs/"+id+"/artifacts/%2e%2e%2fsecret.txt", testToken, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "TOPSECRET") {
		t.Fatalf("traversal leaked secret file (status=%d): %s", resp.StatusCode, body)
	}
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("traversal returned 200, must be rejected (4xx); body=%s", body)
	}
}
