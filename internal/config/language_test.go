package config

import (
	"runtime"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"

	configtmpl "github.com/inhere/gofer/config"
	"github.com/inhere/gofer/internal/i18n"
)

// TestLanguageResolution pins design §3: server.language, overridden per project,
// every accepted alias reading as its canonical value and auto applying when
// nothing is set.
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
	if !empty.LanguageUnset() || empty.Language() != i18n.Auto() {
		t.Errorf("unset config: LanguageUnset=%v Language=%q", empty.LanguageUnset(), empty.Language())
	}
	if got := (*Config)(nil).LanguageFor("x"); got != i18n.Auto() {
		t.Errorf("nil config LanguageFor = %q, want auto", got)
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
// exist; an empty one and auto are legal (both read as auto).
func TestValidateLanguage(t *testing.T) {
	t.Parallel()
	ok := &Config{Server: ServerConfig{Language: "EN"}, Projects: map[string]ProjectConfig{"p": {HostPath: "/tmp", Language: "简体中文"}}}
	if err := Validate(ok); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := Validate(&Config{Server: ServerConfig{Language: ""}}); err != nil {
		t.Fatalf("an unset language must be legal: %v", err)
	}
	auto := &Config{Server: ServerConfig{Language: " AUTO "}, Projects: map[string]ProjectConfig{"p": {HostPath: "/tmp", Language: "auto"}}}
	if err := Validate(auto); err != nil {
		t.Fatalf("auto must be legal: %v", err)
	}
	err := Validate(&Config{Server: ServerConfig{Language: "de-DE"}})
	if err == nil || !strings.Contains(err.Error(), "auto, en, zh-CN") {
		t.Fatalf("server.language error = %v, want the available values listed", err)
	}
	err = Validate(&Config{Projects: map[string]ProjectConfig{"p": {HostPath: "/tmp", Language: "fr"}}})
	if err == nil || !strings.Contains(err.Error(), `project "p"`) {
		t.Fatalf("project language error = %v", err)
	}
}

// setSystemLanguage points the machine's language at lang for one test (LC_ALL is
// the highest-priority source on every platform). Not for parallel tests.
func setSystemLanguage(t *testing.T, locale string) {
	t.Helper()
	t.Setenv("LC_ALL", locale)
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
}

// TestLanguageAutoFollowsTheSystem: an unset language and an explicit auto are the
// same thing — the server machine's language, en when it has none gofer supports —
// and a project that names nothing inherits that; a project that pins keeps its pin.
func TestLanguageAutoFollowsTheSystem(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Language: "auto"},
		Projects: map[string]ProjectConfig{
			"inherit": {},
			"auto":    {Language: "AUTO"},
			"pinned":  {Language: "en"},
		},
	}
	unset := &Config{Projects: map[string]ProjectConfig{"inherit": {}}}

	setSystemLanguage(t, "zh_CN.UTF-8")
	for name, c := range map[string]*Config{"auto": cfg, "unset": unset} {
		if got := c.Language(); got != i18n.ZhCN {
			t.Errorf("%s, Chinese system: Language() = %q, want zh-CN", name, got)
		}
		if got := c.LanguageFor("inherit"); got != i18n.ZhCN {
			t.Errorf("%s, Chinese system: LanguageFor(inherit) = %q, want zh-CN", name, got)
		}
	}
	if got := cfg.LanguageFor("auto"); got != i18n.ZhCN {
		t.Errorf("project auto, Chinese system = %q, want zh-CN", got)
	}
	if got := cfg.LanguageFor("pinned"); got != i18n.EN {
		t.Errorf("pinned project = %q, want en", got)
	}

	if runtime.GOOS != "windows" { // Windows would add its UI language after the variables
		setSystemLanguage(t, "fr_FR.UTF-8")
		for name, c := range map[string]*Config{"auto": cfg, "unset": unset} {
			if got := c.Language(); got != i18n.EN {
				t.Errorf("%s, unsupported system language: Language() = %q, want en", name, got)
			}
		}
		if got := cfg.LanguageFor("auto"); got != i18n.EN {
			t.Errorf("project auto, unsupported system language = %q, want en", got)
		}
	}

	// A pinned server language ignores the machine; a project auto still reads it.
	pinned := &Config{Server: ServerConfig{Language: "en"}, Projects: map[string]ProjectConfig{"a": {Language: "auto"}, "b": {}}}
	setSystemLanguage(t, "zh_CN.UTF-8")
	if got := pinned.Language(); got != i18n.EN {
		t.Errorf("pinned server = %q, want en", got)
	}
	if got := pinned.LanguageFor("b"); got != i18n.EN {
		t.Errorf("inheriting project = %q, want the pinned en", got)
	}
	if got := pinned.LanguageFor("a"); got != i18n.ZhCN {
		t.Errorf("project auto = %q, want the machine's zh-CN", got)
	}
}

func TestLanguageSource(t *testing.T) {
	t.Parallel()
	for want, c := range map[string]*Config{
		"unset":  {},
		"auto":   {Server: ServerConfig{Language: "Auto"}},
		"config": {Server: ServerConfig{Language: "zh"}},
	} {
		if got := c.LanguageSource(); got != want {
			t.Errorf("LanguageSource(%q) = %q, want %q", c.Server.Language, got, want)
		}
	}
	if got := (*Config)(nil).LanguageSource(); got != "unset" {
		t.Errorf("nil config source = %q", got)
	}
}

// TestAutoSurvivesCanonicalize: "auto" is stored as auto (any spelling).
// and the shipped example config asks for it.
func TestAutoSurvivesCanonicalize(t *testing.T) {
	t.Parallel()
	cfg := &Config{Server: ServerConfig{Language: " Auto "}, Projects: map[string]ProjectConfig{"p": {Language: "AUTO"}}}
	ApplyDefaults(cfg)
	if cfg.Server.Language != "auto" || cfg.Projects["p"].Language != "auto" {
		t.Errorf("stored languages = %q / %q, want auto", cfg.Server.Language, cfg.Projects["p"].Language)
	}
}

// TestExampleConfigAsksForAuto: the starter config names auto explicitly, so a
// fresh install follows the server machine's language.
func TestExampleConfigAsksForAuto(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(configtmpl.ExampleYAML), cfg); err != nil {
		t.Fatalf("decode example: %v", err)
	}
	if cfg.Server.Language != "auto" {
		t.Errorf("example server.language = %q, want auto", cfg.Server.Language)
	}
}
