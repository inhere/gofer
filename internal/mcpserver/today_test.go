package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestStewardTodayToolsListReadAndAdvise(t *testing.T) {
	call, meta, _ := newStewardSession(t)
	now := time.Now().Unix()
	if err := meta.UpsertJob(jobstore.JobRecord{ID: "j-today", ProjectKey: "self", Agent: "codex", Status: job.StatusNeedsReview,
		RequestJSON: `{"title":"导出改造"}`, StartedAt: now - 600, UpdatedAt: now - 60, EndedAt: now - 60,
		ResultJSON: `{"summary":"改为逐行写出"}`, DiffSummary: " 2 files changed, 10 insertions(+), 2 deletions(-)",
		CommitsJSON: `[{"sha":"abc","subject":"feat: stream export"}]`}); err != nil {
		t.Fatal(err)
	}
	if err := meta.InsertDecision(&jobstore.PlanDecision{ID: "d-today", Title: "分页", Question: "A 还是 B？", OptionsJSON: `["A","B"]`,
		AskedAt: now - 60, TimeoutSec: 86400}); err != nil {
		t.Fatal(err)
	}

	isErr, out, text := call("gofer_today_list", map[string]any{"unadvised": true})
	if isErr {
		t.Fatalf("today_list: %s", text)
	}
	cards, _ := out["cards"].([]any)
	if len(cards) != 2 {
		t.Fatalf("today_list = %v", out)
	}
	isErr, out, text = call("gofer_today_list", map[string]any{"kind": "review"})
	if cards, _ = out["cards"].([]any); isErr || len(cards) != 1 {
		t.Fatalf("today_list review = %v %s", out, text)
	}
	rev := cards[0].(map[string]any)
	acts, _ := rev["actions"].([]any)
	if rev["key"] != "review:j-today" || len(acts) != 3 || acts[0].(map[string]any)["id"] != "accept" || acts[2].(map[string]any)["advisable"] != false {
		t.Fatalf("review card = %v", rev)
	}

	isErr, out, text = call("gofer_today_card", map[string]any{"card_key": "review:j-today"})
	if isErr {
		t.Fatalf("today_card: %s", text)
	}
	jd, _ := out["job"].(map[string]any)
	if jd == nil || jd["report"] != "改为逐行写出" || !strings.Contains(jd["diff_stat"].(string), "10 insertions") {
		t.Fatalf("today_card = %v", out)
	}

	// Validation errors come back as tool errors.
	if isErr, _, text = call("gofer_today_advise", map[string]any{"card_key": "decision:d-today", "text": "选 A", "action_id": "answer:A"}); !isErr {
		t.Fatalf("decision pick accepted: %s", text)
	}
	if isErr, _, _ = call("gofer_today_advise", map[string]any{"card_key": "review:j-today", "text": strings.Repeat("长", 61)}); !isErr {
		t.Fatal("long text accepted")
	}
	isErr, out, text = call("gofer_today_advise", map[string]any{"card_key": "review:j-today", "text": "改动小，可通过", "action_id": "accept",
		"digest": "改动：导出逐行写\n风险：低\n测试：无新增"})
	if isErr {
		t.Fatalf("today_advise: %s", text)
	}
	if adv, _ := out["advice"].(map[string]any); adv["action_id"] != "accept" || !strings.HasPrefix(adv["by"].(string), "steward") {
		t.Fatalf("advise = %v", out)
	}
	if isErr, _, text = call("gofer_today_advise", map[string]any{"card_key": "decision:d-today", "text": "B 与现有接口一致"}); isErr {
		t.Fatalf("decision background: %s", text)
	}
	if _, out, _ = call("gofer_today_list", map[string]any{"unadvised": true}); len(out["cards"].([]any)) != 0 {
		t.Fatalf("still unadvised: %v", out)
	}
}
