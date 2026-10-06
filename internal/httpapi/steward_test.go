package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newStewardServer is a server with two fake acp-agents (acpa, acpb: they echo the prompt
// they got and print their environment), the built-in default project, and the steward
// switched on with acpa.
func newStewardServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	opts := acptest.Options{EchoPrompt: true, EnvPrint: []string{job.EnvJobToken, "GOFER_STEWARD", "GOFER_SERVER_ADDR"}}
	acp := func() config.AgentConfig {
		return config.AgentConfig{Type: agent.TypeACPAgent, Command: testcmd.Path(t), Args: acptest.CmdArgs(opts)}
	}
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken, Addr: "127.0.0.1:18765"},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			config.DefaultProjectKey: {HostPath: root, AllowedAgents: []string{"acpa", "acpb"}, AllowedRunners: []string{"local"}},
		},
		Agents:  map[string]config.AgentConfig{"acpa": acp(), "acpb": acp()},
		Steward: config.StewardConfig{Enabled: true, Agent: "acpa"},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New(), acprunner.Name: acprunner.New()}
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, testToken, false, jobs, eng, projects, agents, nil, nil, nil, nil)
	s.Steward().SetConfigFn(func() (config.StewardConfig, config.WorkConfig) {
		c := projects.Config()
		return c.Steward, c.Work
	})
	t.Cleanup(s.Steward().WaitIdle)
	return s
}

type stewardStatusResp struct {
	Status struct {
		Enabled      bool   `json:"enabled"`
		Agent        string `json:"agent"`
		State        string `json:"state"`
		JobID        string `json:"job_id"`
		JobAgent     string `json:"job_agent"`
		NotesVersion int    `json:"notes_version"`
		TurnNo       int    `json:"turn_no"`
		LastReview   *struct {
			State string `json:"state"`
		} `json:"last_review"`
	} `json:"status"`
	Settings stewardSettingsView `json:"settings"`
}

func stewardStatus(t *testing.T, s *Server) stewardStatusResp {
	t.Helper()
	var out stewardStatusResp
	resp := do(t, s, http.MethodGet, "/v1/steward", testToken, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /v1/steward = %d", resp.StatusCode)
	}
	decode(t, resp, &out)
	return out
}

func waitStewardState(t *testing.T, s *Server, want string) stewardStatusResp {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var st stewardStatusResp
	for time.Now().Before(deadline) {
		st = stewardStatus(t, s)
		if st.Status.State == want {
			return st
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("steward never reached %q: %+v", want, st.Status)
	return st
}

// waitStewardTurn waits until the steward finished at least `turn` turns and is idle again.
func waitStewardTurn(t *testing.T, s *Server, turn int) stewardStatusResp {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var st stewardStatusResp
	for time.Now().Before(deadline) {
		st = stewardStatus(t, s)
		if st.Status.State == "idle" && st.Status.TurnNo >= turn {
			return st
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("steward never finished turn %d: %+v", turn, st.Status)
	return st
}

func askSteward(t *testing.T, s *Server, text string) (jobID string, started bool) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/steward/ask", testToken, map[string]any{"text": text})
	if resp.StatusCode != 200 {
		var raw map[string]any
		decode(t, resp, &raw)
		t.Fatalf("ask = %d: %v", resp.StatusCode, raw)
	}
	var out struct {
		JobID   string `json:"job_id"`
		Started bool   `json:"started"`
	}
	decode(t, resp, &out)
	return out.JobID, out.Started
}

func jobLog(t *testing.T, s *Server, id, name string) string {
	t.Helper()
	r, ok := s.jobs.Get(id)
	if !ok {
		t.Fatalf("job %s missing", id)
	}
	b, err := os.ReadFile(filepath.Join(r.ResultDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// stewardTokenOf reads the steward credential out of the session's environment (the fake
// agent prints it), so the test can call the API exactly as the steward would.
func stewardTokenOf(t *testing.T, s *Server, jobID string) string {
	t.Helper()
	for _, line := range strings.Split(jobLog(t, s, jobID, store.StderrFile), "\n") {
		if v, ok := strings.CutPrefix(line, "acptest: env "+job.EnvJobToken+"="); ok && v != "" {
			return v
		}
	}
	t.Fatalf("no steward credential in the session env: %s", jobLog(t, s, jobID, store.StderrFile))
	return ""
}

func TestStewardAskStartsASessionAndAnswersFromTheDatabase(t *testing.T) {
	s := newStewardServer(t)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "换门禁设备", "next_step": "周三去现场"}, 200)

	if st := stewardStatus(t, s); st.Status.State != "not_started" || !st.Status.Enabled || st.Status.Agent != "acpa" {
		t.Fatalf("initial status = %+v", st.Status)
	}
	id, started := askSteward(t, s, "我手上还有什么没完成？")
	if !started {
		t.Fatal("the first question must open the session")
	}
	st := waitStewardState(t, s, "idle")
	if st.Status.JobID != id || st.Status.JobAgent != "acpa" {
		t.Fatalf("status = %+v", st.Status)
	}
	out := jobLog(t, s, id, store.StdoutFile)
	for _, want := range []string{"ECHO:", "# 你是工作管家", "我手上还有什么没完成？", w.ID, "换门禁设备", "周三去现场"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the steward's first turn lacks %q:\n%s", want, out)
		}
	}
	// The session carries the steward tag and is a normal job row (Sessions can show it).
	r, _ := s.jobs.Get(id)
	if !hasTag(r.Tags, job.StewardTag) || !r.Session {
		t.Fatalf("steward job = %+v", r)
	}

	// A second question goes to the SAME live session.
	id2, started2 := askSteward(t, s, "今天去现场要做什么？")
	if started2 || id2 != id {
		t.Fatalf("second ask opened a new session: %s started=%v", id2, started2)
	}
	waitStewardTurn(t, s, 2)
	if out := jobLog(t, s, id, store.StdoutFile); !strings.Contains(out, "今天去现场要做什么？") {
		t.Fatalf("second question never reached the session:\n%s", out)
	}

	// Stop ends it; the next question rebuilds a fresh one.
	resp := do(t, s, http.MethodPost, "/v1/steward/stop", testToken, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stop = %d", resp.StatusCode)
	}
	if st := stewardStatus(t, s); st.Status.State != "not_started" {
		t.Fatalf("after stop = %+v", st.Status)
	}
	id3, started3 := askSteward(t, s, "再来一次")
	if !started3 || id3 == id {
		t.Fatalf("rebuild: job %s started=%v", id3, started3)
	}
	waitStewardState(t, s, "idle")
}

func TestStewardAskRefusedWhenDisabledOrWithoutAgent(t *testing.T) {
	s := newStewardServer(t)
	cfg := s.projects.Config()
	cfg.Steward.Enabled = false
	resp := do(t, s, http.MethodPost, "/v1/steward/ask", testToken, map[string]any{"text": "在吗"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("disabled ask = %d, want 409", resp.StatusCode)
	}
	cfg.Steward.Enabled, cfg.Steward.Agent = true, ""
	resp = do(t, s, http.MethodPost, "/v1/steward/ask", testToken, map[string]any{"text": "在吗"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("no-agent ask = %d, want 409", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPost, "/v1/steward/ask", testToken, map[string]any{"text": " "})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusConflict {
		t.Fatalf("empty ask = %d", resp.StatusCode)
	}
}

func TestStewardAgentSwitchEndsTheOldSessionAndRebuildsOnTheNewAgent(t *testing.T) {
	s := newStewardServer(t)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "设备报修"}, 200)
	// An in-flight request and a note: both must be recalled by the new agent's prime.
	req, err := s.jobs.Meta().CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: w.ID, Kind: jobstore.WorkRequestReport, By: "steward(acpa)"})
	if err != nil {
		t.Fatal(err)
	}
	resp := do(t, s, http.MethodPut, "/v1/steward/notes", testToken, map[string]any{"body": "- 设备找老王\n", "version": 0})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put notes = %d", resp.StatusCode)
	}

	a, _ := askSteward(t, s, "你好")
	waitStewardState(t, s, "idle")

	s.projects.Config().Steward.Agent = "acpb"
	s.Steward().Reconcile()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if r, _ := s.jobs.Get(a); job.IsTerminal(r.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the old steward session did not end after the agent switch")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if st := stewardStatus(t, s); st.Status.State != "not_started" || st.Status.Agent != "acpb" {
		t.Fatalf("after switch = %+v", st.Status)
	}

	b, started := askSteward(t, s, "在途的请求有哪些？")
	if !started || b == a {
		t.Fatalf("new session: %s started=%v", b, started)
	}
	st := waitStewardState(t, s, "idle")
	if st.Status.JobAgent != "acpb" {
		t.Fatalf("job agent = %q, want acpb", st.Status.JobAgent)
	}
	out := jobLog(t, s, b, store.StdoutFile)
	for _, want := range []string{"设备找老王", req.ID, "汇报请求", "待发送", w.ID} {
		if !strings.Contains(out, want) {
			t.Fatalf("the new agent's prime lacks %q:\n%s", want, out)
		}
	}
}

// What the steward's own credential can and cannot do, end to end: the token comes out of
// the real session's environment, the routes are the real ones.
func TestStewardCredentialIsConfinedByTheServer(t *testing.T) {
	s := newStewardServer(t)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "管家只能整理"}, 200)
	other := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "另一件"}, 200)
	id, _ := askSteward(t, s, "hi")
	waitStewardState(t, s, "idle")
	tok := stewardTokenOf(t, s, id)
	if !strings.Contains(jobLog(t, s, id, store.StderrFile), "acptest: env GOFER_STEWARD=1") {
		t.Fatal("steward marker env missing")
	}

	// Allowed: reads, descriptive writes, notes, a journal line, a merge suggestion.
	for _, p := range []string{"/v1/work-items", "/v1/work-items/" + w.ID, "/v1/work-items/" + w.ID + "/journal",
		"/v1/work-items/requests", "/v1/sessions", "/v1/jobs", "/v1/jobs/" + id, "/v1/steward", "/v1/steward/notes"} {
		resp := do(t, s, http.MethodGet, p, tok, nil)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("steward GET %s = %d, want 200", p, resp.StatusCode)
		}
	}
	got := workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, tok, map[string]any{"goal": "把设备换掉", "next_step": "周三带备件"}, 200)
	if got.Goal != "把设备换掉" || got.NextStep != "周三带备件" {
		t.Fatalf("descriptive patch lost: %+v", got)
	}
	var withNotes struct {
		Status string   `json:"status"`
		Notes  []string `json:"notes"`
	}
	resp := do(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, tok, map[string]any{"status": "waiting_resource"})
	decode(t, resp, &withNotes)
	if withNotes.Status != "waiting_resource" {
		t.Fatalf("a non-final status the steward may set: %+v", withNotes)
	}
	resp = do(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/journal", tok, map[string]any{"text": "已记录：等备件"})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("steward journal = %d", resp.StatusCode)
	}
	d := workGet(t, s, "/v1/work-items/"+w.ID)
	var sawNote, sawGoalBy bool
	for _, e := range d.Journal {
		if e.Kind == "steward" && strings.Contains(e.Text, "已记录：等备件") && e.By == "steward(acpa)" {
			sawNote = true
		}
		if strings.Contains(e.Text, "下一步") && e.By == "steward(acpa)" {
			sawGoalBy = true
		}
	}
	if !sawNote || !sawGoalBy {
		t.Fatalf("the steward's writes must be labelled steward(acpa): %+v", d.Journal)
	}
	resp = do(t, s, http.MethodPut, "/v1/steward/notes", tok, map[string]any{"body": "- 备件周三到\n", "version": 0})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("steward notes = %d", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/merge-suggestions", tok, map[string]any{"source_id": other.ID, "reason": "同一批设备"})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("steward merge suggestion = %d", resp.StatusCode)
	}

	// Refused by the credential itself, before any handler runs.
	refused := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/jobs", map[string]any{"project_key": "default", "agent": "acpa", "prompt": "do work"}},
		{http.MethodPut, "/v1/config/steward", map[string]any{"enabled": false}},
		{http.MethodPut, "/v1/config/work", map[string]any{"digest_time": "07:00"}},
		{http.MethodPut, "/v1/config/server", map[string]any{}},
		{http.MethodPost, "/v1/projects", map[string]any{}},
		{http.MethodPost, "/v1/steward/ask", map[string]any{"text": "自己问自己"}},
		{http.MethodPost, "/v1/steward/stop", nil},
		{http.MethodPost, "/v1/steward/review", nil},
		{http.MethodPost, "/v1/work-items", map[string]any{"title": "新建不行"}},
		{http.MethodPost, "/v1/work-items/" + w.ID + "/merge", map[string]any{"source_ids": []string{other.ID}}},
		{http.MethodPost, "/v1/work-items/" + w.ID + "/split", map[string]any{}},
		{http.MethodPost, "/v1/work-items/" + w.ID + "/report", map[string]any{"status": "done"}},
		{http.MethodPost, "/v1/work-items/merge-suggestions/1/accept", nil},
		{http.MethodPost, "/v1/work-items/" + w.ID + "/sessions", map[string]any{"session_id": "x"}},
		{http.MethodPost, "/v1/jobs/" + id + "/cancel", nil},
		{http.MethodPost, "/v1/jobs/" + id + "/accept", nil},
		{http.MethodPost, "/v1/plans", map[string]any{}},
		{http.MethodPost, "/v1/schedules", map[string]any{}},
		{http.MethodDelete, "/v1/sessions/whatever", nil},
		{http.MethodGet, "/v1/config", nil},
		{http.MethodGet, "/v1/agents", nil},
		{http.MethodGet, "/v1/jobs/" + id + "/logs/stdout", nil},
		{http.MethodGet, "/v1/jobs/" + id + "/logs/stderr", nil},
		{http.MethodGet, "/v1/jobs/" + id + "/events", nil},
		{http.MethodGet, "/v1/jobs/" + id + "/request", nil},
		{http.MethodGet, "/v1/work-items/digest", nil},
		{http.MethodGet, "/v1/projects", nil},
	}
	for _, r := range refused {
		resp := do(t, s, r.method, r.path, tok, r.body)
		var body map[string]any
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		_ = json.Unmarshal(raw, &body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("steward %s %s = %d, want 403 (%v)", r.method, r.path, resp.StatusCode, body)
			continue
		}
		if msg, _ := body["error"].(string); !strings.Contains(msg, "steward credential may not") {
			t.Errorf("steward %s %s refused with %q", r.method, r.path, msg)
		}
	}

	// Final statuses are refused by the handler; the item is untouched.
	for _, final := range []string{"done", "dropped"} {
		resp := do(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, tok, map[string]any{"status": final})
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("steward status=%s = %d, want 403", final, resp.StatusCode)
		}
	}
	resp = do(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, tok, map[string]any{"status_source": "auto"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("steward status_source = %d, want 403", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, tok, map[string]any{"status_source": "human"})
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("steward status_source=human = %d, want a refusal", resp.StatusCode)
	}
	if cur := workGet(t, s, "/v1/work-items/"+w.ID); cur.Status != "waiting_resource" || cur.MergedInto != "" {
		t.Fatalf("the item must be untouched by refused calls: %+v", cur)
	}
	if other := workGet(t, s, "/v1/work-items/"+other.ID); other.MergedInto != "" {
		t.Fatalf("a suggestion merged something: %+v", other)
	}

	// A person's status wins over the steward's.
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"status": "needs_onsite"}, 200)
	resp = do(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, tok, map[string]any{"status": "waiting_resource", "next_step": "仍然生效"})
	decode(t, resp, &withNotes)
	if withNotes.Status != "needs_onsite" || len(withNotes.Notes) != 1 {
		t.Fatalf("a person's status must win and say so: %+v", withNotes)
	}

	// The steward credential dies with its session.
	resp = do(t, s, http.MethodPost, "/v1/steward/stop", testToken, nil)
	resp.Body.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := do(t, s, http.MethodGet, "/v1/work-items", tok, nil)
		r.Body.Close()
		if r.StatusCode == http.StatusUnauthorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("steward credential still works after its session ended (%d)", r.StatusCode)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// The other credential kinds are not widened by the steward: a member job still cannot ask
// a session to report, and cannot reach the steward's write routes.
func TestMemberAndLeaderCredentialsCannotUseTheStewardSurface(t *testing.T) {
	s := newStewardServer(t)
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "x"}, 200)
	member := seedJobToken(t, s, "member-1", jobstore.JobCredentialMember, "")
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/v1/steward/ask"}, {http.MethodPut, "/v1/steward/notes"}, {http.MethodPost, "/v1/steward/review-summary"},
		{http.MethodPost, "/v1/work-items/" + w.ID + "/merge-suggestions"}, {http.MethodPost, "/v1/work-items/" + w.ID + "/report-request"},
		{http.MethodPatch, "/v1/work-items/" + w.ID}, {http.MethodPut, "/v1/config/steward"},
	} {
		resp := do(t, s, r.method, r.path, member, map[string]any{})
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("member %s %s = %d, want 403", r.method, r.path, resp.StatusCode)
		}
	}
}

func TestStewardNotesRESTVersionsAndConflict(t *testing.T) {
	s := newStewardServer(t)
	put := func(body string, version int, want int) {
		t.Helper()
		resp := do(t, s, http.MethodPut, "/v1/steward/notes", testToken, map[string]any{"body": body, "version": version})
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("PUT notes v%d = %d, want %d", version, resp.StatusCode, want)
		}
	}
	put("# 笔记 v1", 0, 200)
	put("别人的版本", 0, 409) // stale
	put("# 笔记 v2", 1, 200)
	put(strings.Repeat("x", 17<<10), 2, http.StatusRequestEntityTooLarge)

	var cur struct {
		Notes struct {
			Version int    `json:"version"`
			Body    string `json:"body"`
		} `json:"notes"`
		Info struct {
			Version  int  `json:"version"`
			NeedSlim bool `json:"need_slim"`
		} `json:"info"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/steward/notes", testToken, nil), &cur)
	if cur.Notes.Version != 2 || cur.Notes.Body != "# 笔记 v2" || cur.Info.NeedSlim {
		t.Fatalf("latest = %+v", cur)
	}
	decode(t, do(t, s, http.MethodGet, "/v1/steward/notes?version=1", testToken, nil), &cur)
	if cur.Notes.Body != "# 笔记 v1" {
		t.Fatalf("v1 = %+v", cur)
	}
	resp := do(t, s, http.MethodGet, "/v1/steward/notes?version=9", testToken, nil)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("missing version = %d", resp.StatusCode)
	}
	var hist struct {
		History []struct {
			Version int    `json:"version"`
			By      string `json:"by"`
		} `json:"history"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/steward/notes?history=1", testToken, nil), &hist)
	if len(hist.History) != 2 || hist.History[0].Version != 2 || !strings.HasPrefix(hist.History[0].By, "human") {
		t.Fatalf("history = %+v", hist)
	}
}

func TestMergeSuggestionsRESTAreAPersonsToConfirm(t *testing.T) {
	s := newStewardServer(t)
	a := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "登录修复"}, 200)
	b := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "登录问题"}, 200)
	resp := do(t, s, http.MethodPost, "/v1/work-items/"+a.ID+"/merge-suggestions", testToken, map[string]any{"source_id": b.ID, "reason": "同一件事"})
	var rec struct {
		Suggestion struct {
			ID int64 `json:"id"`
		} `json:"suggestion"`
		Recorded bool `json:"recorded"`
	}
	decode(t, resp, &rec)
	if !rec.Recorded || rec.Suggestion.ID == 0 {
		t.Fatalf("suggestion = %+v", rec)
	}
	var list struct {
		Suggestions []struct {
			ID       int64  `json:"id"`
			TargetID string `json:"target_id"`
			SourceID string `json:"source_id"`
			Reason   string `json:"reason"`
		} `json:"suggestions"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/work-items/merge-suggestions", testToken, nil), &list)
	if len(list.Suggestions) != 1 || list.Suggestions[0].SourceID != b.ID || list.Suggestions[0].TargetID != a.ID {
		t.Fatalf("list = %+v", list)
	}
	if cur := workGet(t, s, "/v1/work-items/"+b.ID); cur.MergedInto != "" {
		t.Fatal("recording a suggestion merged")
	}
	resp = do(t, s, http.MethodPost, "/v1/work-items/merge-suggestions/"+itoa(rec.Suggestion.ID)+"/accept", testToken, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("accept = %d", resp.StatusCode)
	}
	if cur := workGet(t, s, "/v1/work-items/"+b.ID); cur.MergedInto != a.ID {
		t.Fatalf("accepted suggestion did not merge: %+v", cur)
	}
	resp = do(t, s, http.MethodPost, "/v1/work-items/merge-suggestions/"+itoa(rec.Suggestion.ID)+"/accept", testToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusConflict {
		t.Fatalf("second accept = %d", resp.StatusCode)
	}
}

func TestSessionTailEndpointIsReadOnlyAndBounded(t *testing.T) {
	s := newStewardServer(t)
	dir := t.TempDir()
	tp := filepath.Join(dir, "t.jsonl")
	line := `{"type":"user","message":{"role":"user","content":"帮我加导出"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"好的"}]}}` + "\n"
	if err := os.WriteFile(tp, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFER_TRANSCRIPT_ROOTS", dir)
	if _, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-tail-1", Agent: "claude", ProjectKey: "p", Cwd: "/ws", Runner: "local", Transcript: tp}); err != nil {
		t.Fatal(err)
	}
	var tail struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
		Text      string `json:"text"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/sessions/sess-tail-1/tail?bytes=999999999", testToken, nil), &tail)
	if tail.Source != "transcript" || !strings.Contains(tail.Text, "帮我加导出") || !strings.Contains(tail.Text, "好的") {
		t.Fatalf("tail = %+v", tail)
	}
	resp := do(t, s, http.MethodGet, "/v1/sessions/nope/tail", testToken, nil)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("unknown session tail = %d", resp.StatusCode)
	}
	// Not a write route: a POST to it is not even routed.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sess-tail-1/tail", testToken, nil)
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("POST tail = %d", resp.StatusCode)
	}
}

func TestPutConfigStewardWritesHotAppliesAndEndsTheSessionOnAgentSwitch(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, cr, cfgPath := newConfigWriteTestServer(t, yamlText, fixedDetector{"claude": true, "mytool": true})
	s.Steward().SetConfigFn(func() (config.StewardConfig, config.WorkConfig) { c := cr.Config(); return c.Steward, c.Work })

	resp := do(t, s, http.MethodPut, "/v1/config/steward", opToken, map[string]any{"enabled": true})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin = %d", resp.StatusCode)
	}
	resp = do(t, s, http.MethodPut, "/v1/config/steward", adminToken, map[string]any{
		"enabled": true, "agent": "claude-acp", "project": "default", "review_time": "08:15", "idle_end_min": 45,
		"review_max_items": 10, "event_wake": true, "event_throttle_min": 20,
	})
	if resp.StatusCode != 200 {
		var raw map[string]any
		decode(t, resp, &raw)
		t.Fatalf("admin put = %d: %v", resp.StatusCode, raw)
	}
	resp.Body.Close()
	live := cr.Config().Steward
	if !live.Enabled || live.AgentName() != "claude-acp" || live.ReviewTime != "08:15" || live.IdleEndMin != 45 ||
		live.MaxReviewItems() != 10 || !live.EventWake || live.EventThrottleMin != 20 {
		t.Fatalf("live steward config = %+v", live)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil || !strings.Contains(string(raw), "agent: claude-acp") || !strings.Contains(string(raw), `review_time: "08:15"`) {
		t.Fatalf("config file lacks the steward block: %v\n%s", err, raw)
	}
	var st stewardStatusResp
	decode(t, do(t, s, http.MethodGet, "/v1/steward", adminToken, nil), &st)
	if !st.Status.Enabled || st.Settings.ReviewTime != "08:15" || st.Settings.IdleEndMin != 45 || !st.Settings.ReviewTimeExplicit {
		t.Fatalf("status/settings = %+v", st)
	}

	for _, body := range []map[string]any{{"idle_end_min": -1}, {"review_time": "25:99"}, {}} {
		resp = do(t, s, http.MethodPut, "/v1/config/steward", adminToken, body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("body %v = %d, want 400", body, resp.StatusCode)
		}
	}
	// Partial: untouched fields keep their values.
	resp = do(t, s, http.MethodPut, "/v1/config/steward", adminToken, map[string]any{"idle_end_min": 15})
	resp.Body.Close()
	live = cr.Config().Steward
	if live.AgentName() != "claude-acp" || live.IdleEndMin != 15 || live.ReviewTime != "08:15" {
		t.Fatalf("partial write lost fields: %+v", live)
	}
}

func TestStewardReviewSummaryReachesTheDigest(t *testing.T) {
	s := newStewardServer(t)
	workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "待巡检的事"}, 200)
	resp := do(t, s, http.MethodPost, "/v1/steward/review", testToken, map[string]any{"force": true})
	var rv struct {
		ReviewID int64  `json:"review_id"`
		JobID    string `json:"job_id"`
		Skipped  bool   `json:"skipped"`
	}
	decode(t, resp, &rv)
	if rv.Skipped || rv.JobID == "" {
		t.Fatalf("review = %+v", rv)
	}
	waitStewardState(t, s, "idle")
	tok := stewardTokenOf(t, s, rv.JobID)
	out := jobLog(t, s, rv.JobID, store.StdoutFile)
	if !strings.Contains(out, "手动巡检") || !strings.Contains(out, "review_summary") {
		t.Fatalf("review prompt not delivered:\n%s", out)
	}
	s.Steward().WaitIdle()
	resp = do(t, s, http.MethodPost, "/v1/steward/review-summary", tok, map[string]any{"text": "今天只有一件事，别忘了设备"})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("review-summary = %d", resp.StatusCode)
	}
	var d struct {
		Commentary string `json:"commentary"`
		Text       string `json:"text"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/work-items/digest", testToken, nil), &d)
	if d.Commentary != "今天只有一件事，别忘了设备" || !strings.Contains(d.Text, "管家点评") {
		t.Fatalf("digest = %+v", d)
	}
}

func TestStewardEndpointsRefuseWorkerTokensAndAnonymous(t *testing.T) {
	s := newStewardServer(t)
	resp := do(t, s, http.MethodGet, "/v1/steward", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", resp.StatusCode)
	}
}
