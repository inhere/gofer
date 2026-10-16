package commands

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestWorkDispatchCLI(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	server, st := newPlanTestServerStore(t)

	capture := func(text string) string {
		w, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: text, Kind: jobstore.WorkKindIdea, By: "human:t"})
		if err != nil {
			t.Fatalf("capture: %v", err)
		}
		return w.ID
	}
	status := func(id string) string {
		w, _, _ := st.GetWorkItem(id)
		return w.Status
	}

	// The flags each target needs, and --to itself, are checked before any request.
	id := capture("要分派的想法")
	for _, args := range [][]string{
		{"dispatch", id},
		{"dispatch", id, "--to", "bogus"},
		{"dispatch", id, "--to", "session"},
		{"dispatch", id, "--to", "job"},
	} {
		if out, code := workCLI(t, server, args...); code == 0 || !strings.Contains(out, "dispatch") {
			t.Fatalf("%v must fail (exit %d):\n%s", args, code, out)
		}
	}
	// An unknown job is refused by the server and the item stays captured.
	if out, code := workCLI(t, server, "dispatch", id, "--to", "job", "--job", "job-nope"); code == 0 {
		t.Fatalf("dispatch to an unknown job must fail:\n%s", out)
	}
	if status(id) != jobstore.WorkCaptured {
		t.Fatalf("status = %s, want captured", status(id))
	}

	out := workCLIOK(t, server, "dispatch", id, "--to", "todo")
	if !strings.Contains(out, "dispatched "+id+" to todo") || status(id) != jobstore.WorkActive {
		t.Fatalf("to todo (status %s):\n%s", status(id), out)
	}
	// It left the capture box: a second dispatch is refused.
	if out, code := workCLI(t, server, "dispatch", id, "--to", "archive"); code == 0 {
		t.Fatalf("dispatch of a non-captured item must fail:\n%s", out)
	}

	a := capture("归档")
	workCLIOK(t, server, "dispatch", a, "--to", "archive")
	d := capture("丢弃")
	workCLIOK(t, server, "dispatch", d, "--to", "drop")
	if status(a) != jobstore.WorkDone || status(d) != jobstore.WorkDropped {
		t.Fatalf("archive/drop statuses = %s / %s", status(a), status(d))
	}
}
