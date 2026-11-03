package job

import (
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// TestPromptSectionResidentDispatch: the child-dispatch guidance is appended to a RESIDENT
// job's prompt only when the job holds the submit permission (design
// 2026-11-03-resident-job-child-wakeup「范围」4). A job without it, an empty prompt and a
// batch job keep their prompt untouched.
func TestPromptSectionResidentDispatch(t *testing.T) {
	t.Parallel()
	cfg := sectionsCfg()
	cfg.Projects["self"] = config.ProjectConfig{ScopeDiscipline: config.ScopeDisciplineOff, KnowledgeCapture: config.ScopeDisciplineOff}
	section := zhSection(t, prompts.NameResidentDispatch, struct{}{})

	t.Run("a resident job with the permission gets it", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Session: true, Prompt: "watch the queue\n"}
		injectResidentDispatchSection(cfg, &req, true)
		if want := "watch the queue\n\n" + section; req.Prompt != want {
			t.Fatalf("prompt = %q, want %q", req.Prompt, want)
		}
	})
	t.Run("no submit permission, no section", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Session: true, Prompt: "watch the queue"}
		injectResidentDispatchSection(cfg, &req, false)
		assert.Eq(t, "watch the queue", req.Prompt, "a job that cannot submit was told to dispatch children")
	})
	t.Run("an empty prompt stays empty", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Session: true}
		injectResidentDispatchSection(cfg, &req, true)
		assert.Eq(t, "", req.Prompt, "a resident job started without a prompt must not be given a turn")
	})
	t.Run("a batch job is not touched", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "do it"}
		injectResidentDispatchSection(cfg, &req, true)
		assert.Eq(t, "do it", req.Prompt, "a batch job is woken by a continuation, not by a say")
	})
	t.Run("gofer's own plumbing is not touched", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Session: true, Steward: true, Prompt: "review"}
		injectResidentDispatchSection(cfg, &req, true)
		assert.Eq(t, "review", req.Prompt, "the steward got turn guidance")
	})
	t.Run("a prompt that already carries it is not duplicated", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Session: true, Prompt: "watch the queue"}
		injectResidentDispatchSection(cfg, &req, true)
		once := req.Prompt
		injectResidentDispatchSection(cfg, &req, true)
		assert.Eq(t, once, req.Prompt, "the second injection changed the prompt")
		assert.Eq(t, 1, strings.Count(req.Prompt, i18n.Title(i18n.ZhCN, i18n.SectionResidentDispatch)))
	})
	t.Run("the language follows the job", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Session: true, Language: "en", Prompt: "watch the queue"}
		injectResidentDispatchSection(cfg, &req, true)
		if want := i18n.Title(i18n.EN, i18n.SectionResidentDispatch); !strings.Contains(req.Prompt, want) {
			t.Fatalf("prompt = %q, want the English heading %q", req.Prompt, want)
		}
	})
}

// TestPromptSectionResidentDispatchAtSubmit is the wiring: Submit decides the permission
// itself (SubmitGrantFor on the request's agent) and the admitted job's prompt carries the
// section — and does not when the agent has no can_submit.
func TestPromptSectionResidentDispatchAtSubmit(t *testing.T) {
	t.Parallel()
	heading := i18n.Title(i18n.ZhCN, i18n.SectionResidentDispatch)
	submit := func(t *testing.T, canSubmit bool) string {
		t.Helper()
		s := newACPServiceAgent(t, t.TempDir(), acptest.Options{}, nil, func(ac *config.AgentConfig) {
			ac.CanSubmit = canSubmit
		})
		created, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
			Prompt: "watch the queue", Session: true, TimeoutSec: 20, IdleTimeoutSec: 10,
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		waitSessionStatus(t, s, created.ID, StatusAwaitingInput, 10*time.Second)
		current, ok := s.Get(created.ID)
		if !ok {
			t.Fatalf("job %s is gone", created.ID)
		}
		if err := s.EndSession(created.ID); err != nil {
			t.Fatalf("end session: %v", err)
		}
		return requestOf(t, current).Prompt
	}

	if got := submit(t, true); !strings.Contains(got, heading) {
		t.Errorf("prompt of a resident job with can_submit lacks %q: %q", heading, got)
	}
	if got := submit(t, false); strings.Contains(got, heading) {
		t.Errorf("prompt of a resident job without can_submit carries %q: %q", heading, got)
	}
}
