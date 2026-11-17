package job

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

// A hosted session for a worker that cannot carry hosted (protocol below v32) is refused at
// submit, never downgraded to one that the worker's idle default ends; a newer worker gets
// hosted on the forward; a session that is not hosted is never refused.
func TestHostedSessionWorkerProtocolGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		proto   int
		hosted  bool
		wantErr bool
	}{
		{"old worker + hosted", wsproto.HostedMinProtocolVersion - 1, true, true},
		{"new worker + hosted", wsproto.HostedMinProtocolVersion, true, false},
		{"old worker, not hosted", wsproto.HostedMinProtocolVersion - 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubWorkerRunner{}
			cand := selfCaps(WorkerCandidate{WorkerID: "w1", ProtocolVersion: tc.proto, ProtocolKnown: true})
			cand.Agents = append(cand.Agents, "acpw")
			sel := fakeSelector{cands: []WorkerCandidate{cand}}
			s := newWorkerTestServiceSel(t, t.TempDir(), stub, map[string]config.WorkerAuthConfig{"w1": {Token: "t"}}, sel)
			res, err := s.Submit(JobRequest{
				ProjectKey: "self", Agent: "acpw", Runner: "remote-w1", Prompt: "hi", Cwd: ".", TimeoutSec: 30,
				Session: true, Hosted: tc.hosted,
			})
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "hosted") {
					t.Fatalf("err = %v, want ErrInvalidRequest naming hosted (worker too old)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			s.Wait(res.ID)
			if stub.gotForward == nil || stub.gotForward.Hosted != tc.hosted {
				t.Fatalf("forward = %+v, want hosted=%v carried", stub.gotForward, tc.hosted)
			}
		})
	}
}

type endReasonRunner struct{ reason string }

func (r endReasonRunner) Name() string { return "remote-w1" }
func (r endReasonRunner) Run(_ context.Context, _ runner.Request) runner.Result {
	return runner.Result{Outcome: &runner.Outcome{Source: "worker:w1", SessionEndReason: r.reason}}
}

// A worker's own reason its session ended (v32 outcome.session_end_reason) lands on the host
// row, where `job show` and the notification read it.
func TestWorkerSessionEndReasonReachesHostRow(t *testing.T) {
	t.Parallel()
	s := newWorkerTestService(t, t.TempDir(), endReasonRunner{reason: "idle_timeout"})
	res, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "remote-w1", Cmd: []string{"echo", "x"}, Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	final, _ := s.Wait(res.ID)
	if final.SessionEndReason != "idle_timeout" {
		t.Fatalf("session_end_reason = %q, want idle_timeout (final=%+v)", final.SessionEndReason, final)
	}
}
