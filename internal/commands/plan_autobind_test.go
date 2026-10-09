package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// autobindServer fakes the three endpoints plan auto-binding touches: the session
// probe, plan create and plan PATCH. sessions maps registered ids to their record;
// refuse makes a bound create fail like validatePlanSupervisorSession does.
type autobindServer struct {
	mu       sync.Mutex
	sessions map[string]client.AgentSession
	refuse   bool
	probes   []string
	creates  []map[string]any
	patches  []map[string]any
	plan     client.Plan
}

func (f *autobindServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			sid := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
			f.probes = append(f.probes, sid)
			a, ok := f.sessions[sid]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "get session failed", "detail": "session not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(client.SessionDetail{Session: a})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/plans":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.creates = append(f.creates, body)
			sid, _ := body["supervisor_session_id"].(string)
			if sid != "" && f.refuse {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid supervisor session", "detail": "supervisor session is unknown or not owned by the authenticated caller"})
				return
			}
			id, _ := body["plan_id"].(string)
			_ = json.NewEncoder(w).Encode(client.Plan{PlanID: id, Status: "open", Leader: "off", SupervisorSessionID: sid})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/plans/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.patches = append(f.patches, body)
			if v, ok := body["leader"].(string); ok {
				f.plan.Leader = v
			}
			if v, ok := body["supervisor_session_id"].(string); ok {
				f.plan.SupervisorSessionID = v
			}
			if v, ok := body["tags"].([]any); ok {
				f.plan.Tags = nil
				for _, x := range v {
					f.plan.Tags = append(f.plan.Tags, x.(string))
				}
			}
			_ = json.NewEncoder(w).Encode(f.plan)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func resetPlanCreateOpts(t *testing.T) {
	t.Helper()
	reset := func() {
		planCreateOpts.planID, planCreateOpts.title, planCreateOpts.supervisorSessionID, planCreateOpts.tags = "", "", "", ""
		planCreateOpts.project, planCreateOpts.desc = "", ""
		planCreateOpts.noSupervisor, planCreateOpts.leader = false, false
		planSetOpts.leader, planSetOpts.tags, planSetOpts.untag = "", "", ""
		planSetOpts.supervisorSessionID, planSetOpts.clearSupervisorSession = "", false
	}
	reset()
	t.Cleanup(reset)
}

func runPlanCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var code int
	out := captureOutput(t, func() { code = NewApp("test").Run(args) })
	return out, code
}

const (
	goferSID  = "aaaaaaaa-1111-4111-8111-000000000001"
	claudeSID = "bbbbbbbb-2222-4222-8222-000000000002"
	codexSID  = "cccccccc-3333-4333-8333-000000000003"
)

// TestPlanCreateAutoBindsCurrentSession covers the resolution order
// (GOFER_SESSION_ID > CLAUDE_CODE_SESSION_ID > CODEX_THREAD_ID), skipping ids the
// server does not know, and the bound / unbound messages.
func TestPlanCreateAutoBindsCurrentSession(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	jobConnOpts.server, jobConnOpts.token = "", ""
	resetPlanCreateOpts(t)

	local := func(sid, name string) client.AgentSession {
		return client.AgentSession{SessionID: sid, Agent: "claude", Runner: "local", State: "idle", PeerName: name}
	}
	cases := []struct {
		name      string
		env       map[string]string
		sessions  map[string]client.AgentSession
		wantBound string
		wantOut   string
	}{
		{"gofer env wins", map[string]string{"GOFER_SESSION_ID": goferSID, "CLAUDE_CODE_SESSION_ID": claudeSID},
			map[string]client.AgentSession{goferSID: local(goferSID, "g-name"), claudeSID: local(claudeSID, "c-name")}, goferSID, "已绑定主 Agent 会话 aaaaaaaa (g-name)"},
		{"claude when no gofer", map[string]string{"CLAUDE_CODE_SESSION_ID": claudeSID, "CODEX_THREAD_ID": codexSID},
			map[string]client.AgentSession{claudeSID: local(claudeSID, "c-name"), codexSID: local(codexSID, "x")}, claudeSID, "已绑定主 Agent 会话 bbbbbbbb (c-name)"},
		{"unregistered falls through to codex", map[string]string{"CLAUDE_CODE_SESSION_ID": claudeSID, "CODEX_THREAD_ID": codexSID},
			map[string]client.AgentSession{codexSID: {SessionID: codexSID, Agent: "codex", Runner: "server", Title: "codex work"}}, codexSID, "已绑定主 Agent 会话 cccccccc (codex work)"},
		{"unregistered only -> unbound", map[string]string{"CLAUDE_CODE_SESSION_ID": claudeSID},
			nil, "", "未绑定主 Agent 会话（session bbbbbbbb ($CLAUDE_CODE_SESSION_ID) is not registered with gofer; 绑定：gofer plan set plan-autobind-1"},
		{"worker-side session is not bound", map[string]string{"CLAUDE_CODE_SESSION_ID": claudeSID},
			map[string]client.AgentSession{claudeSID: {SessionID: claudeSID, Runner: "w-docker"}}, "", "runs on runner w-docker"},
		{"ended session is not bound", map[string]string{"CODEX_SESSION_ID": codexSID},
			map[string]client.AgentSession{codexSID: {SessionID: codexSID, Runner: "local", State: "ended", EndedAt: 1}}, "", "has ended"},
		{"no env -> unbound hint", nil, nil, "", "未绑定主 Agent 会话（绑定：gofer plan set plan-autobind-1 --supervisor-session-id <sid>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			f := &autobindServer{sessions: tc.sessions}
			ts := httptest.NewServer(f.handler(t))
			defer ts.Close()
			resetPlanCreateOpts(t)
			out, code := runPlanCLI(t, "plan", "create", "--server", ts.URL, "--plan-id", "plan-autobind-1")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			if len(f.creates) != 1 {
				t.Fatalf("creates = %d, want 1", len(f.creates))
			}
			got, _ := f.creates[0]["supervisor_session_id"].(string)
			if got != tc.wantBound {
				t.Fatalf("bound %q, want %q", got, tc.wantBound)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Fatalf("output %q missing %q", out, tc.wantOut)
			}
		})
	}
}

// TestPlanCreateAutoBindRefusedAndOptOut: a session the server refuses (not the
// caller's) still yields a plan, unbound; --no-supervisor never probes; an
// explicit id is passed through untouched; a session of another project is skipped.
func TestPlanCreateAutoBindRefusedAndOptOut(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Setenv("CLAUDE_CODE_SESSION_ID", claudeSID)
	sessions := map[string]client.AgentSession{claudeSID: {SessionID: claudeSID, Runner: "local", ProjectKey: "alpha"}}

	t.Run("refused", func(t *testing.T) {
		resetPlanCreateOpts(t)
		f := &autobindServer{sessions: sessions, refuse: true}
		ts := httptest.NewServer(f.handler(t))
		defer ts.Close()
		out, code := runPlanCLI(t, "plan", "create", "--server", ts.URL, "--plan-id", "plan-autobind-2")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if len(f.creates) != 2 || f.creates[1]["supervisor_session_id"] != nil {
			t.Fatalf("want a bound attempt then an unbound create, got %v", f.creates)
		}
		if !strings.Contains(out, "plan plan-autobind-2 created") || !strings.Contains(out, "was refused as supervisor") {
			t.Fatalf("output: %s", out)
		}
	})
	t.Run("no-supervisor", func(t *testing.T) {
		resetPlanCreateOpts(t)
		f := &autobindServer{sessions: sessions}
		ts := httptest.NewServer(f.handler(t))
		defer ts.Close()
		out, code := runPlanCLI(t, "plan", "create", "--server", ts.URL, "--plan-id", "plan-autobind-3", "--no-supervisor")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if len(f.probes) != 0 || f.creates[0]["supervisor_session_id"] != nil {
			t.Fatalf("opt-out must not probe or bind: probes=%v creates=%v", f.probes, f.creates)
		}
		if strings.Contains(out, "主 Agent") {
			t.Fatalf("opt-out prints no binding line: %s", out)
		}
	})
	t.Run("explicit id", func(t *testing.T) {
		resetPlanCreateOpts(t)
		f := &autobindServer{sessions: sessions}
		ts := httptest.NewServer(f.handler(t))
		defer ts.Close()
		out, code := runPlanCLI(t, "plan", "create", "--server", ts.URL, "--plan-id", "plan-autobind-4", "--supervisor-session-id", goferSID)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if len(f.probes) != 0 || f.creates[0]["supervisor_session_id"] != goferSID || !strings.Contains(out, "已绑定主 Agent 会话 aaaaaaaa") {
			t.Fatalf("explicit id: probes=%v creates=%v out=%s", f.probes, f.creates, out)
		}
		resetPlanCreateOpts(t)
		if _, code := runPlanCLI(t, "plan", "create", "--server", ts.URL, "--supervisor-session-id", goferSID, "--no-supervisor"); code == 0 {
			t.Fatal("--supervisor-session-id with --no-supervisor must fail")
		}
	})
	t.Run("other project", func(t *testing.T) {
		resetPlanCreateOpts(t)
		f := &autobindServer{sessions: sessions}
		ts := httptest.NewServer(f.handler(t))
		defer ts.Close()
		out, code := runPlanCLI(t, "plan", "create", "--server", ts.URL, "--plan-id", "plan-autobind-5", "--project", "beta")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out)
		}
		if f.creates[0]["supervisor_session_id"] != nil || !strings.Contains(out, "belongs to project alpha, not beta") {
			t.Fatalf("cross-project: creates=%v out=%s", f.creates, out)
		}
	})
}

// TestPlanSetPrintsWhatChanged: `plan set` reports the fields it touched, not a
// leader line for every edit.
func TestPlanSetPrintsWhatChanged(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	jobConnOpts.server, jobConnOpts.token = "", ""
	f := &autobindServer{plan: client.Plan{PlanID: "plan-set-out1", Status: "open", Leader: "off"}}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()

	cases := []struct {
		args    []string
		want    []string
		notWant []string
	}{
		{[]string{"--tags", "a,b"}, []string{"plan plan-set-out1 tags -> a,b"}, []string{"leader ->", "主 Agent"}},
		{[]string{"--supervisor-session-id", claudeSID}, []string{"plan plan-set-out1 主 Agent 会话 -> bbbbbbbb"}, []string{"leader ->", "tags ->"}},
		{[]string{"--clear-supervisor-session"}, []string{"主 Agent 会话 -> (none)"}, []string{"leader ->"}},
		{[]string{"--leader", "on"}, []string{"plan plan-set-out1 leader -> on"}, []string{"tags ->", "主 Agent"}},
	}
	for _, tc := range cases {
		resetPlanCreateOpts(t)
		out, code := runPlanCLI(t, append([]string{"plan", "set", "--server", ts.URL, "plan-set-out1"}, tc.args...)...)
		if code != 0 {
			t.Fatalf("%v exit %d: %s", tc.args, code, out)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Fatalf("%v output %q missing %q", tc.args, out, w)
			}
		}
		for _, w := range tc.notWant {
			if strings.Contains(out, w) {
				t.Fatalf("%v output %q must not contain %q", tc.args, out, w)
			}
		}
	}
}
