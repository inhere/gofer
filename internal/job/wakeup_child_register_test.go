package job

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// T6: a resident job's own credential submitting a job registers its receipt.
//
// The parent is the remote-session fixture (a LIVE resident session job); the child is a
// real submit, so the row the receipt is built from is the one a run really produces.

// grantSubmitToExec gives the "exec" agent of this service the SEC-01 submit grant, with a
// short wakeup TTL so the child-receipt TTL (child limit + queue margin) is observable.
func grantSubmitToExec(t *testing.T, s *Service) {
	t.Helper()
	cfg := s.config().Clone()
	if cfg.Agents == nil {
		cfg.Agents = map[string]config.AgentConfig{}
	}
	ac := cfg.Agents["exec"]
	ac.CanSubmit = true
	ac.SubmitAgents = []string{"exec"}
	cfg.Agents["exec"] = ac
	cfg.Wakeup.TTLSec = 60
	s.Reload(cfg)
}

// submitChild submits one short exec job as if the resident parent's credential did.
func submitChild(t *testing.T, s *Service, req JobRequest) JobResult {
	t.Helper()
	res, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit child: %v", err)
	}
	if _, ok := s.Wait(res.ID); !ok {
		t.Fatalf("Wait: job %s not found", res.ID)
	}
	return res
}

// childReceipts returns the automatic receipts registered on a parent.
func childReceipts(t *testing.T, s *Service, parentID string) []jobstore.WakeupRecord {
	t.Helper()
	rows, err := s.meta.ListAutoChildByParent(parentID)
	if err != nil {
		t.Fatalf("ListAutoChildByParent: %v", err)
	}
	return rows
}

// TestChildWakeRegisteredForResidentParent: one submission by a resident parent's
// credential produces ONE continuous event receipt on the parent, watching the child,
// created by the parent's caller and armed for longer than the wakeup TTL because the
// child may still be queuing.
func TestChildWakeRegisteredForResidentParent(t *testing.T) {
	t.Parallel()
	s, _, parent := wakeupSessionFixture(t)
	grantSubmitToExec(t, s)

	child := submitChild(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 600,
		ParentJobID: parent.result.ID,
	})

	rows := childReceipts(t, s, parent.result.ID)
	assert.Eq(t, 1, len(rows), "receipts = %+v", rows)
	w := rows[0]
	assert.Eq(t, jobstore.WakeupKindEvent, w.Kind)
	assert.Eq(t, jobstore.WakeupModeContinuous, w.Mode, "an automatic receipt must survive a needs-review child")
	assert.Eq(t, child.ID, w.FilterJobID, "the receipt must watch the child, not the parent")
	assert.Eq(t, 1, w.AutoChild)
	assert.Eq(t, parent.result.CallerID, w.CreatedBy, "the receipt belongs to the parent's caller")
	assert.Eq(t, 1, w.Enabled)
	events := jobstore.DecodeStringList(w.EventTypesJSON)
	assert.True(t, contains(events, EventJobTerminal) && contains(events, EventJobNeedsReview),
		"events = %v, want the configured default", events)
	// 600s child limit + the 3600s queue margin, well past the 60s wakeup TTL.
	minExpiry := s.nowFn().Add(4000 * time.Second).Unix()
	assert.True(t, w.ExpiresAt >= minExpiry, "expires_at = %d, want at least %d", w.ExpiresAt, minExpiry)
}

// TestChildWakeSkipped: nothing is registered for the cases in which the parent did not
// ask for a receipt — a non-credential submit, an explicit opt-out, a waited sync submit,
// a parent that is not a resident session, a parent without the submit grant, and the
// re-entry of a held job (which would otherwise register the same child twice).
func TestChildWakeSkipped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Service, parent *jobEntry) JobRequest
	}{
		{"no parent", func(_ *testing.T, _ *Service, _ *jobEntry) JobRequest {
			return JobRequest{}
		}},
		{"no-parent-wake", func(_ *testing.T, _ *Service, parent *jobEntry) JobRequest {
			return JobRequest{NoParentWake: true, ParentJobID: parent.result.ID}
		}},
		{"waited sync submit", func(_ *testing.T, _ *Service, parent *jobEntry) JobRequest {
			return JobRequest{ParentWakeSync: true, ParentJobID: parent.result.ID}
		}},
		{"hold re-entry", func(_ *testing.T, _ *Service, parent *jobEntry) JobRequest {
			return JobRequest{ParentJobID: parent.result.ID, heldJobID: "job-held"}
		}},
		{"parent is not a session", func(_ *testing.T, s *Service, _ *jobEntry) JobRequest {
			return JobRequest{ParentJobID: "plain"}
		}},
		{"parent has no grant", func(_ *testing.T, s *Service, parent *jobEntry) JobRequest {
			cfg := s.config().Clone()
			ac := cfg.Agents["exec"]
			ac.CanSubmit, ac.SubmitAgents = false, nil
			cfg.Agents["exec"] = ac
			s.Reload(cfg)
			return JobRequest{ParentJobID: parent.result.ID}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, parent := wakeupSessionFixture(t)
			grantSubmitToExec(t, s)
			addChildJob(t, s, "plain", StatusDone, 0, "") // a parent id that is not a session
			req := tc.setup(t, s, parent)
			req.ProjectKey, req.Agent, req.Runner = "self", "exec", "local"
			req.Cmd, req.Cwd, req.TimeoutSec = []string{"go", "version"}, ".", 60

			child := submitChild(t, s, req)

			assert.Eq(t, 0, len(childReceipts(t, s, parent.result.ID)),
				"a receipt was registered on the resident parent")
			assert.Eq(t, 0, len(childReceipts(t, s, "plain")))
			// The job itself still ran: only the receipt was skipped.
			assert.True(t, child.ID != "")
		})
	}
}

// TestChildWakeParkPathRegistersOnce: a held (or update-parked) child registers its
// receipt at the park point — the child can raise an approval before it ever runs — and
// the approval that releases it does NOT register a second one.
func TestChildWakeParkPathRegistersOnce(t *testing.T) {
	t.Parallel()
	s, _, parent := wakeupSessionFixture(t)
	grantSubmitToExec(t, s)

	child := submitChild(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60,
		Hold: true, HoldReason: "release gate", ParentJobID: parent.result.ID,
	})
	assert.Eq(t, StatusAwaitingApproval, child.Status)
	assert.Eq(t, 1, len(childReceipts(t, s, parent.result.ID)), "the parked child registered no receipt")

	if _, err := s.ApproveJob(child.ID, "alice", ""); err != nil {
		t.Fatalf("ApproveJob: %v", err)
	}
	assert.Eq(t, 1, len(childReceipts(t, s, parent.result.ID)),
		"the approval re-entry registered the receipt a second time")
}
