package job

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// Internal exec jobs (plan S7): jobs gofer starts on a caller's behalf with a command gofer
// itself composed carry the caller's ceiling owner, are judged on project / runner / worker,
// and are exempt only from the owner's exec dimension.

// TestDeliveryJobInheritsOwnerAndIsJudgedByIt: the delivery job takes the delivered job's
// ceiling owner (it is NOT an owner-less system job), is refused for a project / runner /
// worker outside that owner's ceiling, and passes an owner who may not run exec at all.
func TestDeliveryJobInheritsOwnerAndIsJudgedByIt(t *testing.T) {
	t.Parallel()
	snap := JobResult{ID: "job-1", ProjectKey: "self", Runner: "w-a", OriginCaller: "ci-bot"}
	req := deliveryJobRequest(snap, []byte("{}"))
	if req.OriginCaller != "ci-bot" {
		t.Fatalf("delivery OriginCaller = %q, want the delivered job's owner", req.OriginCaller)
	}
	if req.SystemJob != "" {
		t.Fatalf("delivery must not be a system job, got %q", req.SystemJob)
	}
	if req.InternalExec != InternalExecDelivery {
		t.Fatalf("delivery InternalExec = %q, want %q", req.InternalExec, InternalExecDelivery)
	}

	worker := func(mutate func(*config.CallerCeiling)) *config.Config {
		cfg := cfgWithCeiling("ci-bot", mutate)
		cfg.Runners = map[string]config.RunnerConfig{"w-a": {Type: "worker", WorkerID: "w-1"}}
		return cfg
	}
	// Exec is off (the default), project / runner / worker are inside: admitted.
	ok := worker(func(c *config.CallerCeiling) {
		c.Projects, c.Runners, c.Workers = []string{"self"}, []string{"w-a"}, []string{"w-1"}
	})
	if err := admitCeiling(ok, req); err != nil {
		t.Fatalf("delivery of an owner with allow_exec off: %v, want admitted", err)
	}
	// Without the marker the very same request is an exec job and is refused.
	plain := req
	plain.InternalExec = ""
	if err := admitCeiling(ok, plain); !errors.Is(err, ErrCeilingDenied) {
		t.Fatalf("the same command without InternalExec: %v, want ErrCeilingDenied", err)
	}
	// Project, runner and worker of the OWNER still bound it.
	for name, mutate := range map[string]func(*config.CallerCeiling){
		"project": func(c *config.CallerCeiling) { c.Projects, c.Runners = []string{"other"}, []string{"w-a"} },
		"runner":  func(c *config.CallerCeiling) { c.Projects, c.Runners = []string{"self"}, []string{"w-b"} },
		"worker": func(c *config.CallerCeiling) {
			c.Projects, c.Runners, c.Workers = []string{"self"}, []string{"w-a"}, []string{"w-9"}
		},
	} {
		if err := admitCeiling(worker(mutate), req); !errors.Is(err, ErrCeilingDenied) {
			t.Errorf("delivery outside the owner's %s: %v, want ErrCeilingDenied", name, err)
		}
	}
	// An unrestricted owner and an owner-less (pre-ceiling) job are unchanged.
	if err := admitCeiling(&config.Config{}, req); err != nil {
		t.Errorf("unrestricted owner: %v", err)
	}
}

// TestInternalExecIsAClosedServerSideMarker: an unknown kind is refused, the marker never
// rides a request body, and it does not waive a real agent's entry in the agents list.
func TestInternalExecIsAClosedServerSideMarker(t *testing.T) {
	t.Parallel()
	cfg := cfgWithCeiling("ci-bot", func(c *config.CallerCeiling) {
		c.Projects, c.Runners, c.Agents = []string{"self"}, []string{"local"}, []string{"codex"}
	})
	exec := JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", OriginCaller: "ci-bot", Cmd: []string{"x"}}

	bad := exec
	bad.InternalExec = "made-up"
	if err := admitCeiling(cfg, bad); !errors.Is(err, ErrCeilingDenied) {
		t.Errorf("unknown InternalExec kind: %v, want ErrCeilingDenied", err)
	}
	for _, kind := range InternalExecKinds() {
		r := exec
		r.InternalExec = kind
		if err := admitCeiling(cfg, r); err != nil {
			t.Errorf("kind %q for an owner whose agents list omits exec: %v, want admitted (the carrier is gofer's)", kind, err)
		}
	}
	// A real agent is still the owner's choice: a session's source agent outside the list is refused.
	r := exec
	r.InternalExec, r.ResumeSourceAgent = InternalExecRelay, "claude"
	if err := admitCeiling(cfg, r); !errors.Is(err, ErrCeilingDenied) {
		t.Errorf("relay job of an agent outside the owner's agents: %v, want ErrCeilingDenied", err)
	}
	// The marker is json:"-": a body or a replayed request_json cannot carry it.
	b, err := json.Marshal(JobRequest{ProjectKey: "self", InternalExec: InternalExecDelivery})
	raw := string(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "internal_exec") || strings.Contains(raw, InternalExecDelivery) {
		t.Errorf("InternalExec leaked into the serialized request: %s", raw)
	}
}
