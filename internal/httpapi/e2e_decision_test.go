package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

// TestE2EDecisionAskAnswer drives the full decision-channel loop (验收1) over
// real HTTP through the public client: ask -> OPEN visible on list/get ->
// answer -> ANSWERED with answer/answered_by/answered_at; plus the client-side
// view of the 404/409 split and the plan-detail inline.
func TestE2EDecisionAskAnswer(t *testing.T) {
	s := newTestServer(t, testToken, false)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c := client.New(ts.URL, testToken)

	if _, err := c.CreatePlan("plan-e2e-dec", "e2e", ""); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	// ask -> OPEN 落库, id 由 store 生成 (dec- 前缀)。
	d, err := c.AskDecision("plan-e2e-dec", "部署窗口", "选哪个窗口?", []string{"今晚", "明早"}, 0)
	if err != nil {
		t.Fatalf("AskDecision: %v", err)
	}
	if !strings.HasPrefix(d.ID, "dec-") || d.State != "OPEN" || d.TimeoutSec != 1800 {
		t.Fatalf("asked mismatch: %+v", d)
	}

	// list ?state=OPEN 能看到它;get 单查一致。
	open, err := c.ListDecisions("OPEN", "plan-e2e-dec")
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(open) != 1 || open[0].ID != d.ID {
		t.Fatalf("open list mismatch: %+v", open)
	}
	got, err := c.GetDecision(d.ID)
	if err != nil {
		t.Fatalf("GetDecision: %v", err)
	}
	if got.State != "OPEN" || len(got.Options) != 2 || got.Options[1] != "明早" {
		t.Fatalf("get mismatch: %+v", got)
	}

	// answer -> ANSWERED 且带 answer/answered_by/answered_at。
	answered, err := c.AnswerDecision(d.ID, "今晚")
	if err != nil {
		t.Fatalf("AnswerDecision: %v", err)
	}
	if answered.State != "ANSWERED" || answered.Answer != "今晚" ||
		answered.AnsweredBy == "" || answered.AnsweredAt <= 0 {
		t.Fatalf("answered mismatch: %+v", answered)
	}

	// 重复 answer -> 409 错误经 errorFor 上抛。
	if _, err := c.AnswerDecision(d.ID, "改主意"); err == nil ||
		!strings.Contains(err.Error(), "not open") {
		t.Fatalf("re-answer err = %v, want 409 decision-not-open error", err)
	}
	// 不存在的 id -> 404 错误经 errorFor 上抛。
	if _, err := c.AnswerDecision("dec-nope", "x"); err == nil ||
		!strings.Contains(err.Error(), "unknown decision") {
		t.Fatalf("answer unknown err = %v, want 404 unknown-decision error", err)
	}
	if _, err := c.GetDecision("dec-nope"); err == nil ||
		!strings.Contains(err.Error(), "unknown decision") {
		t.Fatalf("get unknown err = %v, want 404 unknown-decision error", err)
	}
	// 悬空 plan_id ask -> 404。
	if _, err := c.AskDecision("plan-nope", "t", "q", nil, 0); err == nil ||
		!strings.Contains(err.Error(), "unknown plan") {
		t.Fatalf("ask dangling plan err = %v, want 404 unknown-plan error", err)
	}

	// plan detail 内联 decisions(additive),ANSWERED 不再出现在 OPEN 列表。
	plan, err := c.GetPlan("plan-e2e-dec")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if len(plan.Decisions) != 1 || plan.Decisions[0].ID != d.ID ||
		plan.Decisions[0].State != "ANSWERED" {
		t.Fatalf("plan detail decisions mismatch: %+v", plan.Decisions)
	}
	open, err = c.ListDecisions("OPEN", "")
	if err != nil {
		t.Fatalf("ListDecisions OPEN: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("OPEN list should be empty after answer: %+v", open)
	}
}
