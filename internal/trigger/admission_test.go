package trigger

import (
	"context"
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

type fakeGate struct {
	preflightErr error
	submits      int
	last         job.JobRequest
}

func (g *fakeGate) PreflightFixedRequest(job.JobRequest) error { return g.preflightErr }
func (g *fakeGate) Submit(r job.JobRequest) (job.JobResult, error) {
	g.submits++
	g.last = r
	return job.JobResult{ID: "job-x"}, nil
}

func admissionCfg() *config.Config {
	return &config.Config{Server: config.ServerConfig{Callers: []config.CallerConfig{{ID: "automation"}, {ID: "root", CanAdmin: true}}}}
}

func TestAdmissionOwnerRules(t *testing.T) {
	granted := true
	gate := &fakeGate{}
	adm := NewAdmission(admissionCfg, gate, func(id, perm string) bool { return granted && perm == authz.PermJobSubmit })
	req := func(owner string) job.JobRequest { return job.JobRequest{OriginCaller: owner, ProjectKey: "demo"} }
	ctx := context.Background()

	if id, err := adm.Submit(ctx, req("automation")); err != nil || id != "job-x" || gate.submits != 1 {
		t.Fatalf("normal: id=%q err=%v submits=%d", id, err, gate.submits)
	}
	if gate.last.OriginCaller != "automation" {
		t.Fatalf("origin=%q", gate.last.OriginCaller)
	}
	granted = false
	if _, err := adm.Submit(ctx, req("automation")); !errors.Is(err, ErrOwnerDenied) {
		t.Fatalf("revoked: %v", err)
	}
	if err := adm.Preflight(ctx, req("automation")); !errors.Is(err, ErrOwnerDenied) {
		t.Fatalf("revoked preflight: %v", err)
	}
	granted = true
	for _, owner := range []string{"", "root", "missing"} {
		if _, err := adm.Submit(ctx, req(owner)); !errors.Is(err, ErrOwnerDenied) {
			t.Fatalf("owner %q: %v", owner, err)
		}
	}
	if gate.submits != 1 {
		t.Fatalf("a refused owner reached the job layer: submits=%d", gate.submits)
	}
	gate.preflightErr = job.ErrCeilingDenied
	if err := adm.Preflight(ctx, req("automation")); !errors.Is(err, job.ErrCeilingDenied) {
		t.Fatalf("ceiling: %v", err)
	}
}
