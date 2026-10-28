package agent

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
)

func TestBuiltinCLIReadDirArgs(t *testing.T) {
	for _, key := range []string{"claude", "suga"} {
		ac := applySessionDefaults(key, config.AgentConfig{Type: TypeCLIAgent})
		found := false
		for _, arg := range ac.AddDirArgs {
			if strings.Contains(arg, "{{dir}}") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s add_dir_args = %v, want an argument containing {{dir}}", key, ac.AddDirArgs)
		}
	}
}

// `--help` was measured with, plus the session / ndjson defaults that make a suga
// job injectable, resumable and its stream readable. suga is a CLI and an ACP
// server in one binary, so the two keys must resolve independently and share one
// session family (asserted in session_family_test.go).
func TestSugaBuiltinTemplate(t *testing.T) {
	cfg, _ := Resolve(&config.Config{}, newFake("suga", "suga-acp"))
	if !cfg.IsInjectedAgent("suga") || !cfg.IsInjectedAgent("suga-acp") {
		t.Fatalf("suga templates were not injected (injected=%v)", cfg.InjectedAgents())
	}
	reg := NewRegistry(cfg)

	cli, ok := reg.Get("suga")
	if !ok || cli.Type != TypeCLIAgent {
		t.Fatalf("suga = %+v (ok=%v), want a cli-agent", cli, ok)
	}
	// Batch: the prompt travels as --prompt (one argv element per flag), and
	// --allow-write is what makes the workspace/shell tools available to a job at
	// all — `suga run` is read-only without it.
	if want := []string{"run", "--allow-write", "--output-format", "stream-json", "--prompt", "{{prompt}}"}; !equalStringSlices(cli.Args, want) {
		t.Errorf("suga args = %v, want %v", cli.Args, want)
	}
	if !equalStringSlices(cli.InteractiveArgs, []string{"chat"}) {
		t.Errorf("suga interactive_args = %v, want [chat]", cli.InteractiveArgs)
	}
	// The templates carry no session fields: they come from the built-in table.
	def := builtinSessionDefaults["suga"]
	if !equalStringSlices(cli.SessionInject, def.SessionInject) ||
		!equalStringSlices(cli.ExitKeys, def.ExitKeys) ||
		!equalStringSlices(cli.SessionResume, def.SessionResume) ||
		!equalStringSlices(cli.SessionResumeInteractive, def.SessionResumeInteractive) ||
		!equalStringSlices(cli.SystemInject, def.SystemInject) ||
		!equalStringSlices(cli.FromSessionArgs, def.FromSessionArgs) ||
		!equalStringSlices(cli.AddDirArgs, def.AddDirArgs) {
		t.Errorf("suga did not inherit the built-in session defaults: %+v", cli)
	}
	// gofer hands a new session its own id (`suga run`/`chat` create the session named
	// by --session-id) instead of scraping one out of the output.
	if !equalStringSlices(cli.SessionInject, []string{"--session-id", "{{session_id}}"}) {
		t.Errorf("suga session_inject = %v, want --session-id {{session_id}}", cli.SessionInject)
	}
	// The chat REPL exits on the `:quit` command (Ctrl-D also quits).
	if !equalStringSlices(cli.ExitKeys, []string{":quit", "enter"}) {
		t.Errorf("suga exit_keys = %v, want [\":quit\" enter]", cli.ExitKeys)
	}
	// Resume re-states the subcommand and keeps both --allow-write and the stream-json
	// capture, so a resumed job is as capable and as readable as the source.
	for _, want := range []string{"run", "--allow-write", "--output-format", "stream-json", "{{session_id}}"} {
		if !hasArg(cli.SessionResume, want) {
			t.Errorf("suga session_resume = %v, want it to carry %q", cli.SessionResume, want)
		}
	}
	if !hasArg(cli.SessionResumeInteractive, "chat") || hasPrompt(cli.SessionResumeInteractive) {
		t.Errorf("suga session_resume_interactive = %v, want a prompt-less `chat --resume` argv", cli.SessionResumeInteractive)
	}
	// The chat exit summary prints `续接：suga chat --resume <id>`; a suga session id is
	// a bounded token, not necessarily a uuid.
	m := regexp.MustCompile(cli.SessionCapture).FindStringSubmatch("本次 2 轮 · in 1 / out 2 tokens\n续接：suga chat --resume gofer-6uhf-abc123\n")
	if len(m) != 2 || m[1] != "gofer-6uhf-abc123" {
		t.Errorf("suga session capture = %v, want the id from the exit summary", m)
	}

	// stream-json capture: the answer is the `result` line's `result` text, the usage
	// object is `usage`, and the whitelist drops the per-token deltas.
	if cli.NDJSONStdoutPath != "result" || cli.NDJSONUsagePath != "usage" {
		t.Errorf("suga ndjson paths = (%q, %q), want (result, usage)", cli.NDJSONStdoutPath, cli.NDJSONUsagePath)
	}
	if !reflect.DeepEqual(cli.NDJSONKeep, builtinNDJSON["suga"].keep) {
		t.Errorf("suga ndjson_keep = %v, want %v", cli.NDJSONKeep, builtinNDJSON["suga"].keep)
	}
	for _, noisy := range []string{"message_delta", "usage", "retry"} {
		if hasArg(cli.NDJSONKeep, noisy) {
			t.Errorf("suga ndjson_keep keeps %q — the per-token / running-tally noise the whitelist exists to drop", noisy)
		}
	}
	if got := NDJSONProjectorFor("suga", cli); got != ndjsonfilter.ProjectorGeneric {
		t.Errorf("suga projector = %q, want generic (its event names are its own dialect)", got)
	}

	// read_only_args / model_args come from the per-CLI tables, so a renamed suga
	// wrapper inherits them too. `--read-only` beats the --allow-write the batch argv
	// carries (suga's own AllowWrite = (--allow-write || tools.allow_write) &&
	// !--read-only), so an admitted read-only job really cannot write.
	if !equalStringSlices(cli.ReadOnlyArgs, []string{"--read-only"}) {
		t.Errorf("suga read_only_args = %v, want [--read-only]", cli.ReadOnlyArgs)
	}
	if !equalStringSlices(cli.ModelArgs, []string{"--model", "{{model}}"}) {
		t.Errorf("suga model_args = %v, want [--model {{model}}]", cli.ModelArgs)
	}
	// A job whose cwd is a NESTED repository (one of the repositories inside the workspace
	// project) gets the project root as an extra read-only directory, so the
	// workspace-level rules / tracker / docs stay readable from the inner checkout.
	if !equalStringSlices(cli.AddDirArgs, []string{"--add-dir", "{{dir}}:ro"}) {
		t.Errorf("suga add_dir_args = %v, want [--add-dir {{dir}}:ro]", cli.AddDirArgs)
	}
	// Live delivery to a running `suga chat`: the text goes on stdin and `suga send`
	// exits 3 by itself when the session's process is gone, so no offline_match is
	// needed (gofer's exit-code contract is suga's own).
	if !equalStringSlices(cli.DeliverCommand, []string{"send", "--session", "{{session_id}}", "--source", "gofer"}) ||
		!cli.DeliverStdin || cli.DeliverOfflineMatch != "" {
		t.Errorf("suga delivery = (cmd %v, stdin %v, offline %q)", cli.DeliverCommand, cli.DeliverStdin, cli.DeliverOfflineMatch)
	}

	acpAgent, ok := reg.Get("suga-acp")
	if !ok || acpAgent.Type != TypeACPAgent {
		t.Fatalf("suga-acp = %+v (ok=%v), want an acp-agent", acpAgent, ok)
	}
	if acpAgent.ACP == nil || acpAgent.ACP.Modes["read_only"] != "read_only" {
		t.Errorf("suga-acp acp block = %+v, want read_only -> read_only", acpAgent.ACP)
	}
	if !acpAgent.ACP.AllowsLoadSession() {
		t.Error("suga-acp must allow session/load (its ACP server advertises loadSession), else a resume cannot continue a session")
	}
	if acpAgent.OutputFormat == config.OutputFormatNDJSON {
		t.Error("suga-acp must not use the cli-agent ndjson capture: an ACP session's stream is the protocol's")
	}
}
