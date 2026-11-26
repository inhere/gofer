package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// stageCLI runs `gofer stage <sub> --server <server> ...` through the real dispatch, the way
// an agent in a session runs it.
func stageCLI(t *testing.T, server string, args ...string) (string, int) {
	t.Helper()
	stageOpts = stageOptions{}
	var code int
	out := captureOutput(t, func() {
		app := NewApp("test")
		code = app.Run(append([]string{"stage", args[0], "--server", server}, args[1:]...))
	})
	return out, code
}

func stageCLIOK(t *testing.T, server string, args ...string) string {
	t.Helper()
	out, code := stageCLI(t, server, args...)
	if code != 0 {
		t.Fatalf("gofer stage %v failed (exit %d):\n%s", args, code, out)
	}
	return out
}

// TestStageCLIEndToEnd: start / ls / status / done (gate not passed -> exit 2) / blocked /
// back / drop, all against the real server, plus the "no session, no --work" message.
func TestStageCLIEndToEnd(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	server := newPlanTestServer(t)

	out := stageCLIOK(t, server, "start", "gofer-91", "--size", "small", "--project", "self")
	if !strings.Contains(out, "doing ▶ → verify → done") {
		t.Fatalf("start must draw the small template:\n%s", out)
	}
	id := workIDFrom(t, out)

	if out, code := stageCLI(t, server, "start", "gofer-91", "--size", "huge", "--project", "self"); code == 0 || !strings.Contains(out, "--size must be one of") {
		// the same issue would also be refused (a flow is active), so this checks the flag first
		t.Fatalf("bad --size must fail before the request:\n%s", out)
	}

	if out := stageCLIOK(t, server, "ls"); !strings.Contains(out, id) || !strings.Contains(out, "small") {
		t.Fatalf("ls:\n%s", out)
	}

	// The doing gate (a linked plan whose todos are all done) passes here, so `done` moves the
	// flow on; the verify gate wants a verify workflow run and there is none.
	if out := stageCLIOK(t, server, "done", "--work", id); !strings.Contains(out, "advanced: doing > verify") {
		t.Fatalf("done at doing:\n%s", out)
	}
	out, code := stageCLI(t, server, "done", "--work", id)
	if code != stageGateExit || !strings.Contains(out, "did not pass") {
		t.Fatalf("done with an unmet gate = exit %d:\n%s", code, out)
	}

	out = stageCLIOK(t, server, "blocked", "--work", id, "--reason", "waiting for the printer")
	if !strings.Contains(out, "blocked: waiting for the printer") || !strings.Contains(out, "⏸") {
		t.Fatalf("blocked:\n%s", out)
	}

	if out, code := stageCLI(t, server, "back", "--work", id); code == 0 || !strings.Contains(out, "--reason") {
		t.Fatalf("back without --reason must fail:\n%s", out)
	}
	// verify -> doing is a real step back in the small template.
	if out := stageCLIOK(t, server, "back", "--work", id, "--reason", "the verify found a regression"); !strings.Contains(out, "doing ▶") {
		t.Fatalf("back:\n%s", out)
	}

	out = stageCLIOK(t, server, "drop", "--work", id, "--reason", "superseded")
	if !strings.Contains(out, "dropped") {
		t.Fatalf("drop:\n%s", out)
	}

	// Outside a session, a subcommand without --work says how to name the item.
	if out, code := stageCLI(t, server, "status"); code == 0 || !strings.Contains(out, "--work") {
		t.Fatalf("status with no session must point at --work:\n%s", out)
	}
}

// TestStageRemindOncePerSession: the F2 hint is printed once per session, and not at all
// while the session already has a flow in progress.
func TestStageRemindOncePerSession(t *testing.T) {
	isolateConfigEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "projects:\n  self:\n    host_path: \"" + strings.ReplaceAll(dir, `\`, `\\`) + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	config.InputCfgFile = cfgPath
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	server := newPlanTestServer(t)
	cli, err := newClient("", server, "")
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	cmd := NewApp("test").Commands()["stage"]

	t.Setenv(client.ExplicitSessionEnv, "sess-stage-remind")
	out := captureOutput(t, func() { stageRemind(cmd, cli, "self") })
	if !strings.Contains(out, "gofer stage start") {
		t.Fatalf("the first hint must name the command:\n%s", out)
	}
	if again := captureOutput(t, func() { stageRemind(cmd, cli, "self") }); strings.TrimSpace(again) != "" {
		t.Fatalf("the hint must be printed once per session, got again:\n%s", again)
	}

	// A session that already has a flow in progress is not told to start one.
	t.Setenv(client.ExplicitSessionEnv, "sess-stage-has-flow")
	if _, err := cli.StageStartByIssue("gofer-93", "small", "self", "", "sess-stage-has-flow"); err != nil {
		t.Fatalf("start a flow for the session: %v", err)
	}
	if quiet := captureOutput(t, func() { stageRemind(cmd, cli, "self") }); strings.TrimSpace(quiet) != "" {
		t.Fatalf("a session with a flow must not be reminded:\n%s", quiet)
	}

	// A project with the switch off is never reminded.
	t.Setenv(client.ExplicitSessionEnv, "sess-stage-off")
	if on := stageEnabledFor("nowhere"); on {
		t.Fatal("an unknown project must not be reminded")
	}
}

// TestStageRemindAgentSkipsTheShellRunner: only implementation jobs get the hint.
func TestStageRemindAgentSkipsTheShellRunner(t *testing.T) {
	if stageRemindAgent(config.BuiltinExecAgentKey) {
		t.Fatal("the shell runner is not implementation work")
	}
	if stageRemindAgent("") {
		t.Fatal("an unresolved agent is not implementation work")
	}
	if !stageRemindAgent("claude") {
		t.Fatal("an agent job is implementation work")
	}
}
