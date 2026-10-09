package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestTodayAdviceEndpoint(t *testing.T) {
	s := newTestServer(t, testToken, false)
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "j-adv", ProjectKey: "self", Agent: "codex", Status: job.StatusNeedsReview,
		RequestJSON: `{"title":"导出改造"}`, StartedAt: now - 600, UpdatedAt: now - 60, EndedAt: now - 60}); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"card_key": "review:j-adv", "text": "改动小，verify 通过", "action_id": "accept", "digest": "改动：导出\n风险：低"}

	// Plain job credentials (member / leader) may not advise; the steward and a person may.
	for _, kind := range []string{jobstore.JobCredentialMember, jobstore.JobCredentialLeader} {
		tok := seedJobToken(t, s, "job-adv-"+kind, kind, "")
		todayCall(t, s, http.MethodPost, "/v1/today/advice", tok, body, 403, nil)
	}
	steward := seedJobToken(t, s, "job-adv-steward", jobstore.JobCredentialSteward, "")
	todayCall(t, s, http.MethodGet, "/v1/today", steward, nil, 200, nil)
	var out struct {
		CardKey string `json:"card_key"`
		Advice  struct {
			Text     string `json:"text"`
			ActionID string `json:"action_id"`
			By       string `json:"by"`
		} `json:"advice"`
	}
	todayCall(t, s, http.MethodPost, "/v1/today/advice", steward, body, 200, &out)
	if out.CardKey != "review:j-adv" || out.Advice.ActionID != "accept" || !strings.HasPrefix(out.Advice.By, "steward") {
		t.Fatalf("advice = %+v", out)
	}
	got, ok, _ := s.jobs.Meta().GetDecisionAdvice("review:j-adv")
	if !ok || got.JobID != "job-adv-steward" {
		t.Fatalf("stored = %+v ok=%v", got, ok)
	}

	// Validation.
	todayCall(t, s, http.MethodPost, "/v1/today/advice", testToken, map[string]any{"card_key": "review:nope", "text": "x"}, 404, nil)
	todayCall(t, s, http.MethodPost, "/v1/today/advice", testToken, map[string]any{"card_key": "review:j-adv", "text": "x", "action_id": "diff"}, 400, nil)
	todayCall(t, s, http.MethodPost, "/v1/today/advice", testToken, map[string]any{"card_key": "review:j-adv", "text": strings.Repeat("字", 61)}, 400, nil)

	var raw struct {
		Decisions []struct {
			Key    string `json:"key"`
			Review struct {
				Digest string `json:"digest"`
			} `json:"review"`
			Advice *struct {
				Text     string `json:"text"`
				ActionID string `json:"action_id"`
				Digest   string `json:"digest"`
				At       int64  `json:"at"`
			} `json:"advice"`
		} `json:"decisions"`
		Status struct {
			StewardToday struct {
				Advice int `json:"advice"`
			} `json:"steward_today"`
		} `json:"status"`
	}
	todayCall(t, s, http.MethodGet, "/v1/today", testToken, nil, 200, &raw)
	if len(raw.Decisions) != 1 || raw.Decisions[0].Advice == nil || raw.Decisions[0].Advice.ActionID != "accept" ||
		raw.Decisions[0].Advice.Digest != "改动：导出\n风险：低" || raw.Decisions[0].Review.Digest != "改动：导出\n风险：低" ||
		raw.Status.StewardToday.Advice != 1 {
		t.Fatalf("today = %+v", raw)
	}

	// A person may advise too (it replaces the steward's; the status bar counts only the steward's).
	todayCall(t, s, http.MethodPost, "/v1/today/advice", testToken, map[string]any{"card_key": "review:j-adv", "text": "我来看", "action_id": "rerun"}, 200, nil)
	if got, _, _ := s.jobs.Meta().GetDecisionAdvice("review:j-adv"); got.ActionID != "rerun" || !strings.HasPrefix(got.By, "human") {
		t.Fatalf("person advice = %+v", got)
	}
}
