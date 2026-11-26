package hookrelay

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/stagenote"
)

func inFlow(lang i18n.Lang) client.AgentSession {
	return client.AgentSession{
		SessionID: "sid-stage", Agent: "claude", Language: string(lang),
		Stage: &stagenote.Brief{WorkItemID: "w-1", Step: "doing", StepNo: 4, StepTotal: 6, Status: "active"},
	}
}

func stagePayload(t *testing.T, agent, sid, event, prompt string) Payload {
	return payload(t, agent, map[string]any{"session_id": sid, "hook_event_name": event, "prompt": prompt, "cwd": "/w/repo"})
}

// Every prompt of a session on a flow ends its context with the stage line: after the job
// notice, the letter and the memory — for a typed prompt and for a harness-generated one.
func TestStageLineClosesTheContextOfEveryPrompt(t *testing.T) {
	line := stagenote.Line(i18n.ZhCN, *inFlow(i18n.ZhCN).Stage)
	for _, tc := range []struct{ name, prompt string }{
		{"human", "开始发版"},
		{"harness", JobDoneTag + " job-9 t status=done exit=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMailFake()
			f.sessions["sid-stage"] = inFlow(i18n.ZhCN)
			f.add(7, false, "a letter")
			f.watchRows = []client.SessionJobWatch{{JobID: "job-5", Title: "t", Status: "done"}}
			opts := fastOpts(nil)
			opts.MemoryStateDir = t.TempDir()
			opts.PromptMemories = func(string) ([]PromptMemory, error) {
				return []PromptMemory{{Memory: kwMemory("release-flow", "", "step 1", "发版")}}, nil
			}
			res, err := Run(f, stagePayload(t, "claude", "sid-stage", "UserPromptSubmit", tc.prompt), opts)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(res.Context, line) || strings.Count(res.Context, "gofer stage done") != 1 {
				t.Fatalf("the stage line must close the context exactly once: %q", res.Context)
			}
			if !strings.Contains(res.Context, "job-5") || !strings.Contains(res.Context, `<gofer-mail id="7"`) {
				t.Fatalf("the other notices must stay in front: %q", res.Context)
			}
			if tc.name == "human" && strings.Index(res.Context, "step 1") > strings.Index(res.Context, line) {
				t.Fatalf("the memory must come before the stage line: %q", res.Context)
			}
		})
	}
}

// With no flow in progress (or no stage object at all) a prompt carries no stage line; and
// an agent whose hook output never reaches the model gets none either.
func TestStageLineAbsentWithoutAFlow(t *testing.T) {
	for _, b := range []*stagenote.Brief{nil, {Enabled: true}} {
		f := newFake()
		f.sessions["sid-stage"] = client.AgentSession{SessionID: "sid-stage", Stage: b}
		res, err := Run(f, stagePayload(t, "claude", "sid-stage", "UserPromptSubmit", "hello"), fastOpts(nil))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Context, "gofer stage") {
			t.Fatalf("no flow, brief=%+v: %q", b, res.Context)
		}
	}
	f := newFake()
	f.sessions["sid-omp"] = client.AgentSession{SessionID: "sid-omp", Stage: inFlow(i18n.ZhCN).Stage}
	res, err := Run(f, stagePayload(t, "omp", "sid-omp", "UserPromptSubmit", "hello"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Context, "gofer stage") {
		t.Fatalf("an observe-only agent must get nothing: %q", res.Context)
	}
}

func startContext(t *testing.T, a client.AgentSession) string {
	t.Helper()
	f := newFake()
	f.sessions[a.SessionID] = a
	res, err := Run(f, stagePayload(t, "claude", a.SessionID, "SessionStart", ""), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	return res.Context
}

// SessionStart: a session with no flow in a project that has the flow on is told to start
// one before implementation work; a project with it off hears nothing; a session on a flow
// gets its current step instead of the suggestion.
func TestStageSessionStartHint(t *testing.T) {
	const hint = "gofer stage start <issue>"
	on := client.AgentSession{SessionID: "s1", Language: "zh-CN", Stage: &stagenote.Brief{Enabled: true}}
	if got := startContext(t, on); !strings.Contains(got, "[gofer · 流程] 实现类工作先执行 "+hint) {
		t.Fatalf("flow on, no flow: %q", got)
	}
	off := client.AgentSession{SessionID: "s2", Language: "zh-CN"}
	if got := startContext(t, off); strings.Contains(got, "gofer stage") {
		t.Fatalf("flow off: %q", got)
	}
	cur := inFlow(i18n.ZhCN)
	cur.SessionID = "s3"
	got := startContext(t, cur)
	if strings.Contains(got, hint) || !strings.Contains(got, "第 4/6 步「doing」") {
		t.Fatalf("in a flow SessionStart gives the step, not the suggestion: %q", got)
	}
}

// A seat session gets the stage line inside its seat block, once; a seat session whose
// project has the flow off gets nothing about stage.
func TestStageSessionStartInTheSeatBlock(t *testing.T) {
	seatOf := &client.SessionSeat{ID: "seat-1", Name: "dev", Purpose: "pr review"}
	a := client.AgentSession{SessionID: "s4", Language: "en", Seat: seatOf, Stage: &stagenote.Brief{Enabled: true}}
	got := startContext(t, a)
	hint := stagenote.StartLine(i18n.EN)
	if strings.Count(got, hint) != 1 {
		t.Fatalf("the hint must appear once: %q", got)
	}
	seatHead := i18n.T(i18n.EN, "seat.hook.heading_purpose", "name", "dev", "purpose", "pr review")
	if strings.Index(got, seatHead) > strings.Index(got, hint) {
		t.Fatalf("the hint belongs inside the seat block, after its heading: %q", got)
	}
	a.SessionID, a.Stage = "s5", nil
	if got := startContext(t, a); strings.Contains(got, "gofer stage") {
		t.Fatalf("flow off, seat session: %q", got)
	}
}
