package worker

import (
	"testing"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

func TestMessengerFallsBackToExecForOldWorker(t *testing.T) {
	f := &runner.Forward{Messenger: &runner.MessengerDispatch{SessionName: "claude-main"}, Cmd: []string{"claude", "-p", "hello"}}
	if got := unsupportedDispatchFields(13, f); len(got) != 0 {
		t.Fatalf("v13 messenger fallback rejected dispatch: %v", got)
	}
}

func TestMessengerDispatchProjectsToWire(t *testing.T) {
	h := &fakeHub{workerProto: wsproto.CurrentProtocolVersion}
	r := newRunnerWithHub(h)
	runToResultWithWorker(t, r, h, &runner.Forward{
		ProjectKey: "p", Agent: "exec", Cwd: ".",
		Messenger: &runner.MessengerDispatch{SessionName: "claude-main", Command: []string{"claude", "-p", "hello"}, Cwd: ".", TimeoutSec: 30},
	})
	d := h.dispatchedFrame()
	if d.Messenger == nil || d.Messenger.SessionName != "claude-main" || d.Messenger.TimeoutSec != 30 {
		t.Fatalf("wire messenger = %+v", d.Messenger)
	}
}
