package worker

import (
	"testing"

	"github.com/inhere/gofer/internal/runner"
)

func TestMessengerFallsBackToExecForOldWorker(t *testing.T) {
	f := &runner.Forward{Messenger: &runner.MessengerDispatch{SessionName: "claude-main"}, Cmd: []string{"claude", "-p", "hello"}}
	if got := unsupportedDispatchFields(13, f); len(got) != 0 {
		t.Fatalf("v13 messenger fallback rejected dispatch: %v", got)
	}
}
