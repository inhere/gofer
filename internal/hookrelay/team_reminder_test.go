package hookrelay

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/stagenote"
)

// The server returns the leader protocol block on the heartbeat of a UserPromptSubmit of a
// session on a leader seat; the hook puts it in the context, after the stage line, for a typed
// prompt and a harness one alike.
func TestTeamBlockClosesTheContextOfALeaderPrompt(t *testing.T) {
	const block = "[gofer · team] TEAM-BLOCK-LINE\nroster"
	stage := inFlow(i18n.EN)
	stage.TeamNote = block + "\n\n"
	line := stagenote.Line(i18n.EN, *stage.Stage)
	for _, tc := range []struct{ name, prompt string }{
		{"human", "start the release"},
		{"harness", JobDoneTag + " job-9 t status=done exit=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMailFake()
			f.sessions["sid-stage"] = stage
			f.add(7, false, "a letter")
			res, err := Run(f, stagePayload(t, "claude", "sid-stage", "UserPromptSubmit", tc.prompt), fastOpts(nil))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(res.Context, block) || strings.Count(res.Context, "TEAM-BLOCK-LINE") != 1 {
				t.Fatalf("the team block must close the context exactly once: %q", res.Context)
			}
			if strings.Index(res.Context, line) > strings.Index(res.Context, "TEAM-BLOCK-LINE") || !strings.Contains(res.Context, `<gofer-mail id="7"`) {
				t.Fatalf("the stage line and the letters stay in front: %q", res.Context)
			}
		})
	}
}

// No team note (any other session), or an agent whose hook output never reaches the model:
// nothing is added.
func TestTeamBlockAbsentWithoutALeader(t *testing.T) {
	f := newFake()
	f.sessions["sid-stage"] = client.AgentSession{SessionID: "sid-stage", Agent: "claude"}
	res, err := Run(f, stagePayload(t, "claude", "sid-stage", "UserPromptSubmit", "hello"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Context, "team") {
		t.Fatalf("not a leader: %q", res.Context)
	}
	f = newFake()
	f.sessions["sid-omp"] = client.AgentSession{SessionID: "sid-omp", TeamNote: "[gofer · team] X"}
	res, err = Run(f, stagePayload(t, "omp", "sid-omp", "UserPromptSubmit", "hello"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Context, "[gofer · team]") {
		t.Fatalf("an observe-only agent must get nothing: %q", res.Context)
	}
}
