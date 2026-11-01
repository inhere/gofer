package webpush

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/golden"
)

// pushTextOutputs renders every title / body / button the push builders produce, in lang.
// The zh-CN golden was recorded from the code BEFORE the text moved into the catalog.
func pushTextOutputs(lang i18n.Lang) string {
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()
	var sb strings.Builder
	line := func(label, title, body string) {
		sb.WriteString(fmt.Sprintf("%s | %s | %s\n", label, title, strings.ReplaceAll(body, "\n", "\\n")))
	}
	r := job.JobResult{ID: "job-1", Title: "Fix login", Agent: "claude", ProjectKey: "proj", Runner: "local"}
	inter := &job.Interaction{Prompt: "Run the tests?"}
	t1, b1 := pushTitleBody(lang, job.EventInteractionCreated, r, nil, inter)
	line("interaction", t1, b1)
	t2, b2 := pushTitleBody(lang, job.EventJobNeedsReview, r, nil, nil)
	line("needs review", t2, b2)
	h := r
	h.Hold = &job.HoldState{Reason: "needs sign-off"}
	t3, b3 := pushTitleBody(lang, job.EventJobAwaitingApproval, h, nil, nil)
	line("approval", t3, b3)
	t4, b4 := pushTitleBody(lang, job.EventJobTerminal, r, nil, nil)
	line("terminal job", t4, b4)
	for _, reason := range []string{"manual_end", "idle_timeout", "max_session_timeout", "max_session", "", "boom"} {
		s := r
		s.Session, s.SessionEndReason = true, reason
		ts, bs := pushTitleBody(lang, job.EventJobTerminal, s, nil, nil)
		line("terminal session "+reason, ts, bs)
	}
	t5, b5 := pushTitleBody(lang, job.EventSessionAwaitingReply, r, map[string]any{"turn_no": 3, "reply_preview": "done?", "idle_deadline_at": float64(1_700_000_000)}, nil)
	line("awaiting reply", t5, b5)
	t6, b6 := pushTitleBody(lang, job.EventSessionAwaitingReply, r, map[string]any{"turn_no": 1}, nil)
	line("awaiting reply bare", t6, b6)
	t7, b7 := pushTitleBody(lang, job.EventPlanBlocked, r, map[string]any{"reason": "no worker"}, nil)
	line("plan blocked", t7, b7)
	tp := testPayload(lang)
	line("test", tp.Title, tp.Body)
	for _, a := range permissionActions(lang, []job.InteractionOption{{Kind: "allow_once", Value: "a"}, {Kind: "reject_once", Value: "r"}}) {
		sb.WriteString("action " + a.Action + " " + a.Title + "\n")
	}
	return sb.String()
}

func TestPushTextGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) { golden.Check(t, lang, "push_text", pushTextOutputs(lang)) })
	}
}

// TestPushLanguageFollowsTheProject: an event's push is in the language of the job's
// project; the test notification is in the language the request asked for.
func TestPushLanguageFollowsTheProject(t *testing.T) {
	s := &Service{languageFor: func(project string) i18n.Lang {
		if project == "p-en" {
			return i18n.EN
		}
		return i18n.ZhCN
	}}
	if s.lang("p-en") != i18n.EN || s.lang("p-zh") != i18n.ZhCN || s.lang("") != i18n.ZhCN {
		t.Errorf("lang = %q %q %q", s.lang("p-en"), s.lang("p-zh"), s.lang(""))
	}
	if (&Service{}).lang("x") != i18n.Default {
		t.Error("a service without a resolver must use the default language")
	}
}
