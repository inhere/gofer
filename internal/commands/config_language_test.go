package commands

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/config"
	cfgpkg "github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
)

// validateOutput runs `config validate` on a config body and returns its output.
func validateLanguageOutput(t *testing.T, serverBlock string) string {
	t.Helper()
	t.Setenv(cfgpkg.EnvWorkspace, t.TempDir()+"/missing-workspace")
	host := t.TempDir()
	body := serverBlock + "projects:\n  ok:\n    host_path: " + host + "\n    allowed_runners: [local]\n"
	c := bindCmd(NewConfigCmd().Subs[0])
	cfgpkg.InputCfgFile = writeRawConfig(t, body)
	t.Cleanup(func() { cfgpkg.InputCfgFile = "" })
	var err error
	out := captureOutput(t, func() { err = runConfigValidate(c, nil) })
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	return out
}

// TestConfigValidateShowsEffectiveLanguage: validate says which language is in
// effect and where it came from; an unset one adds the advisory note.
func TestConfigValidateShowsEffectiveLanguage(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
	t.Setenv(i18n.EnvLanguageVar, "en") // the CLI's own text: English, so the note is English

	out := validateLanguageOutput(t, "")
	if !strings.Contains(out, "server.language zh-CN (source: unset)") {
		t.Errorf("unset: the effective language and source are missing:\n%s", out)
	}
	if !strings.Contains(out, "chosen automatically") {
		t.Errorf("unset: the advisory note is missing:\n%s", out)
	}
	out = validateLanguageOutput(t, "server:\n  language: auto\n")
	if !strings.Contains(out, "server.language zh-CN (source: auto)") || strings.Contains(out, "chosen automatically") {
		t.Errorf("auto:\n%s", out)
	}
	out = validateLanguageOutput(t, "server:\n  language: en\n")
	if !strings.Contains(out, "server.language en (source: config)") {
		t.Errorf("pinned:\n%s", out)
	}
}

// TestInitConfigLanguageWritesAuto: the starter config asks for auto with a comment
// and never bakes in the machine's language; a legacy template line is rewritten.
func TestInitConfigLanguageWritesAuto(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	t.Setenv(i18n.EnvLanguageVar, "")

	got := initConfigLanguage(configtmpl.ExampleYAML)
	if !strings.Contains(got, "\n  language: auto\n") || strings.Contains(got, "language: zh-CN") {
		t.Errorf("starter config does not ask for auto:\n%s", got[:min(len(got), 800)])
	}
	if strings.Count(got, "language:") != strings.Count(configtmpl.ExampleYAML, "language:") {
		t.Errorf("the language line count changed")
	}
	for _, legacy := range []string{"server:\n  language: zh-CN\n  addr: x\n", "server:\n  language: \"\"\n  addr: x\n"} {
		out := initConfigLanguage(legacy)
		if !strings.Contains(out, "\n  language: auto\n") || !strings.Contains(out, "# auto =") || !strings.Contains(out, "addr: x") {
			t.Errorf("legacy template %q rewritten to %q", legacy, out)
		}
	}
	const noLine = "server:\n  addr: x\n"
	if out := initConfigLanguage(noLine); out != noLine {
		t.Errorf("a template without the line changed: %q", out)
	}
}
