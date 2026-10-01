package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
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
	deadline := time.Now().Add(2 * time.Second)
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
