package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// decideHook is a decision hook running the testcmd stand-in: it answers with answer
// and, when capture is set, saves its stdin there.
func decideHook(t *testing.T, name, answer, capture string, points ...string) config.DecisionHookConfig {
	t.Helper()
	if len(points) == 0 {
		points = []string{config.DecisionPointSubmit}
	}
	cmd := []string{testcmd.Path(t), "decide", answer}
	if capture != "" {
		cmd = append(cmd, capture)
	}
	return config.DecisionHookConfig{Name: name, Points: points, Command: cmd, TimeoutSec: 20}
}

func setDecisionHooks(s *Service, hooks ...config.DecisionHookConfig) {
	s.cfg.Load().Server.DecisionHooks = hooks
}

func plainExecJob() JobRequest {
	return JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"git", "status"}, Cwd: ".", TimeoutSec: 60, CallerID: "alice"}
}

func readHookInput(t *testing.T, path string) submitHookInput {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the hook was not run: %v", err)
	}
	var in submitHookInput
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("hook stdin %s: %v", b, err)
	}
	return in
}

func TestDecisionHookDeniesSubmit(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	capture := filepath.Join(t.TempDir(), "in.json")
	setDecisionHooks(s, decideHook(t, "no-git", `{"decision":"deny","reason":"no git here"}`, capture))

	_, err := s.Submit(plainExecJob())
	if !errors.Is(err, ErrDecisionDenied) || !strings.Contains(err.Error(), "no-git: no git here") {
		t.Fatalf("submit: %v", err)
	}
	in := readHookInput(t, capture)
	if in.Version != 1 || in.Point != config.DecisionPointSubmit || in.Hook != "no-git" || in.Project != "repo" ||
		in.Caller.ID != "alice" || in.Job.Kind != "submit" || in.Job.Agent != "exec" ||
		!slices.Equal(in.Job.Cmd, []string{"git", "status"}) || in.Job.TimeoutSec != 60 {
		t.Fatalf("hook input = %+v", in)
	}
	list, _ := s.ListJobs(ListOpts{All: true})
	if len(list) != 0 {
		t.Fatalf("a denied submit created a job: %+v", list)
	}
}

func TestDecisionHookPatchTightensSubmit(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	second := filepath.Join(t.TempDir(), "second.json")
	setDecisionHooks(s,
		decideHook(t, "night", `{"decision":"allow","reason":"after hours","patch":{"review":true,"tags_add":["night"],"timeout_sec":30,"title":"checked","acceptance_append":"- reviewed at night"}}`, ""),
		decideHook(t, "observer", `{"decision":"allow"}`, second),
	)
	j := submitAndWait(t, s, plainExecJob())
	if j.Status != StatusNeedsReview || !slices.Contains(j.Tags, "night") || j.TimeoutSec != 30 ||
		j.Title != "checked" || !strings.Contains(j.Acceptance, "- reviewed at night") {
		t.Fatalf("job = status %s tags %v timeout %d title %q acceptance %q", j.Status, j.Tags, j.TimeoutSec, j.Title, j.Acceptance)
	}
	// The second hook saw the first one's patch.
	if in := readHookInput(t, second); !in.Job.Review || !slices.Contains(in.Job.Tags, "night") || in.Job.TimeoutSec != 30 {
		t.Fatalf("second hook input = %+v", in.Job)
	}
	var decisions []string
	evs, _ := s.ListJobEvents(j.ID, 0)
	for _, e := range evs {
		if e.Type == EventJobDecisionHook {
			decisions = append(decisions, e.Detail)
		}
	}
	if len(decisions) != 2 || !strings.Contains(decisions[0], `"hook":"night"`) || !strings.Contains(decisions[0], `"review":true`) ||
		!strings.Contains(decisions[0], `"reason":"after hours"`) || !strings.Contains(decisions[1], `"hook":"observer"`) {
		t.Fatalf("decision events = %v", decisions)
	}
}

// Anything but a conservative answer is a failed run, handled by on_error.
func TestDecisionHookFailuresFollowOnError(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	for name, hook := range map[string]config.DecisionHookConfig{
		"longer timeout":  decideHook(t, "h", `{"decision":"allow","patch":{"timeout_sec":99999}}`, ""),
		"review off":      decideHook(t, "h", `{"decision":"allow","patch":{"review":false}}`, ""),
		"other field":     decideHook(t, "h", `{"decision":"allow","patch":{"agent":"codex"}}`, ""),
		"unknown verdict": decideHook(t, "h", `{"decision":"maybe"}`, ""),
		"not json":        decideHook(t, "h", `allow`, ""),
		"deny with patch": decideHook(t, "h", `{"decision":"deny","patch":{"review":true}}`, ""),
		"exit 3":          {Name: "h", Points: []string{config.DecisionPointSubmit}, Command: []string{testcmd.Path(t), "exit", "3"}},
		"missing command": {Name: "h", Points: []string{config.DecisionPointSubmit}, Command: []string{filepath.Join(t.TempDir(), "nope")}},
	} {
		setDecisionHooks(s, hook)
		if _, err := s.Submit(plainExecJob()); !errors.Is(err, ErrDecisionDenied) || !strings.Contains(err.Error(), "the decision hook failed") {
			t.Errorf("%s with on_error deny: %v", name, err)
		}
		hook.OnError = config.DecisionOnErrorAllow
		setDecisionHooks(s, hook)
		j := submitAndWait(t, s, plainExecJob())
		if j.Status != StatusDone || j.TimeoutSec != 60 || j.RequireReview {
			t.Errorf("%s with on_error allow: status %s timeout %d review %v", name, j.Status, j.TimeoutSec, j.RequireReview)
		}
		if !hasSubsequence(eventTypes(t, s, j.ID), []string{EventJobSubmitted, EventJobDecisionHook}) {
			t.Errorf("%s: the failure was not recorded on the job", name)
		}
	}
}

// A hook may turn a job into a held one; the approval runs exactly that job without
// asking the hooks again.
func TestDecisionHookHoldsAndApprovalSkipsHooks(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	setDecisionHooks(s, decideHook(t, "gate", `{"decision":"allow","patch":{"hold":true,"hold_reason":"needs a human","tags_add":["gated"]}}`, ""))
	held, err := s.Submit(plainExecJob())
	if err != nil {
		t.Fatal(err)
	}
	if held.Status != StatusAwaitingApproval || held.Hold == nil || held.Hold.Reason != "needs a human" {
		t.Fatalf("held = %s %+v", held.Status, held.Hold)
	}
	// Were the hooks asked again, this one would refuse the approved job.
	setDecisionHooks(s, decideHook(t, "gate", `{"decision":"deny","reason":"asked twice"}`, ""))
	if _, err := s.ApproveJob(held.ID, "bob", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	j, _ := s.Wait(held.ID)
	if j.Status != StatusDone || !slices.Contains(j.Tags, "gated") {
		t.Fatalf("approved job = %s tags %v err %s", j.Status, j.Tags, j.Error)
	}
}

// gofer's own internal jobs only reach include_internal hooks.
func TestDecisionHookSkipsInternalJobs(t *testing.T) {
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	deny := decideHook(t, "strict", `{"decision":"deny","reason":"no"}`, "")
	setDecisionHooks(s, deny)
	internal := plainExecJob()
	internal.Tags = []string{TrackerSyncJobTag}
	if j := submitAndWait(t, s, internal); j.Status != StatusDone {
		t.Fatalf("internal job = %s %s", j.Status, j.Error)
	}
	// A worker re-entering a job the hub already decided does not ask again.
	reentry := plainExecJob()
	reentry.CredentialExternal = true
	if j := submitAndWait(t, s, reentry); j.Status != StatusDone {
		t.Fatalf("worker re-entry = %s %s", j.Status, j.Error)
	}
	deny.IncludeInternal = true
	setDecisionHooks(s, deny)
	if _, err := s.Submit(internal); !errors.Is(err, ErrDecisionDenied) {
		t.Fatalf("include_internal: %v", err)
	}
}
