package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// 调用方上限第二期 E3 的收尾（gofer-7798，计划 E3、决策 2）：
//
//   - /v1/today 的 digest（title / text 与 since_last 计数）只基于范围内的工作项 / job；
//   - GET /v1/today/snoozed 按范围过滤；
//   - 其余 steward 路由对受限调用方与 GET /v1/steward 一样 403；
//   - /v1/meta 的 runner 列表按上限的 runners 维度过滤。
//
// 未受限调用方的响应不变（回归断言）。

// seedNeedsMeItem creates one work item of `project` in the needs_me state, so the digest's
// 「等我 N」 line counts it.
func seedNeedsMeItem(t *testing.T, s *Server, project, title string) {
	t.Helper()
	wi, err := s.jobs.Meta().CreateWorkItem(jobstore.WorkItemInput{Title: title, ProjectKey: project, By: ceilOwner})
	if err != nil {
		t.Fatalf("create work item %s: %v", title, err)
	}
	status := jobstore.WorkNeedsMe
	if _, _, err := s.jobs.Meta().UpdateWorkItem(wi.ID, jobstore.WorkItemPatch{Status: &status}, wi.Rev, ceilOwner); err != nil {
		t.Fatalf("needs_me %s: %v", title, err)
	}
}

// TestCeilingTodayDigestIsScoped: the restricted caller's digest counts only its own jobs and
// sums only its own work items; the unrestricted caller keeps the whole-server digest.
func TestCeilingTodayDigestIsScoped(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	seedCeilingWorld(t, s, "p1") // one done job of p1
	seedCeilingWorld(t, s, "p2") // one done job of p2
	seedNeedsMeItem(t, s, "p1", "waiting-p1-item")
	seedNeedsMeItem(t, s, "p2", "waiting-p2-item")

	digestOf := func(token string) (map[string]any, map[string]any) {
		t.Helper()
		status, body := ceilReq(t, s, token, http.MethodGet, "/v1/today", nil)
		if status != http.StatusOK {
			t.Fatalf("GET /v1/today as %s = %d: %s", token, status, body)
		}
		digest, _ := decodeJSON(t, body)["digest"].(map[string]any)
		sinceLast, _ := digest["since_last"].(map[string]any)
		return digest, sinceLast
	}

	digest, sinceLast := digestOf(ceilTokLim)
	if sinceLast["jobs_done"] != float64(1) {
		t.Errorf("restricted since_last = %v, want 1 done job (p1 only)", sinceLast)
	}
	text, _ := digest["text"].(string)
	if !strings.Contains(text, "等我 1 ") {
		t.Errorf("restricted digest text = %q, want the 「等我 1」 line of p1 only", text)
	}

	_, plainSinceLast := digestOf(ceilTokPlain)
	if plainSinceLast["jobs_done"] != float64(2) {
		t.Errorf("unrestricted since_last = %v, want both done jobs", plainSinceLast)
	}
	plainText := func() string {
		_, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/today", nil)
		d, _ := decodeJSON(t, body)["digest"].(map[string]any)
		v, _ := d["text"].(string)
		return v
	}()
	if !strings.Contains(plainText, "等我 2 ") {
		t.Errorf("unrestricted digest text = %q, want the 「等我 2」 line", plainText)
	}
}

// TestCeilingTodaySnoozedIsScoped: GET /v1/today/snoozed of a restricted caller lists only the
// snoozed cards of its own projects.
func TestCeilingTodaySnoozedIsScoped(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	seedReviewJob(t, s, "rev-p1", "p1")
	seedReviewJob(t, s, "rev-p2", "p2")
	until := time.Now().Add(time.Hour).Unix()
	for _, key := range []string{"review:rev-p1", "review:rev-p2"} {
		if _, err := s.today.Snooze(today.SnoozeInput{CardKey: key, UntilAt: until}, ceilOwner); err != nil {
			t.Fatalf("snooze %s: %v", key, err)
		}
	}

	snoozedKeys := func(token string) []string {
		t.Helper()
		status, body := ceilReq(t, s, token, http.MethodGet, "/v1/today/snoozed", nil)
		if status != http.StatusOK {
			t.Fatalf("GET /v1/today/snoozed as %s = %d: %s", token, status, body)
		}
		return jsonNames(decodeJSON(t, body)["snoozed"], "card_key")
	}

	if got := snoozedKeys(ceilTokLim); len(got) != 1 || got[0] != "review:rev-p1" {
		t.Errorf("restricted snoozed = %v, want [review:rev-p1]", got)
	}
	if got := snoozedKeys(ceilTokPlain); len(got) != 2 {
		t.Errorf("unrestricted snoozed = %v, want both rows", got)
	}
}

// TestCeilingStewardRoutesRefuseRestrictedCaller: every steward route is server-global, so a
// caller with a project ceiling is refused there like it is on GET /v1/steward. An unrestricted
// caller keeps the route's own answer (never a 403 from this rule).
func TestCeilingStewardRoutesRefuseRestrictedCaller(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/steward/notes"},
		{http.MethodPut, "/v1/steward/notes"},
		{http.MethodPost, "/v1/steward/review-summary"},
		{http.MethodPost, "/v1/steward/ask"},
		{http.MethodPost, "/v1/steward/review"},
		{http.MethodPost, "/v1/steward/start"},
		{http.MethodPost, "/v1/steward/stop"},
		{http.MethodPost, "/v1/steward/restart"},
	} {
		if status, body := ceilReq(t, s, ceilTokLim, rt.method, rt.path, map[string]any{}); status != http.StatusForbidden {
			t.Errorf("%s %s as lim = %d, want 403: %s", rt.method, rt.path, status, body)
		}
		if status, body := ceilReq(t, s, ceilTokPlain, rt.method, rt.path, map[string]any{}); status == http.StatusForbidden {
			t.Errorf("%s %s as plain = 403, want the route's own answer: %s", rt.method, rt.path, body)
		}
	}
}

// TestCeilingMetaFiltersRunners: the /v1/meta runner list follows the ceiling's runners
// dimension, like the today status bar does; the unrestricted caller keeps both runners.
func TestCeilingMetaFiltersRunners(t *testing.T) {
	t.Parallel()
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "lim", Token: ceilTokLim, Ceiling: &config.CallerCeiling{Projects: []string{"p1"}, Runners: []string{"local"}}},
			{ID: "plain", Token: ceilTokPlain},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}, func(cfg *config.Config) {
		for _, p := range []string{"p1", "p2"} {
			cfg.Projects[p] = config.ProjectConfig{
				HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"},
				AllowedRunners: []string{"local"}, AllowExec: true,
			}
		}
	})
	grantAll(s)
	// The peer runner a ceiling can filter out; SetRunners is what serve calls on a reload.
	s.SetRunners(map[string]config.RunnerConfig{"peer": {Type: "peer-http", BaseURL: "http://peer.example.test"}})

	status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/meta", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/meta as lim = %d: %s", status, body)
	}
	if got := jsonNames(decodeJSON(t, body)["runners"], "name"); len(got) != 1 || got[0] != "local" {
		t.Errorf("restricted meta runners = %v, want [local]", got)
	}

	status, plainBody := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/meta", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/meta as plain = %d: %s", status, plainBody)
	}
	if got := jsonNames(decodeJSON(t, plainBody)["runners"], "name"); len(got) != 2 {
		t.Errorf("unrestricted meta runners = %v, want both", got)
	}
}
