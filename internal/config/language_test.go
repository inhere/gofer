package config

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// TestLanguageResolution pins design §3: server.language, overridden per project,
// every accepted alias reading as its canonical value and the DEPRECATED default
// (zh-CN) applying when nothing is set.
func TestLanguageResolution(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Server: ServerConfig{Language: "zh"}, // alias of zh-CN
		Projects: map[string]ProjectConfig{
			"en":     {Language: "english"}, // alias of en
			"unset":  {},                    // -> server.language
			"broken": {Language: "klingon"}, // unrecognized: reads as unset
		},
	}
	if got := cfg.Language(); got != i18n.ZhCN {
		t.Errorf("Language() = %q, want zh-CN (alias normalized)", got)
	}
	if got := cfg.LanguageFor("en"); got != i18n.EN {
		t.Errorf("LanguageFor(en) = %q, want en", got)
	}
	for _, project := range []string{"unset", "broken", "missing"} {
		if got := cfg.LanguageFor(project); got != i18n.ZhCN {
			t.Errorf("LanguageFor(%s) = %q, want the server language zh-CN", project, got)
		}
	}
	if cfg.LanguageUnset() {
		t.Error("LanguageUnset = true with server.language set")
	}
	empty := &Config{}
	if !empty.LanguageUnset() || empty.Language() != i18n.Default {
		t.Errorf("unset config: LanguageUnset=%v Language=%q", empty.LanguageUnset(), empty.Language())
	}
	if got := (*Config)(nil).LanguageFor("x"); got != i18n.Default {
		t.Errorf("nil config LanguageFor = %q, want the default", got)
	}
}

// TestApplyDefaultsCanonicalizesLanguage: what an in-memory config hands to a save
// is the canonical value, not the alias the file carried.
func TestApplyDefaultsCanonicalizesLanguage(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Server:   ServerConfig{Language: "zh_CN.UTF-8"},
		Projects: map[string]ProjectConfig{"p": {Language: "en_US"}},
	}
	ApplyDefaults(cfg)
	if cfg.Server.Language != string(i18n.ZhCN) {
		t.Errorf("server.language = %q, want %q", cfg.Server.Language, i18n.ZhCN)
	}
	if got := cfg.Projects["p"].Language; got != string(i18n.EN) {
		t.Errorf("projects.p.language = %q, want %q", got, i18n.EN)
	}
}

// TestValidateLanguage: an unrecognized value fails the load and names what does
// exist; an empty one is legal (the DEPRECATED default).
func TestValidateLanguage(t *testing.T) {
	t.Parallel()
	ok := &Config{Server: ServerConfig{Language: "EN"}, Projects: map[string]ProjectConfig{"p": {HostPath: "/tmp", Language: "简体中文"}}}
	if err := Validate(ok); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := Validate(&Config{Server: ServerConfig{Language: ""}}); err != nil {
		t.Fatalf("an unset language must be legal: %v", err)
	}
	err := Validate(&Config{Server: ServerConfig{Language: "de-DE"}})
	if err == nil || !strings.Contains(err.Error(), "en, zh-CN") {
		t.Fatalf("server.language error = %v, want the available values listed", err)
	}
	err = Validate(&Config{Projects: map[string]ProjectConfig{"p": {HostPath: "/tmp", Language: "fr"}}})
	if err == nil || !strings.Contains(err.Error(), `project "p"`) {
		t.Fatalf("project language error = %v", err)
	}
}
