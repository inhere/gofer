package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/sessionrelay"
)

type resumePlanBody struct {
	Can     bool     `json:"can"`
	Reason  string   `json:"reason"`
	Message string   `json:"message"`
	Warning string   `json:"warning"`
	State   string   `json:"state"`
	Ended   bool     `json:"ended"`
	Runner  string   `json:"runner"`
	Agent   string   `json:"agent"`
	Cwd     string   `json:"cwd"`
	Command []string `json:"command"`
}

func endSession(t *testing.T, s *Server, sid string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/sessions/"+sid+"/heartbeat", testToken, map[string]any{"event": "SessionEnd"})
	resp.Body.Close()
	if got := sessionState(t, s, sid); got.State != "ended" {
		t.Fatalf("session state = %q, want ended", got.State)
	}
}

// An ended session is exactly what a wake-up is for: the plan says can=true with a
// plain-language summary, resume starts the takeover job, and the session ends up
// handed_off — and back at ended (not idle) once the takeover is released.
func TestSessionResumeEndedSession(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	to := &httpTakeoverer{res: sessionrelay.TakeoverResult{JobID: "job-resume-1"}}
	s.relay.SetTakeoverer(to)
	registerTakeoverSession(t, s, "sid-ended", "/w/repo/sub", "")
	endSession(t, s, "sid-ended")

	var plan resumePlanBody
	if code := getJSON(t, s, "/v1/sessions/sid-ended/takeover-plan", &plan); code != http.StatusOK {
		t.Fatalf("plan status = %d", code)
	}
	if !plan.Can || !plan.Ended || plan.Cwd != "sub" || len(plan.Command) == 0 || plan.Warning != "" || !strings.Contains(plan.Message, "会话已结束") {
		t.Fatalf("plan = %+v", plan)
	}
	if len(to.reqs) != 0 {
		t.Fatal("a plan must not start anything")
	}

	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-ended/resume", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d", resp.StatusCode)
	}
	var out struct {
		Path  string `json:"path"`
		JobID string `json:"job_id"`
	}
	decode(t, resp, &out)
	if out.Path != "takeover" || out.JobID != "job-resume-1" {
		t.Fatalf("resume result = %+v", out)
	}
	if len(to.reqs) != 1 || to.reqs[0].InitialInput != "" || to.reqs[0].SessionID != "sid-ended" {
		t.Fatalf("takeover requests = %+v, want one resume with no priming text", to.reqs)
	}
	if got := sessionState(t, s, "sid-ended"); got.State != "handed_off" || got.HandedOffJobID != "job-resume-1" {
		t.Fatalf("session = %+v, want handed_off", got)
	}
	// Waking a handed-off session again is refused with a reason, and the plan says so.
	var again resumePlanBody
	getJSON(t, s, "/v1/sessions/sid-ended/takeover-plan", &again)
	if again.Can || !strings.HasPrefix(again.Reason, "handed_off:") || !strings.Contains(again.Message, "job-resume-1") {
		t.Fatalf("plan of a handed-off session = %+v", again)
	}
	// Releasing a woken-up ended session returns it to ended, not idle.
	resp = do(t, s, http.MethodPost, "/v1/sessions/sid-ended/release-takeover", testToken, nil)
	var released sessionView
	decode(t, resp, &released)
	if released.State != "ended" || released.HandedOffJobID != "" {
		t.Fatalf("released = %+v, want ended with the takeover cleared", released)
	}
}

func TestSessionResumeWithInitialInput(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	to := &httpTakeoverer{res: sessionrelay.TakeoverResult{JobID: "job-resume-2"}}
	s.relay.SetTakeoverer(to)
	registerTakeoverSession(t, s, "sid-live", "/w/repo", "")
	var plan resumePlanBody
	getJSON(t, s, "/v1/sessions/sid-live/takeover-plan", &plan)
	if !plan.Can || plan.Ended || plan.Warning == "" {
		t.Fatalf("a not-yet-ended session can be taken over, with a warning about the open terminal: %+v", plan)
	}
	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-live/resume", testToken, map[string]any{"initial_input": "继续"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(to.reqs) != 1 || to.reqs[0].InitialInput != "[gofer web 回复] 继续\r" {
		t.Fatalf("requests = %+v", to.reqs)
	}
}

// Every refusal keeps its reason code in the short error and a Chinese explanation
// in the detail, and the plan reports the same.
func TestSessionResumeRefusals(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	to := &httpTakeoverer{plan: sessionrelay.TakeoverPlan{Argv: []string{"claude", "--resume", "x"}, AllowInteractive: false, ExecRoot: "/w/repo"}}
	s.relay.SetTakeoverer(to)
	registerTakeoverSession(t, s, "sid-no-inter", "/w/repo", "")
	endSession(t, s, "sid-no-inter")

	var plan resumePlanBody
	getJSON(t, s, "/v1/sessions/sid-no-inter/takeover-plan", &plan)
	if plan.Can || plan.Reason != "interactive_not_allowed" || !strings.Contains(plan.Message, "allow_interactive") {
		t.Fatalf("plan = %+v", plan)
	}
	resp := do(t, s, http.MethodPost, "/v1/sessions/sid-no-inter/resume", testToken, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("resume status = %d, want 409", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error != "resume failed: interactive_not_allowed" || !strings.Contains(body.Detail, "交互终端") {
		t.Fatalf("error body = %+v", body)
	}
	if len(to.reqs) != 0 {
		t.Fatal("a refused resume must not start a process")
	}

	// cwd outside the project root as the runner sees it.
	to.plan = sessionrelay.TakeoverPlan{Argv: []string{"claude"}, AllowInteractive: true, ExecRoot: "/elsewhere"}
	var out resumePlanBody
	getJSON(t, s, "/v1/sessions/sid-no-inter/takeover-plan", &out)
	if out.Reason != "cwd_outside_project" || !strings.Contains(out.Message, "/w/repo") {
		t.Fatalf("plan = %+v", out)
	}
	if code := getJSON(t, s, "/v1/sessions/unknown/takeover-plan", nil); code != http.StatusNotFound {
		t.Fatalf("unknown session plan status = %d, want 404", code)
	}
	resp = do(t, s, http.MethodPost, "/v1/sessions/unknown/resume", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session resume status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
