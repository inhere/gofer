package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
)

// F14 (2026-09-25): values that reach an agent's environment through claude's settings
// file must stay THERE. This is the job-level half of the promise — request_json (the
// audit copy of the request), the rendered command, the job events and every file in
// the result directory are scanned for the sentinel the settings file carries.
//
// The test is deliberately non-vacuous: the same settings file also carries a key the
// fake agent REPORTS, so "the inheritance happened" is proven in the same run that
// proves the other key went nowhere.
const (
	settingsSecretKey   = "ANTHROPIC_API_KEY"
	settingsSecretValue = "sk-ant-f14-never-persisted"
	settingsEchoKey     = "GOFER_F14_ECHO"
	settingsEchoValue   = "echo-from-claude-settings"
)

func TestACPClaudeSettingsEnvNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	doc := map[string]any{"env": map[string]string{
		settingsSecretKey: settingsSecretValue,
		settingsEchoKey:   settingsEchoValue,
	}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal settings.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0o600); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	unsetSettingsEnv(t, settingsSecretKey)

	on := true
	root := t.TempDir()
	s := newACPServiceAgent(t, root, acptest.Options{EnvPrint: []string{settingsEchoKey}}, nil,
		func(ac *config.AgentConfig) {
			ac.ACP = &config.ACPConfig{ClaudeSettingsEnv: &on}
		})

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}

	// The inheritance really happened: the agent saw the key it was asked to report.
	stderr := acpStderr(t, root, final.ID)
	if !strings.Contains(stderr, "acptest: env "+settingsEchoKey+"="+settingsEchoValue) {
		t.Fatalf("the agent never received the settings env (scan below would be vacuous):\n%s", stderr)
	}
	// And the two columns scanned below really exist: scanning an empty string would
	// "pass" while proving nothing about request_json / rendered_command.
	if final.RequestJSON == "" || final.RenderedCommand == "" {
		t.Fatalf("request_json=%q rendered_command=%q, want both populated", final.RequestJSON, final.RenderedCommand)
	}

	// 1. The job row: everything the API can serve, plus the two audit-only columns.
	row, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("marshal job row: %v", err)
	}
	for name, blob := range map[string]string{
		"job row":          string(row),
		"request_json":     final.RequestJSON,
		"artifacts_json":   final.ArtifactsJSON,
		"rendered_command": final.RenderedCommand,
	} {
		if strings.Contains(blob, settingsSecretValue) {
			t.Errorf("%s carries the settings VALUE:\n%s", name, blob)
		}
	}

	// 2. The event log (the timeline an operator and the web both read).
	events, err := s.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, e := range events {
		if strings.Contains(e.Detail, settingsSecretValue) {
			t.Errorf("event %s carries the settings VALUE: %s", e.Type, e.Detail)
		}
	}

	// 3. Everything the job left on disk — stdout.log, stderr.log, acp.jsonl, every
	// artifact. The key NAMES may appear (the runner logs how many it added), the
	// value may not.
	err = filepath.WalkDir(final.ResultDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		blob, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(blob), settingsSecretValue) {
			t.Errorf("%s carries the settings VALUE", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk result dir: %v", err)
	}
}

// unsetSettingsEnv removes the named variables from this process for the rest of the
// test: a developer machine with a real ANTHROPIC_API_KEY exported would otherwise make
// the merge treat it as "already set" and skip the settings key — and the "value never
// persisted" scan would pass because nothing was ever injected. The runner package's
// F14 tests need the same isolation (internal/runner/acp/claude_settings_test.go).
func unsetSettingsEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if old, ok := os.LookupEnv(name); ok {
			t.Cleanup(func() { _ = os.Setenv(name, old) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(name) })
		}
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}
