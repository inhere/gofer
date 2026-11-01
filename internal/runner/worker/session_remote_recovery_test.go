package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/wsproto"
)

func TestRemoteSessionFailsOnWorkerRestart(t *testing.T) {
	h := &fakeHub{workerProto: wsproto.CurrentProtocolVersion}
	r := newRunnerWithHub(h)
	want := "worker 重启，持续会话已结束；可用 gofer job resume remote-session-restart 以 session/load 接续上下文"
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID:   "remote-session-restart",
			Forward: &runner.Forward{Session: true},
		})
	}()
	deadline := time.Now().Add(wait.Timeout(t, 5*time.Second))
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	if h.getSink() == nil {
		t.Fatal("sink never registered")
	}
	h.getSink().OnDisconnect(errors.New("worker restarted (new instance)"))
	result := <-done
	if result.Err == nil || result.Err.Error() != want {
		t.Fatalf("session restart error = %v, want %q", result.Err, want)
	}
}

// TestRemoteSessionRestartErrorFollowsProjectLanguage: the error a worker restart leaves a
// persistent session is the catalog's job.errors.worker_restarted in the job's project
// language (zh-CN is verbatim the text above; English carries no CJK).
func TestRemoteSessionRestartErrorFollowsProjectLanguage(t *testing.T) {
	h := &fakeHub{workerProto: wsproto.CurrentProtocolVersion}
	r := newRunnerWithHub(h)
	WithLanguageFor(func(project string) i18n.Lang {
		if project == "p-en" {
			return i18n.EN
		}
		return i18n.ZhCN
	})(r)
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID:   "remote-session-lang",
			Forward: &runner.Forward{Session: true, ProjectKey: "p-en"},
		})
	}()
	deadline := time.Now().Add(wait.Timeout(t, 5*time.Second))
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	if h.getSink() == nil {
		t.Fatal("sink never registered")
	}
	h.getSink().OnDisconnect(errors.New("worker restarted (new instance)"))
	result := <-done
	want := "The worker restarted and the persistent session ended. Use gofer job resume remote-session-lang to continue its context through session/load"
	if result.Err == nil || result.Err.Error() != want {
		t.Fatalf("english restart error = %v, want %q", result.Err, want)
	}
}
