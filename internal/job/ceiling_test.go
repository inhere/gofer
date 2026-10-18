package job

import (
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// submitCeilingSourceJob starts a trivial terminal job whose ceiling owner is owner and
// returns its id: the fixture the ceiling-owner tests build a derived submission from.
func submitCeilingSourceJob(t *testing.T, s *Service, owner string) string {
	t.Helper()
	res := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60,
		CallerID: "src-caller", OriginCaller: owner,
	})
	return res.ID
}

// cfgWithCeiling returns a config in which caller id carries a ceiling. mutate may set
// the dimensions; nil leaves the strict defaults (no machine runner, no exec).
func cfgWithCeiling(id string, mutate func(*config.CallerCeiling)) *config.Config {
	ceil := &config.CallerCeiling{}
	if mutate != nil {
		mutate(ceil)
	}
	cfg := &config.Config{}
	cfg.Server.Callers = []config.CallerConfig{{ID: id, Ceiling: ceil}}
	return cfg
}

// TestCredentialCeilingOwnerFailsClosed pins the entry rule for a job credential: the
// ceiling owner is the owner of the job the credential belongs to, and a job row that
// cannot be read REFUSES the submit (ErrCeilingDenied) instead of answering "no owner" —
// which would turn a restricted caller's derived chain into an unchecked system job.
func TestCredentialCeilingOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())

	if _, err := s.CredentialCeilingOwner("job-does-not-exist"); !errors.Is(err, ErrCeilingDenied) {
		t.Fatalf("missing job: err = %v, want ErrCeilingDenied", err)
	}

	owner := submitCeilingSourceJob(t, s, "ci-bot")
	got, err := s.CredentialCeilingOwner(owner)
	if err != nil {
		t.Fatalf("CredentialCeilingOwner: %v", err)
	}
	if got != "ci-bot" {
		t.Fatalf("owner = %q, want ci-bot", got)
	}
}

// TestCommentDispatchOwnerIsInitiatorNotAuthor: a comment-dispatched job keeps the
// author NAME on CallerID (audit/display) but takes its ceiling owner from the comment's
// initiator — an agent name is not a caller id and must never be resolved as one.
func TestCommentDispatchOwnerIsInitiatorNotAuthor(t *testing.T) {
	t.Parallel()
	target := commentTarget{projectKey: "self", cwd: ".", runner: "local"}
	cm := &jobstore.Comment{ID: "cm-1", Author: "codex", AuthorKind: jobstore.CommentAuthorAgent, Body: "@codex do it"}

	req := target.dispatchRequest(cm, "codex", "agent", "alice")
	if req.CallerID != "codex" {
		t.Fatalf("caller_id = %q, want the author name codex", req.CallerID)
	}
	if req.OriginCaller != "alice" {
		t.Fatalf("origin_caller = %q, want the initiator alice", req.OriginCaller)
	}

	// A comment written from inside a job carries that job's owner instead; a job with no
	// owner (a system job) dispatches with none.
	req = target.dispatchRequest(cm, "codex", "agent", "")
	if req.OriginCaller != "" {
		t.Fatalf("origin_caller = %q, want empty", req.OriginCaller)
	}
}

// TestRebuildCarriesInitiatorAndSourceOwners: a rerun is bounded by BOTH the caller who
// asked for it and the owner of the replayed work.
func TestRebuildCarriesInitiatorAndSourceOwners(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	id := submitCeilingSourceJob(t, s, "ci-bot")

	res, err := s.RebuildJob(id, RebuildOverrides{}, "alice", "127.0.0.1")
	if err != nil {
		t.Fatalf("RebuildJob: %v", err)
	}
	req := requestFromJSON(t, res.RequestJSON)
	if req.OriginCaller != "alice" {
		t.Fatalf("origin_caller = %q, want the initiator alice", req.OriginCaller)
	}
	if len(req.ExtraCeilingOwners) != 1 || req.ExtraCeilingOwners[0] != "ci-bot" {
		t.Fatalf("extra owners = %v, want [ci-bot]", req.ExtraCeilingOwners)
	}
	s.Wait(res.ID)

	// The source's own owner re-running its own work carries no duplicate owner.
	res2, err := s.RebuildJob(id, RebuildOverrides{}, "ci-bot", "127.0.0.1")
	if err != nil {
		t.Fatalf("RebuildJob: %v", err)
	}
	if got := requestFromJSON(t, res2.RequestJSON).ExtraCeilingOwners; len(got) != 0 {
		t.Fatalf("extra owners = %v, want none", got)
	}
	s.Wait(res2.ID)
}

// TestSystemJobRegistry: the exemption list is explicit, queryable, and closed — an
// unknown kind is never a system job.
func TestSystemJobRegistry(t *testing.T) {
	t.Parallel()
	kinds := SystemJobKinds()
	if len(kinds) == 0 {
		t.Fatal("the system job registry is empty")
	}
	for _, k := range kinds {
		if !IsSystemJobKind(k) {
			t.Fatalf("%q is registered but IsSystemJobKind says no", k)
		}
	}
	for _, k := range []string{"", "not-a-kind", "job"} {
		if IsSystemJobKind(k) {
			t.Fatalf("%q is not a registered system job", k)
		}
	}
}

// TestTodoCeilingOwnersDropsRedundantAuthor: the item's author is an extra owner only
// when it differs from the caller triggering the dispatch.
func TestTodoCeilingOwnersDropsRedundantAuthor(t *testing.T) {
	t.Parallel()
	if got := todoCeilingOwners(jobstore.PlanTodo{AuthorCaller: "alice"}, "alice"); len(got) != 0 {
		t.Fatalf("same caller: %v, want none", got)
	}
	if got := todoCeilingOwners(jobstore.PlanTodo{AuthorCaller: "ci-bot"}, "alice"); len(got) != 1 || got[0] != "ci-bot" {
		t.Fatalf("other author: %v, want [ci-bot]", got)
	}
	if got := todoCeilingOwners(jobstore.PlanTodo{}, "alice"); len(got) != 0 {
		t.Fatalf("unknown author: %v, want none", got)
	}
}

// TestInheritCeilingOwnerFailsClosed: a derivative whose source row is gone is refused
// when the chain still names a RESTRICTED caller, and is left alone otherwise.
func TestInheritCeilingOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	cfg := cfgWithCeiling("ci-bot", nil)

	src := &JobResult{OriginCaller: "ci-bot"}
	if got, err := inheritCeilingOwner(cfg, src, "whatever"); err != nil || got != "ci-bot" {
		t.Fatalf("source present: %q %v, want ci-bot/nil", got, err)
	}
	if _, err := inheritCeilingOwner(cfg, nil, "ci-bot"); !errors.Is(err, ErrCeilingDenied) {
		t.Fatalf("restricted chain without a source: err = %v, want ErrCeilingDenied", err)
	}
	if got, err := inheritCeilingOwner(cfg, nil, "alice"); err != nil || got != "" {
		t.Fatalf("unrestricted chain: %q %v, want empty/nil", got, err)
	}
	if got, err := inheritCeilingOwner(cfg, nil, ""); err != nil || got != "" {
		t.Fatalf("no chain caller: %q %v, want empty/nil", got, err)
	}
}

// TestAdmitCeilingDimensions covers every dimension a ceiling declares: project, runner
// (the machine runner must be named explicitly), the routed worker, the agent and the
// four exec forms — plus the extra owners a derived dispatch carries. An unrestricted
// caller and a request with no owner at all keep the previous behaviour.
func TestAdmitCeilingDimensions(t *testing.T) {
	t.Parallel()
	// A worker runner pinned to w-1, for the worker dimension.
	workerCfg := func() *config.Config {
		cfg := &config.Config{Runners: map[string]config.RunnerConfig{"w-a": {Type: "worker", WorkerID: "w-1"}}}
		return cfg
	}
	strict := func(mutate func(*config.CallerCeiling)) *config.Config {
		return cfgWithCeiling("ci-bot", mutate)
	}

	base := func(req *JobRequest) {
		*req = JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", OriginCaller: "ci-bot",
			Cmd: []string{"go", "version"}}
	}

	tests := []struct {
		name  string
		cfg   *config.Config
		req   func(*JobRequest)
		okEnv bool
	}{
		{"no ceiling for the owner", &config.Config{}, base, true},
		{"no owner at all (system)", strict(nil), func(r *JobRequest) { base(r); r.OriginCaller = "" }, true},
		{"project allowed", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
			c.AllowExec = boolPtr(true)
		}), base, true},
		{"project denied", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"other"}
			c.Runners = []string{"local"}
			c.AllowExec = boolPtr(true)
		}), base, false},
		{"machine runner not named", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.AllowExec = boolPtr(true)
		}), base, false},
		{"machine runner named", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"server"}
			c.AllowExec = boolPtr(true)
		}), base, true},
		{"worker allowed", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"w-a"}
			c.Workers = []string{"w-1"}
			c.Agents = []string{"exec"}
			c.AllowExec = boolPtr(true)
		}), func(r *JobRequest) {
			base(r)
			r.Runner, r.Cmd = "w-a", nil
			r.Agent = "exec"
		}, true},
		{"worker denied", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"w-a"}
			c.Workers = []string{"w-2"}
		}), func(r *JobRequest) {
			base(r)
			r.Runner, r.Cmd = "w-a", nil
		}, false},
		{"agent denied", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
			c.Agents = []string{"codex"}
			c.AllowExec = boolPtr(true)
		}), base, false},
		{"exec denied: exec agent", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
		}), base, false},
		{"exec denied: cmd", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
		}), func(r *JobRequest) {
			base(r)
			r.Agent, r.Cmd = "codex", []string{"ls"}
		}, false},
		{"exec denied: verify", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
		}), func(r *JobRequest) {
			base(r)
			r.Agent, r.Cmd, r.Verify = "codex", nil, []string{"go", "test"}
		}, false},
		{"exec denied: agent argv", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
		}), func(r *JobRequest) {
			base(r)
			r.Agent, r.Cmd, r.AgentArgs = "codex", nil, []string{"--yolo"}
		}, false},
		{"resume carrier uses the source agent, not exec", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
			c.Runners = []string{"local"}
			c.Agents = []string{"codex"}
		}), func(r *JobRequest) {
			base(r)
			r.Agent, r.Cmd, r.ResumeSourceAgent = "exec", nil, "codex"
		}, true},
		{"extra owner denied", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
		}), func(r *JobRequest) {
			base(r)
			r.OriginCaller = "alice"
			r.ExtraCeilingOwners = []string{"ci-bot"}
		}, false},
		{"registered system job without owner", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"other"}
		}), func(r *JobRequest) {
			base(r)
			r.OriginCaller, r.SystemJob = "", SystemJobUpgrade
		}, true},
		{"system job carrying an owner", strict(func(c *config.CallerCeiling) {
			c.Projects = []string{"self"}
		}), func(r *JobRequest) { base(r); r.SystemJob = SystemJobUpgrade }, false},
		{"unknown system job kind", strict(nil), func(r *JobRequest) {
			base(r)
			r.OriginCaller, r.SystemJob = "", "not-a-kind"
		}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			if tc.name == "worker allowed" || tc.name == "worker denied" {
				w := workerCfg()
				w.Server = cfg.Server
				cfg = w
			}
			var req JobRequest
			tc.req(&req)
			err := admitCeiling(cfg, req)
			if tc.okEnv && err != nil {
				t.Fatalf("want allowed, got %v", err)
			}
			if !tc.okEnv && !errors.Is(err, ErrCeilingDenied) {
				t.Fatalf("want ErrCeilingDenied, got %v", err)
			}
		})
	}
}

// TestCeilingOwnersCollectsEveryOwner: the admission checks the origin owner plus every
// extra owner, without duplicates and without treating "" as a caller.
func TestCeilingOwnersCollectsEveryOwner(t *testing.T) {
	t.Parallel()
	req := JobRequest{OriginCaller: "alice", ExtraCeilingOwners: []string{"", "ci-bot", "alice"}}
	got := ceilingOwners(req)
	if len(got) != 2 || got[0] != "alice" || got[1] != "ci-bot" {
		t.Fatalf("owners = %v, want [alice ci-bot]", got)
	}
	if got := ceilingOwners(JobRequest{}); len(got) != 0 {
		t.Fatalf("no owner: %v, want none", got)
	}
}
