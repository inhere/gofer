package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestReviewJobCallerForbidden(t *testing.T) {
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	repo := t.TempDir()
	runHTTPGit(t, repo, "init")
	runHTTPGit(t, repo, "config", "user.email", "gofer-test@example.invalid")
	runHTTPGit(t, repo, "config", "user.name", "gofer test")
	if err := os.WriteFile(filepath.Join(repo, "review.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHTTPGit(t, repo, "add", "review.txt")
	runHTTPGit(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(runHTTPGit(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "review.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{
		ID: "review-readable", SessionID: "review-secure", Status: job.StatusDone,
		StartedAt: now - 10, UpdatedAt: now - 5, Runner: "local", Cwd: repo, BaseSHA: base,
	}, "review", "review")
	jobToken := seedJobToken(t, s, "review-caller", jobstore.JobCredentialMember, "")

	getResp := do(t, s, http.MethodGet, "/v1/workbench/threads/s:review-secure/diff", jobToken, nil)
	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(getResp.Body)
		getResp.Body.Close()
		t.Fatalf("job caller diff status=%d, want 200: %s", getResp.StatusCode, body)
	}
	var diff struct {
		Source string `json:"source"`
		Patch  string `json:"patch"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&diff); err != nil {
		t.Fatalf("decode diff: %v", err)
	}
	getResp.Body.Close()
	if diff.Source != "live" || !strings.Contains(diff.Patch, "after") {
		t.Fatalf("diff=%+v", diff)
	}

	postResp := do(t, s, http.MethodPost, "/v1/workbench/threads/s:review-secure/review", jobToken, map[string]any{
		"summary": "forbidden",
	})
	if postResp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(postResp.Body)
		postResp.Body.Close()
		t.Fatalf("job caller review status=%d, want 403: %s", postResp.StatusCode, body)
	}
	assertJobCredentialRefusal(t, postResp)
}

func runHTTPGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
