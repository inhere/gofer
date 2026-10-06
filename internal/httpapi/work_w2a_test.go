package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

type stubOneShot struct {
	out   string
	check error
}

func (f stubOneShot) Check(string) error { return f.check }
func (f stubOneShot) Run(context.Context, work.OneShotRequest) (work.OneShotResult, error) {
	return work.OneShotResult{JobID: "job-stub", Output: f.out}, nil
}

type noTranscript struct{}

func (noTranscript) ReadTail(context.Context, jobstore.AgentSession, int64) ([]byte, error) {
	return nil, work.ErrNoTranscript
}

const stubSummary = `{"goal":"导出订单","progress":"按钮已加","blocker_kind":"person","blocker":"等后端","next":"联调","status_hint":"waiting_resource","confidence":0.7}`

func w2aServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := newTestServer(t, testToken, false)
	s.work.SetOneShot(stubOneShot{out: stubSummary})
	s.work.SetTranscriptSource(noTranscript{})
	registerSession(t, s, "sess-w2a-0001", "/ws/a")
	humanPrompt(t, s, "sess-w2a-0001", "导出订单")
	items, _ := workList(t, s, "")
	return s, items[0].ID
}

func TestWorkSummarizeEndpointFillsFieldsAndSuggestions(t *testing.T) {
	s, id := w2aServer(t)
	// A person already wrote the goal, so the model's goal must come back as a suggestion.
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+id, testToken, map[string]any{"goal": "我写的目标"}, 200)

	resp := do(t, s, http.MethodPost, "/v1/work-items/"+id+"/summarize", testToken, nil)
	var out struct {
		Request jobstore.WorkRequest `json:"request"`
	}
	decode(t, resp, &out)
	if out.Request.Kind != jobstore.WorkRequestSummarize || out.Request.ID == "" {
		t.Fatalf("summarize response = %+v", out)
	}
	s.work.WaitIdle()

	resp = do(t, s, http.MethodGet, "/v1/work-items/"+id, testToken, nil)
	var d struct {
		Goal         string                              `json:"goal"`
		BlockerText  string                              `json:"blocker_text"`
		NextStep     string                              `json:"next_step"`
		Status       string                              `json:"status"`
		FieldSources map[string]jobstore.WorkFieldSource `json:"field_sources"`
		Suggestions  []jobstore.WorkSuggestion           `json:"suggestions"`
		Requests     []jobstore.WorkRequest              `json:"requests"`
	}
	decode(t, resp, &d)
	if d.Goal != "我写的目标" || d.BlockerText != "等后端" || d.NextStep != "联调" || d.Status != jobstore.WorkActive {
		t.Fatalf("detail = %+v", d)
	}
	if d.FieldSources[jobstore.WorkFieldBlocker].By != "summarizer(claude)" || !strings.HasPrefix(d.FieldSources[jobstore.WorkFieldGoal].By, "human:") {
		t.Fatalf("field sources = %+v", d.FieldSources)
	}
	got := map[string]bool{}
	for _, sg := range d.Suggestions {
		got[sg.Field] = true
	}
	if !got[jobstore.SuggestGoal] || !got[jobstore.SuggestStatusHint] || len(d.Requests) != 1 || d.Requests[0].State != jobstore.WorkRequestAnswered {
		t.Fatalf("suggestions=%+v requests=%+v", d.Suggestions, d.Requests)
	}
	if d.Requests[0].Text != "" {
		t.Fatal("the card view must not carry request text")
	}

	// Adopt the goal, dismiss the status hint; both answer with the fresh detail.
	workCall(t, s, http.MethodPost, "/v1/work-items/"+id+"/suggestions/goal/accept", testToken, nil, 200)
	workCall(t, s, http.MethodPost, "/v1/work-items/"+id+"/suggestions/status_hint/dismiss", testToken, nil, 200)
	workCall(t, s, http.MethodPost, "/v1/work-items/"+id+"/suggestions/goal/accept", testToken, nil, 404)
	workCall(t, s, http.MethodPost, "/v1/work-items/"+id+"/suggestions/bogus/dismiss", testToken, nil, 404)
	it := workGet(t, s, "/v1/work-items/"+id)
	if it.Goal != "导出订单" || it.Status != jobstore.WorkActive {
		t.Fatalf("after adopt = %+v", it)
	}

	// The ledger endpoints.
	resp = do(t, s, http.MethodGet, "/v1/work-items/"+id+"/requests", testToken, nil)
	var led struct {
		Requests []jobstore.WorkRequest `json:"requests"`
	}
	decode(t, resp, &led)
	if len(led.Requests) != 1 {
		t.Fatalf("ledger = %+v", led)
	}
	resp = do(t, s, http.MethodGet, "/v1/work-items/requests", testToken, nil)
	decode(t, resp, &led)
	if len(led.Requests) != 1 {
		t.Fatalf("global ledger = %+v", led)
	}
	resp = do(t, s, http.MethodGet, "/v1/work-items/requests?active=1", testToken, nil)
	decode(t, resp, &led)
	if len(led.Requests) != 0 {
		t.Fatalf("active ledger = %+v", led)
	}
}

func TestWorkSummarizeRefusals(t *testing.T) {
	s, id := w2aServer(t)
	s.work.SetOneShot(stubOneShot{check: errors.New("agent claude 不存在")})
	workCall(t, s, http.MethodPost, "/v1/work-items/"+id+"/summarize", testToken, nil, 409)
	st := summarizerStatusOf(t, s)
	if st.Status.Available || !strings.Contains(st.Status.Reason, "不存在") {
		t.Fatalf("status = %+v", st.Status)
	}

	// No session: 409. A job credential may not trigger it.
	s.work.SetOneShot(stubOneShot{out: stubSummary})
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "no session"}, 200)
	workCall(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/summarize", testToken, nil, 409)
	workCall(t, s, http.MethodPost, "/v1/work-items/nope/summarize", testToken, nil, 404)
}

type summarizerResp struct {
	Status   work.SummarizerStatus `json:"status"`
	Settings workSettingsView      `json:"settings"`
}

func summarizerStatusOf(t *testing.T, s *Server) summarizerResp {
	t.Helper()
	var out summarizerResp
	decode(t, do(t, s, http.MethodGet, "/v1/work-items/summarizer", testToken, nil), &out)
	return out
}

func TestWorkSummarizerStatusShowsEffectiveDefaults(t *testing.T) {
	s, _ := w2aServer(t)
	st := summarizerStatusOf(t, s)
	if !st.Status.Available || st.Status.Agent != "claude" || !st.Status.Enabled {
		t.Fatalf("status = %+v", st.Status)
	}
	set := st.Settings
	if set.SummarizeIdleMin != 15 || set.SummarizeMinIntervalMin != 30 || set.SummarizeDailyLimit != 50 ||
		set.RequestTimeoutMin != 30 || !set.AutoHandoff || !set.SummarizeEnabled || set.DigestTime != "09:00" ||
		strings.Join(set.SummarizerArgs, " ") != "--model haiku --tools " {
		t.Fatalf("settings = %+v", set)
	}
}

func TestPutConfigWorkWritesAndHotApplies(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, cr, cfgPath := newConfigWriteTestServer(t, yamlText, fixedDetector{"claude": true, "mytool": true})
	_ = cr
	// Non-admin callers are refused; admin writes a partial block.
	resp := do(t, s, http.MethodPut, "/v1/config/work", opToken, map[string]any{"summarize_idle_min": 5})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin = %d", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPut, "/v1/config/work", adminToken, map[string]any{
		"summarizer_agent": "mytool", "summarizer_args": []string{"--fast"}, "summarize_enabled": false,
		"summarize_idle_min": 5, "summarize_daily_limit": -1, "auto_handoff": false, "digest_time": "08:30",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("admin put = %d", resp.StatusCode)
	}
	resp.Body.Close()
	live := cr.Config().Work
	if live.SummarizerAgentName() != "mytool" || live.SummarizeOn() || live.SummarizeIdleMin != 5 || live.SummarizeDaily() != 0 || live.AutoHandoffOn() {
		t.Fatalf("live work config = %+v", live)
	}
	if h, m := live.DigestClock(); h != 8 || m != 30 {
		t.Fatalf("digest clock = %d:%d", h, m)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil || !strings.Contains(string(raw), "summarizer_agent: mytool") {
		t.Fatalf("config file lacks the work block: %v\n%s", err, raw)
	}

	// Validation: a negative threshold and an empty body are 400s.
	for _, body := range []map[string]any{{"summarize_idle_min": -3}, {}} {
		resp = do(t, s, http.MethodPut, "/v1/config/work", adminToken, body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("body %v = %d, want 400", body, resp.StatusCode)
		}
	}
	// The write is only partial: untouched fields keep their values.
	resp = do(t, s, http.MethodPut, "/v1/config/work", adminToken, map[string]any{"request_timeout_min": 10})
	resp.Body.Close()
	live = cr.Config().Work
	if live.SummarizerAgentName() != "mytool" || live.RequestTimeoutMin != 10 {
		t.Fatalf("partial write lost fields: %+v", live)
	}
	_ = agent.TypeCLIAgent
}

func TestWorkSummarizeAndSuggestionsRefuseJobCredential(t *testing.T) {
	s, id := w2aServer(t)
	_ = id
	// A route a job credential may not reach is refused before any handler runs.
	for _, p := range []string{"/summarize", "/suggestions/goal/accept", "/suggestions/goal/dismiss"} {
		if jobWriteAllowlist[jobRouteKey(http.MethodPost, "/v1/work-items/"+id+p)] {
			t.Fatalf("POST %s must not be open to job credentials", p)
		}
	}
	_ = s
}

func TestWorkOneShotCheckExplainsWhyAnAgentCannotSummarize(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"cheap":    {Type: agent.TypeCLIAgent, Command: "cheap", Args: []string{"{{prompt}}"}, ReadOnlyArgs: []string{"--ro"}},
		"no-ro":    {Type: agent.TypeCLIAgent, Command: "x", Args: []string{"{{prompt}}"}},
		"acp-only": {Type: agent.TypeACPAgent, Command: "x"},
		"gone":     {Type: agent.TypeCLIAgent, Command: "gone", Args: []string{"{{prompt}}"}, ReadOnlyArgs: []string{"--ro"}},
	}}
	reg := agent.NewRegistryWith(cfg, map[string]agent.DetectResult{
		"cheap": {Available: true}, "no-ro": {Available: true}, "acp-only": {Available: true}, "gone": {Available: false, Error: "not on PATH"},
	})
	o := workOneShot{agents: reg}
	if err := o.Check("cheap"); err != nil {
		t.Fatalf("usable agent refused: %v", err)
	}
	for agentKey, want := range map[string]string{
		"missing": "不存在", "acp-only": "不是 cli-agent", "no-ro": "只读模式", "gone": "not on PATH",
	} {
		err := o.Check(agentKey)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Check(%q) = %v, want it to mention %q", agentKey, err, want)
		}
	}
	if err := (workOneShot{}).Check("cheap"); err == nil {
		t.Fatal("no registry must be an error")
	}
}
