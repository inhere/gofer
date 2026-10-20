package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/today"
)

// 调用方上限第二期 E3：聚合与计数按范围（gofer-7798，计划 E3、决策 2）。
//
//   - /v1/today（含 handled、lanes、状态条）、/v1/stats、/v1/stats/overview、work-items digest、
//     /v1/meta 对受限调用方只含范围内项目，全局数字（用量、DB、会话、管家）不返回；
//   - /v1/steward 与 digest 的 POST 对受限调用方 403；
//   - 未受限调用方的响应逐字节不变（回归断言）。

// seedReviewJob upserts one job in needs_review, which is what makes a 「今天」 review card.
func seedReviewJob(t *testing.T, s *Server, id, project string) {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: project, Agent: "claude", Runner: "local", Status: "needs_review",
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now,
		CallerID: ceilOwner,
	}); err != nil {
		t.Fatalf("seed review job %s: %v", id, err)
	}
}

// decodeJSON decodes a response body into a generic map.
func decodeJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("response body is not JSON: %q (%v)", body, err)
	}
	return m
}

// cardKeys lists the card keys of a /v1/today body.
func cardKeys(t *testing.T, body string) []string {
	t.Helper()
	m := decodeJSON(t, body)
	raw, _ := m["decisions"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if card, ok := r.(map[string]any); ok {
			if k, ok := card["key"].(string); ok {
				out = append(out, k)
			}
		}
	}
	return out
}

// TestCeilingTodayIsScoped: the Today queue, the handled rows, the lanes and the status bar of a
// restricted caller cover its projects only; the unrestricted caller keeps everything, byte for
// byte the payload the shared builder produces.
func TestCeilingTodayIsScoped(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	seedCeilingWorld(t, s, "p1")
	seedCeilingWorld(t, s, "p2")
	seedReviewJob(t, s, "rev-p1", "p1")
	seedReviewJob(t, s, "rev-p2", "p2")

	// The handled rows carry the card's project, so the scope filter needs no card lookup: record
	// one action of each project directly through the service.
	for _, p := range []struct{ key, project string }{{"review:rev-p1", "p1"}, {"review:rev-p2", "p2"}} {
		if _, err := s.today.RecordAction(today.ActionInput{CardKey: p.key, ActionID: "accept", Project: p.project}, ceilOwner); err != nil {
			t.Fatalf("record today action (%s): %v", p.project, err)
		}
	}

	// Restricted: its own card, its own handled row, and no server-global number at all.
	status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/today", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/today as lim = %d: %s", status, body)
	}
	keys := cardKeys(t, body)
	if !containsStr(keys, "review:rev-p1") {
		t.Fatalf("restricted today cards = %v, want the review card of p1", keys)
	}
	for _, k := range keys {
		if strings.Contains(k, "-p2") {
			t.Fatalf("restricted today cards = %v, want none of p2", keys)
		}
	}
	m := decodeJSON(t, body)
	if v, ok := m["status"].(map[string]any); ok {
		for _, field := range []string{"usage_today", "steward_today"} {
			if got, present := v[field]; present {
				t.Errorf("restricted today status %s = %v, want the field omitted", field, got)
			}
		}
	}

	status, hbody := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/today/handled", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/today/handled as lim = %d: %s", status, hbody)
	}
	handled, _ := decodeJSON(t, hbody)["handled"].([]any)
	if len(handled) != 1 {
		t.Fatalf("restricted handled rows = %d, want the single row of p1: %s", len(handled), hbody)
	}

	status, lbody := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/today/lanes", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/today/lanes as lim = %d: %s", status, lbody)
	}
	lanes, _ := decodeJSON(t, lbody)["lanes"].([]any)
	for _, raw := range lanes {
		if lane, ok := raw.(map[string]any); ok && lane["project_key"] != "p1" {
			t.Errorf("restricted lane of project %v, want only p1", lane["project_key"])
		}
	}

	// Unrestricted: both cards, both handled rows, and the global blocks are present — the
	// service's own answer for the same query, so nothing was filtered on the way.
	status, allBody := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/today", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/today as plain = %d: %s", status, allBody)
	}
	if all := cardKeys(t, allBody); !containsStr(all, "review:rev-p1") || !containsStr(all, "review:rev-p2") {
		t.Fatalf("unrestricted today cards = %v, want both seeded review cards", all)
	}
	plainStatus, _ := decodeJSON(t, allBody)["status"].(map[string]any)
	for _, field := range []string{"usage_today", "steward_today"} {
		if _, present := plainStatus[field]; !present {
			t.Errorf("unrestricted today status lost %s: %v", field, plainStatus)
		}
	}
	_, allHandled := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/today/handled", nil)
	if rows, _ := decodeJSON(t, allHandled)["handled"].([]any); len(rows) != 2 {
		t.Fatalf("unrestricted handled rows = %d, want both", len(rows))
	}
}

// TestCeilingStatsIsScoped: /v1/stats counts the restricted caller's projects, omits every
// server-global block, and leaves the unrestricted payload exactly the shared builder's.
func TestCeilingStatsIsScoped(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	seedCeilingWorld(t, s, "p1")
	seedCeilingWorld(t, s, "p2")

	status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/stats", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/stats as lim = %d: %s", status, body)
	}
	lim := decodeJSON(t, body)
	jobsBlock, _ := lim["jobs"].(map[string]any)
	if jobsBlock == nil || jobsBlock["total"] != float64(1) {
		t.Errorf("restricted stats jobs = %v, want the single job of p1", lim["jobs"])
	}
	if got := lim["projects"]; got != float64(1) {
		t.Errorf("restricted stats projects = %v, want 1 (p1)", got)
	}
	for _, field := range []string{"db", "sessions", "usage", "session_usage"} {
		if got, present := lim[field]; present {
			t.Errorf("restricted stats %s = %v, want the field omitted", field, got)
		}
	}

	// The regression side: the unrestricted handler payload is the shared global builder's, byte
	// for byte, so nothing about it got scoped.
	status, plainBody := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/stats", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/stats as plain = %d: %s", status, plainBody)
	}
	plain := decodeJSON(t, plainBody)
	if jobsBlock, _ := plain["jobs"].(map[string]any); jobsBlock == nil || jobsBlock["total"] != float64(2) {
		t.Errorf("unrestricted stats jobs = %v, want both seeded jobs", plain["jobs"])
	}
	for _, field := range []string{"db", "sessions", "usage", "session_usage"} {
		if _, present := plain[field]; !present {
			t.Errorf("unrestricted stats lost the %s block", field)
		}
	}
	want, serr := s.buildStats()
	if serr != nil {
		t.Fatalf("build stats: %v", serr)
	}
	if want.DB == nil || want.Sessions == nil || want.Usage == nil || want.SessionUsage == nil {
		t.Fatalf("the shared builder lost a server-global block for an unrestricted caller: %+v", want)
	}
	// Everything but the two per-request stamps must be exactly the shared builder's payload.
	if got, wantJSON := withoutStamps(t, plainBody), withoutStamps(t, marshalStats(t, want)); got != wantJSON {
		t.Errorf("unrestricted /v1/stats is no longer the shared builder's payload:\n got %s\nwant %s", got, wantJSON)
	}
}

// marshalStats renders a stats payload the way the handler does.
func marshalStats(t *testing.T, v statsResp) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}
	return string(b)
}

// withoutStamps re-renders a stats body without the two fields that change between two calls
// (server_time, uptime_sec), so the rest can be compared literally.
func withoutStamps(t *testing.T, body string) string {
	t.Helper()
	m := decodeJSON(t, body)
	delete(m, "server_time")
	delete(m, "uptime_sec")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal stats body: %v", err)
	}
	return string(b)
}

// TestCeilingOverviewIsScoped: the statistics wall of a restricted caller covers its own jobs
// only, and the unrestricted caller still sees every project.
func TestCeilingOverviewIsScoped(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	seedCeilingWorld(t, s, "p1")
	seedCeilingWorld(t, s, "p2")

	status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/stats/overview?range=all&tz=0", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/stats/overview as lim = %d: %s", status, body)
	}
	lim := decodeJSON(t, body)
	projects := overviewProjects(t, lim)
	if len(projects) != 1 || projects[0] != "p1" {
		t.Errorf("restricted overview projects = %v, want [p1]", projects)
	}
	if totals, _ := lim["totals"].(map[string]any); totals == nil || totals["jobs"] != float64(1) {
		t.Errorf("restricted overview totals = %v, want the single job of p1", lim["totals"])
	}

	status, plainBody := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/stats/overview?range=all&tz=0", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/stats/overview as plain = %d: %s", status, plainBody)
	}
	all := overviewProjects(t, decodeJSON(t, plainBody))
	if len(all) != 2 || all[0] != "p1" || all[1] != "p2" {
		t.Errorf("unrestricted overview projects = %v, want [p1 p2]", all)
	}
}

// contains reports whether list holds v.
func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// overviewProjects lists the project keys of an overview body.
func overviewProjects(t *testing.T, m map[string]any) []string {
	t.Helper()
	raw, _ := m["projects"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if row, ok := r.(map[string]any); ok {
			if k, ok := row["project"].(string); ok {
				out = append(out, k)
			}
		}
	}
	return out
}

// TestCeilingDigestAndSteward: the digest counts only the restricted caller's work items, sending
// it and reading the steward's status are refused (both are server-global), and the unrestricted
// caller keeps 200 on both.
func TestCeilingDigestAndSteward(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	for _, p := range []string{"p1", "p2"} {
		if _, err := s.jobs.Meta().CreateWorkItem(jobstore.WorkItemInput{
			Title: "needs me " + p, ProjectKey: p, Status: jobstore.WorkNeedsMe, By: ceilOwner,
		}); err != nil {
			t.Fatalf("seed work item (%s): %v", p, err)
		}
	}

	status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/work-items/digest", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/work-items/digest as lim = %d: %s", status, body)
	}
	if got := decodeJSON(t, body)["needs_me"]; got != float64(1) {
		t.Errorf("restricted digest needs_me = %v, want 1 (p1 only): %s", got, body)
	}
	status, plainBody := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/work-items/digest", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/work-items/digest as plain = %d: %s", status, plainBody)
	}
	if got := decodeJSON(t, plainBody)["needs_me"]; got != float64(2) {
		t.Errorf("unrestricted digest needs_me = %v, want both items: %s", got, plainBody)
	}

	if status, body := ceilReq(t, s, ceilTokLim, http.MethodPost, "/v1/work-items/digest", nil); status != http.StatusForbidden {
		t.Errorf("POST /v1/work-items/digest as lim = %d, want 403: %s", status, body)
	}
	if status, body := ceilReq(t, s, ceilTokPlain, http.MethodPost, "/v1/work-items/digest", nil); status != http.StatusOK {
		t.Errorf("POST /v1/work-items/digest as plain = %d, want 200: %s", status, body)
	}

	if status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/steward", nil); status != http.StatusForbidden {
		t.Errorf("GET /v1/steward as lim = %d, want 403: %s", status, body)
	}
	if status, body := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/steward", nil); status == http.StatusForbidden {
		t.Errorf("GET /v1/steward as plain = 403, want the route's own answer: %s", body)
	}
}

// TestCeilingMetaAndStatusBarFilterWorkers: both lists a restricted caller gets are narrowed by the
// ceiling's projects and workers dimensions; the unrestricted caller keeps both.
func TestCeilingMetaAndStatusBarFilterWorkers(t *testing.T) {
	t.Parallel()
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "lim", Token: ceilTokLim, Ceiling: &config.CallerCeiling{Projects: []string{"p1"}, Workers: []string{"w-ok"}}},
			{ID: "plain", Token: ceilTokPlain},
		},
		Workers: map[string]config.WorkerAuthConfig{"w-ok": {}, "w-no": {}},
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

	status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/meta", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/meta as lim = %d: %s", status, body)
	}
	m := decodeJSON(t, body)
	if got := jsonNames(m["projects"], "key"); len(got) != 1 || got[0] != "p1" {
		t.Errorf("restricted meta projects = %v, want [p1]", got)
	}
	if got := jsonNames(m["workers"], "id"); len(got) != 1 || got[0] != "w-ok" {
		t.Errorf("restricted meta workers = %v, want [w-ok]", got)
	}

	status, plainBody := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/meta", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/meta as plain = %d: %s", status, plainBody)
	}
	all := decodeJSON(t, plainBody)
	if got := jsonNames(all["workers"], "id"); len(got) != 2 {
		t.Errorf("unrestricted meta workers = %v, want both", got)
	}
	if got := jsonNames(all["projects"], "key"); len(got) != 3 { // self, p1, p2
		t.Errorf("unrestricted meta projects = %v, want every project", got)
	}

	// The status bar's runner rows follow the ceiling's workers / runners dimensions.
	status, tb := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/today", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/today as lim = %d: %s", status, tb)
	}
	limRunners := decodeJSON(t, tb)["status"].(map[string]any)["runners"].(map[string]any)
	// The ceiling names no runners, so it admits no machine runner: the built-in local runner is
	// not part of this caller's status bar, and w-no is outside its workers dimension.
	if limRunners["total"] != float64(1) {
		t.Errorf("restricted status bar runners = %v, want total 1 (w-ok only)", limRunners)
	}
	if offline := jsonStrings(limRunners["offline"]); len(offline) != 1 || offline[0] != "w-ok" {
		t.Errorf("restricted status bar offline runners = %v, want [w-ok]", offline)
	}
	status, ptb := ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/today", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/today as plain = %d: %s", status, ptb)
	}
	plainRunners := decodeJSON(t, ptb)["status"].(map[string]any)["runners"].(map[string]any)
	if plainRunners["total"] != float64(3) { // local + w-ok + w-no
		t.Errorf("unrestricted status bar runners = %v, want total 3", plainRunners)
	}
}

// jsonNames lists one string field of a JSON array of objects.
func jsonNames(raw any, field string) []string {
	rows, _ := raw.([]any)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			if v, ok := m[field].(string); ok {
				out = append(out, v)
			}
		}
	}
	return out
}

// jsonStrings lists a JSON array of strings.
func jsonStrings(raw any) []string {
	rows, _ := raw.([]any)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if v, ok := r.(string); ok {
			out = append(out, v)
		}
	}
	return out
}
