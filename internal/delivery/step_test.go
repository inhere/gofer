package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// writeFile writes content (or a fresh line when empty) to path.
func writeFile(path string, content ...string) error {
	text := "touched\n"
	if len(content) > 0 {
		text = content[0]
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// stepRepos builds a bare remote plus a work clone holding one commit, a branch and a
// tag. Returns the works clone's dir and the bare remote's path.
func stepRepos(t *testing.T) (work, bare string) {
	t.Helper()
	dir := t.TempDir()
	bare = dir + "/remote.git"
	git(t, dir, "init", "--bare", "-q", bare)
	work = dir + "/work"
	git(t, dir, "init", "-q", "-b", "main", work)
	if err := writeFile(work+"/a.txt", "a\n"); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-q", "-m", "one", "--no-gpg-sign")
	git(t, work, "tag", "v1.0.0")
	git(t, work, "remote", "add", "origin", bare)
	return work, bare
}

func TestPushRefsPushesBranchAndTagAndVerifies(t *testing.T) {
	work, bare := stepRepos(t)
	branchSHA := git(t, work, "rev-parse", "refs/heads/main")
	tagSHA := git(t, work, "rev-parse", "refs/tags/v1.0.0")

	res, err := PushRefs(context.Background(), work, "origin", []string{"main", "v1.0.0"}, "")
	if err != nil {
		t.Fatalf("PushRefs: %v", err)
	}
	if len(res) != 2 || res[0].Kind != "branch" || res[1].Kind != "tag" {
		t.Fatalf("results = %+v, want a branch then a tag", res)
	}
	if got := git(t, bare, "rev-parse", "refs/heads/main"); got != branchSHA {
		t.Errorf("remote branch = %s, want %s", got, branchSHA)
	}
	if got := git(t, bare, "rev-parse", "refs/tags/v1.0.0"); got != tagSHA {
		t.Errorf("remote tag = %s, want %s", got, tagSHA)
	}
	if res[0].SHA != branchSHA || res[1].SHA != tagSHA {
		t.Errorf("reported shas = %+v", res)
	}
}

// A remote ref that moved elsewhere is never overwritten: the push is rejected and the
// remote keeps its own commit.
func TestPushRefsNeverForces(t *testing.T) {
	work, bare := stepRepos(t)
	other := t.TempDir()
	git(t, other, "init", "-q", "-b", "main", other+"/x")
	if err := writeFile(other + "/x/b.txt"); err != nil {
		t.Fatal(err)
	}
	git(t, other+"/x", "add", ".")
	git(t, other+"/x", "commit", "-q", "-m", "other", "--no-gpg-sign")
	moved := git(t, other+"/x", "rev-parse", "HEAD")
	git(t, other+"/x", "push", "-q", bare, "main:refs/heads/main")

	// Rewrite main in the work clone so the push is a non-fast-forward.
	if err := writeFile(work + "/a.txt"); err != nil { // touch to make a new commit
		t.Fatal(err)
	}
	git(t, work, "commit", "-q", "--amend", "-m", "rewritten", "--no-gpg-sign")
	git(t, bare, "update-ref", "refs/heads/main", moved)

	if _, err := PushRefs(context.Background(), work, "origin", []string{"main"}, ""); err == nil {
		t.Fatal("PushRefs over a moved remote branch should fail")
	}
	if got := git(t, bare, "rev-parse", "refs/heads/main"); got != moved {
		t.Errorf("remote branch = %s, want the moved commit %s (no force)", got, moved)
	}
}

func TestPushRefsRejectsUnknownRef(t *testing.T) {
	work, _ := stepRepos(t)
	if _, err := PushRefs(context.Background(), work, "origin", []string{"nope"}, ""); err == nil {
		t.Fatal("an unknown branch / tag must be refused")
	}
	if _, err := PushRefs(context.Background(), work, "origin", nil, ""); err == nil {
		t.Fatal("an empty ref list must be refused")
	}
}

// fakeGitHub serves the check-runs / alerts / release endpoints a built-in step polls.
func fakeGitHub(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Errorf("%s: request without a token", r.URL.Path)
		}
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ciDir is a clone whose origin parses as a GitHub repository.
func ciDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", dir)
	git(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	return dir
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func fastPoll(t *testing.T) {
	t.Helper()
	old := StepPollFloor
	StepPollFloor = time.Millisecond
	t.Cleanup(func() { StepPollFloor = old })
}

func TestWaitCISuccess(t *testing.T) {
	fastPoll(t)
	dir := ciDir(t)
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/o/r/commits/v1/check-runs") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{"check_runs": []map[string]string{
			{"name": "Unit-Tests", "status": "completed", "conclusion": "success"},
			{"name": "Lint", "status": "completed", "conclusion": "skipped"},
		}})
	})
	res, err := WaitCI(context.Background(), dir, WaitCIOptions{
		Ref: "v1", Required: []string{"Unit-Tests"}, TimeoutMin: 1,
		Env: Env{Lookup: func(string) (string, bool) { return "tok", true }, HTTPBase: srv.URL},
	})
	if err != nil {
		t.Fatalf("WaitCI: %v", err)
	}
	if res.Provider != ProviderGitHub || len(res.Checks) != 2 {
		t.Errorf("result = %+v", res)
	}
}

func TestWaitCIRequiredFailure(t *testing.T) {
	fastPoll(t)
	dir := ciDir(t)
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"check_runs": []map[string]string{
			{"name": "Unit-Tests", "status": "completed", "conclusion": "failure"},
		}})
	})
	_, err := WaitCI(context.Background(), dir, WaitCIOptions{
		Ref: "v1", Required: []string{"Unit-Tests"}, TimeoutMin: 1,
		Env: Env{Lookup: func(string) (string, bool) { return "tok", true }, HTTPBase: srv.URL},
	})
	if err == nil || !strings.Contains(err.Error(), "Unit-Tests") || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("err = %v, want the failed required check named", err)
	}
}

func TestWaitCITimesOutWhileRunning(t *testing.T) {
	fastPoll(t)
	dir := ciDir(t)
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"check_runs": []map[string]string{
			{"name": "Unit-Tests", "status": "in_progress"},
		}})
	})
	// A context deadline stands in for the (long) timeout: the step must report the checks
	// still running instead of hanging or reporting success.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := WaitCI(ctx, dir, WaitCIOptions{
		Ref: "v1", TimeoutMin: 1,
		Env: Env{Lookup: func(string) (string, bool) { return "tok", true }, HTTPBase: srv.URL},
	})
	if err == nil || !strings.Contains(err.Error(), "in_progress") {
		t.Fatalf("err = %v, want the running check named", err)
	}
}

func TestWaitCICodeScanningAlerts(t *testing.T) {
	fastPoll(t)
	dir := ciDir(t)
	alerts := 1
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/check-runs"):
			writeJSON(t, w, map[string]any{"check_runs": []map[string]string{
				{"name": "Unit-Tests", "status": "completed", "conclusion": "success"},
			}})
		case strings.Contains(r.URL.Path, "/code-scanning/alerts"):
			if got := r.URL.Query().Get("ref"); got != "v1" {
				t.Errorf("alerts ref = %q, want v1", got)
			}
			out := []map[string]any{}
			for i := 0; i < alerts; i++ {
				out = append(out, map[string]any{"rule": map[string]string{"id": "go/overflow"}, "html_url": "https://x/1"})
			}
			writeJSON(t, w, out)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	env := Env{Lookup: func(string) (string, bool) { return "tok", true }, HTTPBase: srv.URL}
	_, err := WaitCI(context.Background(), dir, WaitCIOptions{Ref: "v1", CodeScanning: true, TimeoutMin: 1, Env: env})
	if err == nil || !strings.Contains(err.Error(), "go/overflow") {
		t.Fatalf("err = %v, want the new alert reported", err)
	}
	alerts = 0
	res, err := WaitCI(context.Background(), dir, WaitCIOptions{Ref: "v1", CodeScanning: true, TimeoutMin: 1, Env: env})
	if err != nil || res.Alerts != 0 {
		t.Fatalf("no alerts: %+v %v", res, err)
	}
}

func TestWaitAssets(t *testing.T) {
	fastPoll(t)
	dir := ciDir(t)
	assets := []map[string]string{{"name": "app-linux-amd64.tar.gz"}}
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases/tags/v1" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{"html_url": "https://github.com/o/r/releases/tag/v1", "assets": assets})
	})
	env := Env{Lookup: func(string) (string, bool) { return "tok", true }, HTTPBase: srv.URL}
	res, err := WaitAssets(context.Background(), dir, WaitAssetsOptions{
		Tag: "v1", Assets: []string{"app-*-amd64.tar.gz"}, TimeoutMin: 1, Env: env,
	})
	if err != nil || len(res.Assets) != 1 || res.URL == "" {
		t.Fatalf("WaitAssets = %+v %v", res, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = WaitAssets(ctx, dir, WaitAssetsOptions{
		Tag: "v1", Assets: []string{"*-windows.zip"}, TimeoutMin: 1, Env: env,
	})
	if err == nil || !strings.Contains(err.Error(), "*-windows.zip") {
		t.Fatalf("err = %v, want the missing asset named", err)
	}
}

// A release that does not exist yet is polled, not failed: 404 keeps waiting until the
// timeout reports what was missing.
func TestWaitAssetsPollsMissingRelease(t *testing.T) {
	fastPoll(t)
	dir := ciDir(t)
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := WaitAssets(ctx, dir, WaitAssetsOptions{
		Tag: "v1", Assets: []string{"a.zip"}, TimeoutMin: 1,
		Env: Env{Lookup: func(string) (string, bool) { return "tok", true }, HTTPBase: srv.URL},
	})
	if err == nil || !strings.Contains(err.Error(), "a.zip") {
		t.Fatalf("err = %v, want the missing asset reported on timeout", err)
	}
}

// gitlab / gitea have no implementation yet: the step says so instead of half-working.
func TestWaitCIUnsupportedProvider(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", dir)
	git(t, dir, "remote", "add", "origin", "https://gitlab.com/o/r.git")
	if _, err := WaitCI(context.Background(), dir, WaitCIOptions{Ref: "v1"}); err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("err = %v, want a not-supported error", err)
	}
}
