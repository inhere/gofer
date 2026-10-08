package job

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// gofer-5foz: Submit must return without waiting for the pre-agent git work (GIT-01
// baseline, base SHA). Both run on the job goroutine; a slow scan on a huge workspace
// used to be able to hold entry.mu and stall the HTTP response.
func TestSubmitDoesNotWaitForGitScan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := dirlockService(t, root, []string{"exit", "0"}, nil)
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	s.gitBase = &gitBaseline{
		uncommitted: func(string) uncommittedSnapshot {
			entered <- struct{}{}
			<-release
			return uncommittedSnapshot{}
		},
		baseSHA: func(string, string) string {
			entered <- struct{}{}
			<-release
			return "deadbeef"
		},
	}
	t.Cleanup(func() { close(release) })

	for _, req := range []JobRequest{
		{ProjectKey: "self", Agent: "agent", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30},
		{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{testcmd.Path(t), "exit", "0"}, Cwd: ".", TimeoutSec: 30},
	} {
		done := make(chan struct{})
		var res JobResult
		var err error
		go func() { res, err = s.Submit(req); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Submit(%s) blocked on the git scan", req.Agent)
		}
		if err != nil {
			t.Fatalf("Submit(%s): %v", req.Agent, err)
		}
		if res.ID == "" {
			t.Fatalf("Submit(%s): empty job id", req.Agent)
		}
		select { // the job goroutine did reach (and is parked in) the slow git step
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("job %s never reached the git baseline", req.Agent)
		}
		// Status readers must not be blocked either.
		got := make(chan struct{})
		go func() { _, _ = s.Get(res.ID); close(got) }()
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatalf("Get(%s) blocked while git baseline was running", req.Agent)
		}
	}
}

func TestPhaseTimerReport(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	tm := newPhaseTimer()
	tm.mark("a")
	tm.mark("b")
	tm.report("job.submit_slow", time.Hour, "job_id", "x")
	if buf.Len() != 0 {
		t.Fatalf("fast path must not log, got %q", buf.String())
	}
	tm.report("job.submit_slow", 0, "job_id", "x")
	out := buf.String()
	for _, want := range []string{"job.submit_slow", "job_id=x", "total_ms=", "a_ms=", "b_ms="} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q: %s", want, out)
		}
	}
}

// nestedGitRoots must skip ignored directories (and repos beneath them) with one
// batched check-ignore, same result as the old per-directory probing.
func TestNestedGitRootsHonoursIgnoreBatched(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	uncommittedGit(t, root, "init")
	uncommittedWrite(t, root, ".gitignore", "ignored-repo/\nbucket/\n")
	for _, d := range []string{"repo-a", "ignored-repo", "grp/repo-b", "bucket/repo-c"} {
		uncommittedWrite(t, root, filepath.Join(d, "f.txt"), "x")
		uncommittedGit(t, filepath.Join(root, d), "init")
	}
	got := nestedGitRoots(context.Background(), root)
	var rel []string
	for _, g := range got {
		r, _ := filepath.Rel(root, g)
		rel = append(rel, filepath.ToSlash(r))
	}
	if want := []string{"grp/repo-b", "repo-a"}; !reflect.DeepEqual(rel, want) {
		t.Fatalf("nested roots = %v, want %v", rel, want)
	}
}
