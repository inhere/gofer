package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gcli "github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
)

const scopeSID = "11111111-2222-3333-4444-555555555555"

// scopeServer stubs the session probe (the session lives in another project than the
// job) and POST /v1/jobs. rejectNotify makes a submit that still carries a notify
// target fail with the server's notify-target rejection (status 400 + stable code);
// otherErr makes every submit fail with a plain 400 instead. posts records each
// submitted request.
type scopeServer struct {
	mu           sync.Mutex
	posts        []job.JobRequest
	rejectNotify bool
	otherErr     bool
}

func (s *scopeServer) client(t *testing.T) *client.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/"+scopeSID:
			_ = json.NewEncoder(w).Encode(client.SessionDetail{Session: client.AgentSession{
				SessionID: scopeSID, ProjectKey: "session-project", Runner: "session-runner", State: "waiting_reply"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			var req job.JobRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.posts = append(s.posts, req)
			s.mu.Unlock()
			switch {
			case s.otherErr:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"job rejected","detail":"invalid request: notify session \"x\" belongs to another caller"}`))
			case s.rejectNotify && req.NotifySession != "":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"job rejected","detail":"invalid request: notify session belongs to another caller","code":"` + job.ErrCodeNotifyTargetRejected + `"}`))
			default:
				_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-1", Status: "queued", Notify: req.Notify, NotifySession: req.NotifySession})
			}
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return client.New(ts.URL, "tok")
}

// submitScope parses `job run <args>` and runs the JSON submit path against cli,
// returning its result, error and what the CLI wrote to stderr.
func submitScope(t *testing.T, cli *client.Client, args ...string) (client.SubmitResult, error, string) {
	t.Helper()
	jobRunOpts = jobRunFlags{}
	oldStderr := jobRunStderr
	var buf bytes.Buffer
	jobRunStderr = &buf
	t.Cleanup(func() { jobRunOpts = jobRunFlags{}; jobRunStderr = oldStderr })

	var (
		res client.SubmitResult
		err error
	)
	app := NewApp("test")
	app.GetCommand("job").GetCommand("run").Func = func(c *gcli.Command, _ []string) error {
		res, err = submitJSONJob(c, cli)
		return nil
	}
	in := append(append([]string{"job", "run", "-p", "job-project", "-a", "exec"}, args...), "--", "go", "version")
	if code := app.Run(in); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, in)
	}
	return res, err, buf.String()
}

func TestExplicitNotifyResolvesCrossProjectSession(t *testing.T) {
	isolateAgentSessionEnv(t)
	t.Setenv("GOFER_SESSION_ID", scopeSID)
	srv := &scopeServer{}
	cli := srv.client(t)

	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })
	_, _, _, _, _, _, _ = parseRun(t, []string{"job", "run", "-p", "job-project", "-a", "exec", "--notify", "--", "go", "version"})
	notify, sid, err := resolveJobRunNotify(cli)
	if err != nil || !notify || sid != scopeSID {
		t.Fatalf("notify=%v sid=%q err=%v, want the session of another project (%s)", notify, sid, err, scopeSID)
	}
}

func TestImplicitHoldNotifyDegradesOnTargetRejection(t *testing.T) {
	isolateAgentSessionEnv(t)
	t.Setenv("GOFER_SESSION_ID", scopeSID)
	srv := &scopeServer{rejectNotify: true}
	res, err, stderr := submitScope(t, srv.client(t), "--hold")
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if res.Job.ID != "job-1" {
		t.Fatalf("job = %+v, want the retried submit's result", res.Job)
	}
	if len(srv.posts) != 2 {
		t.Fatalf("posts = %d, want 2 (first with the notify, then without)", len(srv.posts))
	}
	if srv.posts[0].NotifySession != scopeSID || !srv.posts[0].Notify {
		t.Fatalf("first post = %+v, want the implicit notify", srv.posts[0])
	}
	if srv.posts[1].Notify || srv.posts[1].NotifySession != "" {
		t.Fatalf("second post = %+v, want no notify", srv.posts[1])
	}
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "--notify-session") {
		t.Fatalf("stderr = %q, want a one-line warning naming --notify-session", stderr)
	}
	if n := strings.Count(strings.TrimSpace(stderr), "\n"); n != 0 {
		t.Fatalf("stderr = %q, want exactly one line", stderr)
	}
}

func TestExplicitNotifyTargetRejectionStillFails(t *testing.T) {
	isolateAgentSessionEnv(t)
	t.Setenv("GOFER_SESSION_ID", scopeSID)
	for _, args := range [][]string{
		{"--hold", "--notify-session", "other"},
		{"--hold", "--notify"},
		{"--notify-session", "other"},
	} {
		srv := &scopeServer{rejectNotify: true}
		_, err, stderr := submitScope(t, srv.client(t), args...)
		if err == nil || !client.IsNotifyTargetRejected(err) {
			t.Fatalf("%v: err = %v, want the notify-target rejection", args, err)
		}
		if len(srv.posts) != 1 || strings.Contains(stderr, "warning") {
			t.Fatalf("%v: posts=%d stderr=%q, want a single submit and no warning", args, len(srv.posts), stderr)
		}
	}
}

func TestOtherSubmitErrorsDoNotRetry(t *testing.T) {
	isolateAgentSessionEnv(t)
	t.Setenv("GOFER_SESSION_ID", scopeSID)
	srv := &scopeServer{otherErr: true}
	_, err, stderr := submitScope(t, srv.client(t), "--hold")
	if err == nil {
		t.Fatal("a plain 400 must fail the submit")
	}
	if client.IsNotifyTargetRejected(err) {
		t.Fatalf("err = %v, must not look like a notify-target rejection (message text alone is not the identity)", err)
	}
	if len(srv.posts) != 1 || strings.Contains(stderr, "warning") {
		t.Fatalf("posts=%d stderr=%q, want a single submit and no warning", len(srv.posts), stderr)
	}
}
