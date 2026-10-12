package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseRemoteAndDetect(t *testing.T) {
	for raw, want := range map[string]Remote{
		"https://github.com/o/r.git":              {Host: "github.com", Path: "o/r"},
		"git@github.com:o/r.git":                  {Host: "github.com", Path: "o/r"},
		"ssh://git@gitlab.example.com:2222/g/s/r": {Host: "gitlab.example.com", Path: "g/s/r"},
		"https://gitea.example.com:3000/o/r":      {Host: "gitea.example.com:3000", Path: "o/r"},
		"http://172.20.1.5/team/project.git":      {Host: "172.20.1.5", Path: "team/project"},
	} {
		got, err := ParseRemote(raw)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", raw, got, err, want)
		}
	}
	if _, err := ParseRemote("/srv/git/r.git"); err == nil {
		t.Error("a local path is not a platform remote")
	}
	for host, want := range map[string]string{"github.com": "github", "gitlab.com": "gitlab", "git.gitlab.corp": "gitlab",
		"codeberg.org": "gitea", "forgejo.home": "gitea", "172.20.1.5": ""} {
		if got := DetectProvider(host, ""); got != want {
			t.Errorf("%s: %q want %q", host, got, want)
		}
	}
	if DetectProvider("172.20.1.5", "forgejo") != ProviderGitea || DetectProvider("x", "gitlab") != ProviderGitLab {
		t.Error("explicit provider")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repoWithWorktree builds: a bare remote, a main checkout on `main` pushed to it, and
// a linked worktree on gofer/j1 with one commit ahead. The remote URL is written as a
// GitHub address and rewritten to the bare repo with insteadOf.
func repoWithWorktree(t *testing.T) (main, wt, bare string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "remote.git")
	git(t, root, "init", "--bare", "-b", "main", bare)
	main = filepath.Join(root, "main")
	git(t, root, "init", "-b", "main", main)
	// Execute's own git calls (the merge commit) run without the test's identity
	// env, and a CI runner has no global user — give the repository one.
	git(t, main, "config", "user.name", "t")
	git(t, main, "config", "user.email", "t@t")
	git(t, main, "config", "url."+bare+".insteadOf", "https://github.com/o/r.git")
	git(t, main, "remote", "add", "origin", "https://github.com/o/r.git")
	if err := os.WriteFile(filepath.Join(main, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", ".")
	git(t, main, "commit", "-m", "init")
	git(t, main, "push", "origin", "main")
	wt = filepath.Join(root, "wt")
	git(t, main, "worktree", "add", "-b", "gofer/j1", wt)
	if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "job work")
	return main, wt, bare
}

func TestExecuteMergeAndCleanup(t *testing.T) {
	main, wt, _ := repoWithWorktree(t)
	res := Execute(context.Background(), Plan{Mode: ModeMerge, Worktree: wt, Branch: "gofer/j1", Cleanup: true}, Env{})
	if res.State != StateMerged || res.Base != "main" {
		t.Fatalf("merge: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(main, "b.txt")); err != nil {
		t.Fatal("the job's file did not reach main")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("cleanup kept the worktree")
	}
	if out := git(t, main, "branch", "--list", "gofer/j1"); out != "" {
		t.Fatalf("merged branch kept: %q", out)
	}
}

// A merge that fails for a reason other than a conflict (here: no committer identity)
// reports git's own error, not an empty "has conflicts", and leaves main as it was.
func TestExecuteMergeReportsNonConflictFailure(t *testing.T) {
	main, wt, _ := repoWithWorktree(t)
	git(t, main, "config", "--unset", "user.email")
	git(t, main, "config", "--unset", "user.name")
	git(t, main, "config", "user.useConfigOnly", "true")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	head := git(t, main, "rev-parse", "HEAD")
	res := Execute(context.Background(), Plan{Mode: ModeMerge, Worktree: wt, Branch: "gofer/j1"}, Env{})
	if res.State != StateFailed || strings.Contains(res.Error, "conflicts") || !strings.Contains(res.Error, "merge gofer/j1") {
		t.Fatalf("non-conflict failure: %+v", res)
	}
	if got := git(t, main, "rev-parse", "HEAD"); got != head {
		t.Fatalf("main moved: %s -> %s", head, got)
	}
}

func TestExecuteMergeRefusesDirtyMain(t *testing.T) {
	main, wt, _ := repoWithWorktree(t)
	if err := os.WriteFile(filepath.Join(main, "a.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := Execute(context.Background(), Plan{Mode: ModeMerge, Worktree: wt, Branch: "gofer/j1"}, Env{})
	if res.State != StateFailed || !strings.Contains(res.Error, "uncommitted") {
		t.Fatalf("dirty main: %+v", res)
	}
}

func TestExecutePushNeedsPermission(t *testing.T) {
	_, wt, _ := repoWithWorktree(t)
	res := Execute(context.Background(), Plan{Mode: ModePush, Worktree: wt, Branch: "gofer/j1", RemoteBranch: "gofer/j1"}, Env{})
	if res.State != StateFailed || !strings.Contains(res.Error, "allow_push") {
		t.Fatalf("push without permission: %+v", res)
	}
}

// pr: push the branch, then open the request through the (fake) GitHub API; a retry
// finds the open request instead of opening a second.
func TestExecutePROnGitHub(t *testing.T) {
	_, wt, bare := repoWithWorktree(t)
	var mu sync.Mutex
	var created []map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls":
			if len(created) == 0 {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"html_url":"https://github.com/o/r/pull/7","number":7}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = append(created, body)
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/o/r/pull/7","number":7}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/7/labels":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer srv.Close()
	env := Env{Lookup: func(k string) (string, bool) { return "tok123", k == "GITHUB_TOKEN" }, HTTPBase: srv.URL}
	plan := Plan{Mode: ModePR, Worktree: wt, Branch: "gofer/j1", RemoteBranch: "agent/j1", AllowPush: true,
		Title: "Add b", Body: "body", Draft: true, Labels: []string{"agent"}}
	res := Execute(context.Background(), plan, env)
	if res.State != StatePROpened || res.PRURL != "https://github.com/o/r/pull/7" || res.Provider != ProviderGitHub || res.Base != "main" {
		t.Fatalf("pr: %+v", res)
	}
	if got := git(t, bare, "rev-parse", "--verify", "refs/heads/agent/j1"); got == "" {
		t.Fatal("branch not pushed")
	}
	if auth != "Bearer tok123" || len(created) != 1 || created[0]["base"] != "main" || created[0]["head"] != "agent/j1" || created[0]["draft"] != true {
		t.Fatalf("request: auth=%q created=%+v", auth, created)
	}
	again := Execute(context.Background(), plan, env)
	if again.State != StatePROpened || len(created) != 1 || !strings.Contains(again.Note, "already existed") {
		t.Fatalf("retry opened a second request: %+v created=%d", again, len(created))
	}
}

// No token: the branch is pushed and the result says how to open the request.
func TestExecutePRWithoutToken(t *testing.T) {
	_, wt, _ := repoWithWorktree(t)
	env := Env{Lookup: func(string) (string, bool) { return "", false },
		RunTokenCLI: func(context.Context, string, ...string) (string, error) { return "", errors.New("no gh") }}
	res := Execute(context.Background(), Plan{Mode: ModePR, Worktree: wt, Branch: "gofer/j1", RemoteBranch: "gofer/j1", AllowPush: true, Title: "t"}, env)
	if res.State != StatePushed || !strings.Contains(res.Note, "GITHUB_TOKEN") || !strings.Contains(res.CompareURL, "github.com/o/r/compare/main...gofer/j1") {
		t.Fatalf("no token: %+v", res)
	}
}

func TestGitLabAndGiteaRequests(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+" "+r.Header.Get("PRIVATE-TOKEN")+r.Header.Get("Authorization")+" "+toString(body["title"]))
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "merge_requests"):
			_, _ = w.Write([]byte(`{"web_url":"https://gitlab.example.com/g/r/-/merge_requests/3","iid":3}`))
		default:
			_, _ = w.Write([]byte(`{"html_url":"https://gitea.example.com/o/r/pulls/5","number":5}`))
		}
	}))
	defer srv.Close()
	gl := &Client{Provider: ProviderGitLab, Remote: Remote{Host: "gitlab.example.com", Path: "g/sub/r"}, Token: "glt", APIBase: srv.URL}
	pr, err := gl.Open(context.Background(), PRRequest{Title: "T", Head: "h", Base: "main", Draft: true})
	if err != nil || pr.Number != 3 {
		t.Fatalf("gitlab: %+v %v", pr, err)
	}
	gt := &Client{Provider: ProviderGitea, Remote: Remote{Host: "gitea.example.com", Path: "o/r"}, Token: "gtt", APIBase: srv.URL}
	pr, err = gt.Open(context.Background(), PRRequest{Title: "T", Head: "h", Base: "main", Draft: true})
	if err != nil || pr.Number != 5 {
		t.Fatalf("gitea: %+v %v", pr, err)
	}
	all := strings.Join(seen, "\n")
	for _, want := range []string{"POST /projects/g%2Fsub%2Fr/merge_requests glt Draft: T", "POST /repos/o/r/pulls token gtt WIP: T"} {
		if !strings.Contains(all, want) {
			t.Errorf("requests lack %q:\n%s", want, all)
		}
	}
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

// The API calls go through the delivery proxy: the plan's, else git's http.proxy.
func TestExecutePRThroughProxy(t *testing.T) {
	for _, viaGit := range []bool{false, true} {
		main, wt, _ := repoWithWorktree(t)
		var mu sync.Mutex
		var hosts []string
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hosts = append(hosts, r.URL.Host)
			mu.Unlock()
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/o/r/pull/9","number":9}`))
		}))
		plan := Plan{Mode: ModePR, Worktree: wt, Branch: "gofer/j1", RemoteBranch: "gofer/j1", AllowPush: true, Title: "t"}
		if viaGit {
			git(t, main, "config", "http.proxy", proxy.URL)
		} else {
			plan.Proxy = proxy.URL
		}
		env := Env{Lookup: func(k string) (string, bool) { return "tok", k == "GITHUB_TOKEN" }, HTTPBase: "http://api.invalid"}
		res := Execute(context.Background(), plan, env)
		proxy.Close()
		if res.State != StatePROpened || res.PRNumber != 9 {
			t.Fatalf("viaGit=%v: %+v", viaGit, res)
		}
		if len(hosts) == 0 || hosts[0] != "api.invalid" {
			t.Fatalf("viaGit=%v: the API did not go through the proxy: %v", viaGit, hosts)
		}
	}
}
