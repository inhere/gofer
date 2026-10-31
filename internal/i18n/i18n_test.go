package i18n

import (
	"context"
	"io/fs"
	"path"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestNormalize covers EVERY alias table entry plus the forms that must not be
// understood: the alias table is the contract every entry point relies on.
func TestNormalize(t *testing.T) {
	t.Parallel()
	for alias, want := range Aliases {
		if got, ok := Normalize(alias); !ok || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q, true", alias, got, ok, want)
		}
	}
	cases := map[string]Lang{
		// Case, separators, encoding and modifier are all irrelevant.
		"EN":             EN,
		"en_US.UTF-8":    EN,
		"en-US":          EN,
		"en_US":          EN,
		"  zh_cn.utf8  ": ZhCN,
		"zh_CN.UTF-8":    ZhCN,
		"zh-Hans-CN":     ZhCN,
		"zh-Hant":        ZhCN,
		"zh_TW.UTF-8@x":  ZhCN,
		"zh-SG":          ZhCN,
		"C":              EN,
		"POSIX":          EN,
		"English":        EN,
		"中文":             ZhCN,
		"简体中文":           ZhCN,
		// A regional variant of a known base language reads as that base.
		"en-AU": EN,
		"zh-MO": ZhCN,
	}
	for in, want := range cases {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", "de", "fr-FR", "klingon", "xx-YY"} {
		if got, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) = %q, want not understood", in, got)
		}
	}
}

// TestFromAcceptLanguage pins the q-value order: the FIRST understood language by
// q wins, a header without q keeps its written order, and "*" names nothing.
func TestFromAcceptLanguage(t *testing.T) {
	t.Parallel()
	cases := map[string]Lang{
		"zh-CN,zh;q=0.9,en;q=0.8":        ZhCN,
		"en-US,en;q=0.9,zh-CN;q=0.8":     EN,
		"zh-CN;q=0.5,en;q=0.9":           EN,
		"de-DE,fr;q=0.9,zh-CN;q=0.8":     ZhCN,
		"*":                              "",
		"*;q=0.9,zh-CN;q=0.1":            ZhCN,
		"de-DE":                          "",
		"":                               "",
		"en-GB;q=0.4,en-US;q=0.6,zh;q=0": EN,
		"zh-CN;q=0":                      "",
		"zh-CN;q=0,en;q=0.1":             EN,
	}
	for in, want := range cases {
		if got := FromAcceptLanguage(in); got != want {
			t.Errorf("FromAcceptLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolve: the first understood candidate wins, and nothing understood means
// the legacy Default.
func TestResolve(t *testing.T) {
	t.Parallel()
	if got := Resolve("", "zh-CN", "en"); got != ZhCN {
		t.Errorf("Resolve = %q, want zh-CN", got)
	}
	if got := Resolve(EN, ZhCN); got != EN {
		t.Errorf("Resolve = %q, want en", got)
	}
	if got := Resolve("", ""); got != Default {
		t.Errorf("Resolve(empty) = %q, want the default", got)
	}
	if got := Resolve("klingon"); got != Default {
		t.Errorf("Resolve(unrecognized) = %q, want the default", got)
	}
}

// TestFromSystemAndEnv: LC_ALL wins over LANG, GOFER_LANG wins over both.
func TestFromSystemAndEnv(t *testing.T) {
	t.Setenv("GOFER_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
	if got := FromSystem(); got != "" && !slices.Contains(Supported(), got) {
		t.Errorf("FromSystem with no locale = %q, want no language (or the machine's UI language)", got)
	}
	t.Setenv("LANG", "zh_CN.UTF-8")
	if got := FromSystem(); got != ZhCN {
		t.Errorf("FromSystem(LANG=zh_CN.UTF-8) = %q, want zh-CN", got)
	}
	t.Setenv("LC_ALL", "C")
	if got := FromSystem(); got != EN {
		t.Errorf("FromSystem(LC_ALL=C, LANG=zh) = %q, want en", got)
	}
	t.Setenv(EnvLanguageVar, "zh-CN")
	if got := FromEnv(); got != ZhCN {
		t.Errorf("FromEnv(GOFER_LANG=zh-CN, LC_ALL=C) = %q, want zh-CN", got)
	}
	t.Setenv(EnvLanguageVar, "en")
	if got := FromEnv(); got != EN {
		t.Errorf("FromEnv = %q, want en", got)
	}
}

// TestCatalogKeysMatch keeps the two catalogs in step: a key one language has and
// the other does not is a text that would silently fall back to English. It also
// proves every key of both files is actually LOADED (translator.HasKey).
func TestCatalogKeysMatch(t *testing.T) {
	t.Parallel()
	en, zh := catalogKeys(t, EN), catalogKeys(t, ZhCN)
	for _, key := range en {
		if !slices.Contains(zh, key) {
			t.Errorf("key %q is missing from zh-CN", key)
		}
	}
	for _, key := range zh {
		if !slices.Contains(en, key) {
			t.Errorf("key %q is missing from en", key)
		}
	}
	if len(en) == 0 {
		t.Fatal("no keys found in the en catalog")
	}
	// Every key must actually translate to itself+: a key the loader dropped would
	// read back as its own name.
	for _, lang := range Supported() {
		for _, key := range catalogKeys(t, lang) {
			if got := T(lang, key); got == "" || got == key {
				t.Errorf("%s: key %q is in the file but does not translate", lang, key)
			}
		}
	}
}

// catalogKeys returns the keys the ini FILES under lang/ declare.
func catalogKeys(t *testing.T, lang Lang) []string {
	t.Helper()
	dir := path.Join("lang", string(lang))
	entries, err := fs.ReadDir(langFS, dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, ent := range entries {
		raw, err := fs.ReadFile(langFS, path.Join(dir, ent.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", ent.Name(), err)
		}
		section := ""
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
				continue
			}
			key, _, ok := strings.Cut(line, "=")
			if !ok {
				t.Fatalf("%s: not a key=value line: %q", ent.Name(), line)
			}
			key = strings.TrimSpace(key)
			if section != "" {
				key = section + "." + key
			}
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// TestTReplacesNamedArgs: placeholders are named, so a language may reorder them.
func TestTReplacesNamedArgs(t *testing.T) {
	t.Parallel()
	if got := T(ZhCN, "job.deadline.minutes", "n", 30); got != "30 分钟" {
		t.Errorf("zh minutes = %q", got)
	}
	if got := T(EN, "job.deadline.minutes", "n", 30); got != "30 min" {
		t.Errorf("en minutes = %q", got)
	}
	if got := T(EN, "job.delivery.more_commits", "n", 3); got != "…and 3 more" {
		t.Errorf("en more_commits = %q", got)
	}
	// A key missing from the requested language falls back to English.
	if got := T(ZhCN, "no.such.key"); got != "no.such.key" {
		t.Errorf("unknown key = %q, want the key itself", got)
	}
}

// TestDefaultIsNotTheUnsetLanguage: Default is only the baseline of code that
// carries no language at all; a language nobody named is Auto(), and the note says so.
func TestDefaultIsNotTheUnsetLanguage(t *testing.T) {
	old := detectSystem
	t.Cleanup(func() { detectSystem = old })
	detectSystem = func() []Lang { return nil }
	if got := Auto(); got != EN {
		t.Errorf("Auto() with no system language = %q, want en", got)
	}
	if got := List(); got != "en, zh-CN" {
		t.Errorf("List = %q", got)
	}
	for _, l := range Supported() {
		note := LanguageDefaultNoteIn(l)
		if note == "cli.setup.language.unset_note" || !strings.Contains(note, "server.language") {
			t.Errorf("LanguageDefaultNoteIn(%s) = %q", l, note)
		}
	}
	if !strings.Contains(LanguageDefaultNote, "auto") || strings.Contains(LanguageDefaultNote, "DEPRECATED") {
		t.Errorf("LanguageDefaultNote = %q", LanguageDefaultNote)
	}
	if !strings.Contains(LanguageDefaultNoteIn(ZhCN), "系统语言") {
		t.Errorf("zh-CN note = %q", LanguageDefaultNoteIn(ZhCN))
	}
}

// TestAutoFollowsTheSystemLanguage: auto is the machine's language when gofer has
// text for it, else en. The detector is injected so the Windows branch (UI
// language) is covered on every platform.
func TestAutoFollowsTheSystemLanguage(t *testing.T) {
	old := detectSystem
	t.Cleanup(func() { detectSystem = old })
	cases := []struct {
		name string
		got  []Lang
		want Lang
	}{
		{"chinese system", []Lang{ZhCN}, ZhCN},
		{"english system", []Lang{EN}, EN},
		{"first preference wins", []Lang{ZhCN, EN}, ZhCN},
		{"nothing detected", nil, EN},
	}
	for _, tc := range cases {
		detectSystem = func() []Lang { return tc.got }
		if got := Auto(); got != tc.want {
			t.Errorf("%s: Auto() = %q, want %q", tc.name, got, tc.want)
		}
	}
	detectSystem = func() []Lang { return nil }
	if got := FromSystem(); got != "" {
		t.Errorf("FromSystem() with nothing detected = %q, want empty", got)
	}
}

// TestAutoFromLocaleVariables drives the real detector through the POSIX locale
// variables: a Chinese locale is zh-CN; an unsupported one (or none) is en.
func TestAutoFromLocaleVariables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows also reads the UI language; covered by TestAutoFollowsTheSystemLanguage")
	}
	for _, tc := range []struct {
		env  string
		want Lang
	}{{"zh_CN.UTF-8", ZhCN}, {"en_US.UTF-8", EN}, {"fr_FR.UTF-8", EN}, {"", EN}} {
		t.Setenv("LC_ALL", tc.env)
		t.Setenv("LC_MESSAGES", "")
		t.Setenv("LANG", "")
		if got := Auto(); got != tc.want {
			t.Errorf("LC_ALL=%q: Auto() = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestIsAuto(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{"": true, "  ": true, "auto": true, "AUTO": true, " Auto ": true, "en": false, "zh-CN": false, "x": false} {
		if got := IsAuto(in); got != want {
			t.Errorf("IsAuto(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestFromEnvVarIgnoresTheSystemLocale: text for an agent follows the deployment, so an
// English machine locale alone must not pick English (CI runs under LANG=C.UTF-8).
func TestFromEnvVarIgnoresTheSystemLocale(t *testing.T) {
	t.Setenv(EnvLanguageVar, "")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("LANG", "en_US.UTF-8")
	if got := FromEnvVar(); got != "" {
		t.Fatalf("FromEnvVar() = %q with only a system locale, want \"\"", got)
	}
	t.Setenv(EnvLanguageVar, "english")
	if got := FromEnvVar(); got != EN {
		t.Fatalf("FromEnvVar() = %q with GOFER_LANG=english, want %q", got, EN)
	}
}

// TestLangContext pins the request-language carrier: a stored language comes
// back canonical, an empty / unknown one stores nothing, and a bare ctx answers "".
func TestLangContext(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	if got := FromContext(bg); got != "" {
		t.Errorf("FromContext(bare) = %q, want empty", got)
	}
	if got := FromContext(nil); got != "" { //nolint:staticcheck // nil ctx is tolerated on purpose
		t.Errorf("FromContext(nil) = %q, want empty", got)
	}
	if got := FromContext(WithLang(bg, "en_US")); got != EN {
		t.Errorf("FromContext(WithLang(en_US)) = %q, want %q", got, EN)
	}
	if got := FromContext(WithLang(bg, ZhCN)); got != ZhCN {
		t.Errorf("FromContext(WithLang(zh-CN)) = %q, want %q", got, ZhCN)
	}
	if got := FromContext(WithLang(bg, "klingon")); got != "" {
		t.Errorf("an unknown language must store nothing, got %q", got)
	}
	// An unknown language does not erase an earlier one.
	if got := FromContext(WithLang(WithLang(bg, EN), "")); got != EN {
		t.Errorf("WithLang(empty) overrode the stored language: %q", got)
	}
	if got := FromContextOr(bg, ZhCN); got != ZhCN {
		t.Errorf("FromContextOr(bare, zh-CN) = %q", got)
	}
	if got := FromContextOr(WithLang(bg, EN), ZhCN); got != EN {
		t.Errorf("FromContextOr(en, zh-CN) = %q, want en", got)
	}
}
