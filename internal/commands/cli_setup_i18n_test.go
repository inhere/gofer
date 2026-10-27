package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
)

// The setup & config command group (init / config / worker / project / serve) is
// migrated to the CLI catalog after job / plan / session
// (docs/design/2026-10-27-i18n-design.md §11 S2b2). These goldens were generated
// from the code BEFORE that migration (GOFER_GOLDEN_UPDATE=1), so an existing
// zh-CN deployment keeps exactly the help and messages it had.
var (
	setupHelpGolden    = filepath.Join("testdata", "cli-help-zh-CN.setup.golden")
	setupOutputsGolden = filepath.Join("testdata", "cli-setup-output-zh-CN.golden")
)

// setupHelpRoots are the command subtrees this step migrates. `serve` and `worker`
// carry their own `stop`, so stop.go's text is covered through them.
var setupHelpRoots = []string{"init", "config", "worker", "project", "serve"}

// TestZhSetupCliHelpIsUnchanged pins the zh-CN help text (descriptions, help,
// examples and every flag usage) of the setup & config subtrees.
func TestZhSetupCliHelpIsUnchanged(t *testing.T) {
	t.Setenv("GOFER_LANG", "zh-CN")
	got := collectCliHelpFor(t, setupHelpRoots)
	compareGolden(t, setupHelpGolden, got)
}

// TestZhSetupOutputsAreUnchanged pins the zh-CN text these commands PRINT (the
// worker doctor rows, the config validate lines and the init hints) — the half of
// a CLI that --help does not show. The cases call the code that renders the text,
// so a change to what an existing zh-CN deployment prints turns this red.
func TestZhSetupOutputsAreUnchanged(t *testing.T) {
	t.Setenv("GOFER_LANG", "zh-CN")
	compareGolden(t, setupOutputsGolden, collectSetupOutputs(t))
}

// compareGolden compares got with the golden file, or writes it when
// GOFER_GOLDEN_UPDATE=1.
func compareGolden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("GOFER_GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (regenerate with GOFER_GOLDEN_UPDATE=1): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s changed (regenerate with GOFER_GOLDEN_UPDATE=1 only when the change is intended)\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

// setupCase is one rendered piece of output: a doctor row, a validate line, an
// init hint.
type setupCase struct {
	name string
	text string
}

// collectSetupOutputs renders the representative outputs of the setup group, one
// "=== <name>" block each, in a fixed order.
func collectSetupOutputs(t *testing.T) string {
	t.Helper()
	cases := doctorOutputCases(t)
	cases = append(cases, configValidateOutputCases(t)...)
	cases = append(cases, initOutputCases(t)...)

	var sb strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&sb, "=== %s\n%s\n", c.name, c.text)
	}
	return sb.String()
}

// doctorOutputCases renders the worker doctor text: every failure row kind, the
// table layout and the verdict line.
func doctorOutputCases(t *testing.T) []setupCase {
	t.Helper()
	dir := t.TempDir()
	good := filepath.Join(dir, "root")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "no-such-root")

	var cases []setupCase
	addRow := func(name string, row workerDoctorRow) {
		cases = append(cases, setupCase{name: name, text: fmt.Sprintf("[%s] %s: %s", row.Status, row.Check, row.Detail)})
	}

	// url rows: a bad scheme, a missing scheme, a missing host, an unreachable
	// port. The last one prints an OS error string, so it is not pinned here.
	addRow("doctor/url/no-scheme", doctorURLRow("hub.local:8765", time.Second))
	addRow("doctor/url/bad-scheme", doctorURLRow("ftp://hub.local:8765", time.Second))
	addRow("doctor/url/no-host", doctorURLRow("ws://", time.Second))

	// token rows: resolvable from the env, inline, or nothing at all.
	t.Setenv("GOFER_TOKEN_FIXTURE", "s3cr3t")
	addRow("doctor/token/from-env", doctorTokenRow(&config.WorkerConfig{
		ServerLink: config.WorkerServerLink{TokenEnv: "GOFER_TOKEN_FIXTURE"},
	}))
	addRow("doctor/token/env-empty", doctorTokenRow(&config.WorkerConfig{
		ServerLink: config.WorkerServerLink{TokenEnv: "GOFER_TOKEN_FIXTURE_UNSET"},
	}))
	addRow("doctor/token/inline", doctorTokenRow(&config.WorkerConfig{
		ServerLink: config.WorkerServerLink{Token: "abc"},
	}))
	addRow("doctor/token/unset", doctorTokenRow(&config.WorkerConfig{}))

	// roots rows: every way a root mapping fails.
	for _, r := range doctorRootRows(&config.WorkerConfig{Roots: []config.WorkerRoot{
		{From: "", To: good},
		{From: "/a", To: ""},
		{From: "/a", To: good},
		{From: "/a", To: good},
		{From: "/b", To: missing},
	}}) {
		addRow("doctor/"+r.Check, r)
	}
	addRow("doctor/guards/unset", doctorGuardsRow(&config.WorkerConfig{}))
	addRow("doctor/max_concurrent/set", doctorMaxConcurrentRow(&config.WorkerConfig{MaxConcurrent: 4}))
	addRow("doctor/max_concurrent/unset", doctorMaxConcurrentRow(&config.WorkerConfig{}))
	addRow("doctor/tools/none", doctorToolsRow(&config.WorkerConfig{}))
	addRow("doctor/tools/missing", doctorToolsRow(&config.WorkerConfig{Tools: []config.Tool{
		{Name: "ffmpeg", Bin: "gofer-no-such-tool-xyz"},
	}}))
	addRow("doctor/agent/unavailable", doctorAgentRow("claude", agent.DetectResult{}))

	cases = append(cases,
		setupCase{name: "doctor/dial-hint/401", text: dialFailureHint(errors.New("websocket: bad handshake (401 Unauthorized)"))},
		setupCase{name: "doctor/dial-hint/other", text: dialFailureHint(errors.New("connection refused"))},
		setupCase{name: "doctor/verdict/failed", text: doctorVerdict(&workerDoctorReport{Failures: 2, Warnings: 1})},
		setupCase{name: "doctor/verdict/warn", text: doctorVerdict(&workerDoctorReport{Warnings: 3})},
		setupCase{name: "doctor/verdict/ok", text: doctorVerdict(&workerDoctorReport{})},
	)

	table, err := renderWorkerDoctor(&workerDoctorReport{
		Config:   "<config>",
		WorkerID: "w1",
		Rows: []workerDoctorRow{
			{Check: "config", Status: doctorPass, Detail: "<config>"},
			{Check: "worker_id", Status: doctorPass, Detail: "w1"},
			{Check: "url", Status: doctorFail, Detail: "ws://hub.local:8765: TCP 不可达"},
		},
		Failures: 1,
	}, false)
	if err != nil {
		t.Fatalf("render doctor table: %v", err)
	}
	cases = append(cases, setupCase{name: "doctor/table", text: strings.TrimRight(table, "\n")})
	for i := range cases {
		cases[i].text = maskDir(cases[i].text, dir, "<tmp>")
	}
	return cases
}

// maskDir replaces a fixture directory (both its host and its yaml spelling) with
// repl, so a golden never carries the machine's temp path.
func maskDir(s, dir, repl string) string {
	s = strings.ReplaceAll(s, dir, repl)
	return strings.ReplaceAll(s, slashPath(dir), repl)
}

// configValidateOutputCases renders `config validate worker` for a broken config
// and for the policy roots checks; its lines ARE the operator-facing wording.
func configValidateOutputCases(t *testing.T) []setupCase {
	t.Helper()
	var cases []setupCase

	// A worker config with nothing set: worker_id, urls and projects all fail.
	path := filepath.Join(t.TempDir(), "worker.yaml")
	if err := os.WriteFile(path, []byte("worker_id: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := captureValidateWorker(t, path)
	cases = append(cases, setupCase{name: "validate/worker/empty", text: out + errorLine(err)})

	// A policy worker whose roots fail (missing from / duplicate / missing dir) and
	// whose roots overlap.
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "worker_id: w1\nroots:\n" +
		"  - from: /a\n    to: " + slashPath(root) + "\n" +
		"  - from: /a\n    to: " + slashPath(root) + "\n" +
		"  - from: /a/b\n    to: " + slashPath(filepath.Join(filepath.Dir(root), "nope")) + "\n" +
		"server_link:\n  urls: [ws://127.0.0.1:1]\n  token: t\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = captureValidateWorker(t, path)
	cases = append(cases, setupCase{
		name: "validate/worker/roots",
		text: maskDir(out, filepath.Dir(root), "<tmp>") + errorLine(err),
	})
	return cases
}

// captureValidateWorker runs `config validate worker` against path and returns
// what it printed.
func captureValidateWorker(t *testing.T, path string) (string, error) {
	t.Helper()
	c := bindCmd(configSubCmd(t, "validate"))
	c.Arg("target").WithValue("worker")
	oldCfg := config.InputCfgFile
	t.Cleanup(func() { config.InputCfgFile = oldCfg })
	config.InputCfgFile = path
	var err error
	out := captureOutput(t, func() { err = runConfigValidate(c, nil) })
	return out, err
}

// configSubCmd returns the `config` sub-command called name.
func configSubCmd(t *testing.T, name string) *gcli.Command {
	t.Helper()
	for _, sub := range NewConfigCmd().Subs {
		if sub.Name == name {
			return sub
		}
	}
	t.Fatalf("config has no %q sub-command", name)
	return nil
}

// errorLine renders the returned error the way the shell reports the exit: the
// message plus its coded exit status (the exit code is machine-readable and must
// never be localized).
func errorLine(err error) string {
	if err == nil {
		return ""
	}
	return "exit: " + err.Error()
}

// initOutputCases renders the completion hints `gofer init` prints.
func initOutputCases(t *testing.T) []setupCase {
	t.Helper()
	var cases []setupCase
	dir := t.TempDir()

	// init worker --output <path>: the non-global completion hint.
	c := bindCmd(NewInitCmd(buildinfo.Info{}))
	c.Arg("target").WithValue("worker")
	oldOpts := initOpts
	t.Cleanup(func() { initOpts = oldOpts })
	path := filepath.Join(dir, "worker.yaml")
	initOpts.config = path
	out := captureOutput(t, func() {
		if err := runInit(c, buildinfo.Info{}); err != nil {
			t.Errorf("init worker: %v", err)
		}
	})
	cases = append(cases, setupCase{name: "init/worker", text: strings.ReplaceAll(out, dir, "<tmp>")})

	// init client: the .env starter hint.
	c = bindCmd(NewInitCmd(buildinfo.Info{}))
	c.Arg("target").WithValue("client")
	initOpts.config = filepath.Join(dir, ".env")
	out = captureOutput(t, func() {
		if err := runInit(c, buildinfo.Info{}); err != nil {
			t.Errorf("init client: %v", err)
		}
	})
	cases = append(cases, setupCase{name: "init/client", text: strings.ReplaceAll(out, dir, "<tmp>")})

	// The worker wizard's default-workspace hint.
	cases = append(cases, setupCase{name: "init/workspace-hint", text: inlineWorkspaceHint("/ws")})
	return cases
}

// TestSetupGoferLangPinsTheHelp guards the language source: the same command tree
// built under GOFER_LANG=en must render English help, i.e. the descriptions are
// resolved while the tree is built, never in an init().
func TestSetupGoferLangPinsTheHelp(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	app := NewApp("test")
	cmd, ok := app.Command("worker")
	if !ok {
		t.Fatal("worker command not found")
	}
	if han.MatchString(cmd.Desc) {
		t.Errorf("worker desc = %q, want English", cmd.Desc)
	}
}
