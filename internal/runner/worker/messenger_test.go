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

func TestMessengerListAgentsRefusedForOlderWorker(t *testing.T) {
	f := &runner.Forward{Messenger: &runner.MessengerDispatch{Op: "list_agents"}}
	if got := unsupportedDispatchFields(wsproto.MessengerListMinProtocolVersion-1, f); len(got) != 1 || got[0] != "messenger_list_agents" {
		t.Fatalf("v15 worker must be refused list_agents, got %v", got)
	}
	if got := unsupportedDispatchFields(wsproto.MessengerListMinProtocolVersion, f); len(got) != 0 {
		t.Fatalf("v16 worker refused list_agents: %v", got)
	}
}

func TestMessengerOpProjectsToWire(t *testing.T) {
	h := &fakeHub{workerProto: wsproto.CurrentProtocolVersion}
	r := newRunnerWithHub(h)
	runToResultWithWorker(t, r, h, &runner.Forward{
		ProjectKey: "p", Agent: "exec", Cwd: ".",
		Messenger: &runner.MessengerDispatch{Op: "list_agents", Command: []string{"claude"}},
	})
	if d := h.dispatchedFrame(); d.Messenger == nil || d.Messenger.Op != "list_agents" {
		t.Fatalf("wire messenger = %+v", d.Messenger)
	}
}

// A model needs the v19 dispatch field: an older worker would run its default model
// while the job claims another, so the dispatch is refused with the field named.
func TestUnsupportedDispatchFieldsModel(t *testing.T) {
	f := &runner.Forward{Model: "opus"}
	if got := unsupportedDispatchFields(wsproto.ModelMinProtocolVersion-1, f); len(got) != 1 || got[0] != "model" {
		t.Fatalf("lacks = %v, want [model]", got)
	}
	if got := unsupportedDispatchFields(wsproto.ModelMinProtocolVersion, f); len(got) != 0 {
		t.Fatalf("lacks = %v, want none", got)
	}
	if got := unsupportedDispatchFields(2, &runner.Forward{}); len(got) != 0 {
		t.Fatalf("a model-less job must never be refused: %v", got)
	}
}
