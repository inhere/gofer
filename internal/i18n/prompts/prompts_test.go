package prompts

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// cjk matches a Han character: an English prompt must carry none.
var cjk = regexp.MustCompile(`[\p{Han}]`)

// goldenData is the fixed data every template is rendered with.
var goldenData = map[string]any{
	NameAcceptance: AcceptanceData{Acceptance: "- tests pass\n- no CJK in the prompt"},
	NameContract:   ContractData{Scope: true, Knowledge: true, ScopeList: "internal/job/**, docs/**"},
	NameDeadline:   DeadlineData{Limit: "30 min", Env: "GOFER_JOB_DEADLINE"},
}

// TestRenderGolden pins the rendered text of every template in both languages.
//
// The zh-CN files are a VERBATIM copy of the text gofer injects before the i18n
// migration: a change here changes what every existing (zh-CN) deployment's agent
// reads, so it must be deliberate.
func TestRenderGolden(t *testing.T) {
	t.Parallel()
	for _, name := range Names() {
		data := goldenData[name]
		if data == nil {
			t.Fatalf("%s: no golden data — add one with the template", name)
		}
		for _, lang := range i18n.Supported() {
			got, err := Render(lang, name, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", lang, name, err)
			}
			want := readGolden(t, lang, name)
			if got != want {
				t.Errorf("%s/%s differs from its golden file\n--- got ---\n%s\n--- want ---\n%s", lang, name, got, want)
			}
		}
	}
}

// TestEnglishTemplatesHaveNoCJK: the English text must stand on its own — a
// leftover Chinese line would make an English prompt mixed (G046).
func TestEnglishTemplatesHaveNoCJK(t *testing.T) {
	t.Parallel()
	for _, name := range Names() {
		got := MustRender(i18n.EN, name, goldenData[name])
		if cjk.MatchString(got) {
			t.Errorf("%s (en) carries CJK: %s", name, cjk.FindString(got))
		}
	}
}

// TestTemplatesMatchAcrossLanguages: same names, same placeholders. A template
// nobody translated, or one that references a field its sibling does not, is a
// prompt that diverges silently.
func TestTemplatesMatchAcrossLanguages(t *testing.T) {
	t.Parallel()
	en, zh := Names(), make([]string, 0)
	for _, lang := range i18n.Supported() {
		if len(en) != len(templateNames(lang)) {
			t.Fatalf("%s has a different template set: %v vs %v", lang, templateNames(lang), en)
		}
	}
	zh = templateNames(i18n.ZhCN)
	if strings.Join(en, ",") != strings.Join(zh, ",") {
		t.Fatalf("template names differ: en %v, zh-CN %v", en, zh)
	}
	for _, name := range en {
		enFields, zhFields := Fields(i18n.EN, name), Fields(i18n.ZhCN, name)
		if strings.Join(enFields, ",") != strings.Join(zhFields, ",") {
			t.Errorf("%s: variables differ: en %v, zh-CN %v", name, enFields, zhFields)
		}
	}
}

// templateNames lists the templates one language actually carries, by asking the
// parse result (a name it lacks would have failed at startup, this is the guard
// that the test itself notices).
func templateNames(lang i18n.Lang) []string {
	out := make([]string, 0, len(parsed))
	for name, byLang := range parsed {
		if byLang[i18n.Resolve(lang)] != nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestUnknownTemplate pins the error path: a typo must not silently render "".
func TestUnknownTemplate(t *testing.T) {
	t.Parallel()
	if _, err := Render(i18n.EN, "no-such-template", nil); err == nil {
		t.Fatal("Render of an unknown template returned no error")
	}
}

// TestMarkerIsTheTemplateHeading: the protocol heading a parser looks for is the
// heading the template actually writes, in every language.
func TestMarkerIsTheTemplateHeading(t *testing.T) {
	t.Parallel()
	markers := map[string]i18n.Section{
		NameAcceptance: i18n.SectionAcceptance,
		NameContract:   i18n.SectionContract,
		NameDeadline:   i18n.SectionDeadline,
	}
	for name, section := range markers {
		for _, lang := range i18n.Supported() {
			text := MustRender(lang, name, goldenData[name])
			if want := i18n.Title(lang, section); !strings.HasPrefix(text, want) {
				t.Errorf("%s/%s starts with %q, want the %s heading %q", lang, name,
					firstLine(text), lang, want)
			}
		}
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// readGolden reads testdata/<lang>/<name>.md. A missing file is WRITTEN with the
// current text and the test fails, so a new template's baseline is reviewed in the
// diff instead of being invented here.
func readGolden(t *testing.T, lang i18n.Lang, name string) string {
	t.Helper()
	file := filepath.Join("testdata", string(lang), name+".md")
	raw, err := os.ReadFile(file)
	if err == nil {
		return strings.TrimRight(string(raw), "\n")
	}
	out, rerr := Render(lang, name, goldenData[name])
	if rerr != nil {
		t.Fatalf("%s: %v", file, rerr)
	}
	if werr := os.MkdirAll(filepath.Dir(file), 0o755); werr != nil {
		t.Fatalf("create %s: %v", filepath.Dir(file), werr)
	}
	if werr := os.WriteFile(file, []byte(out+"\n"), 0o644); werr != nil {
		t.Fatalf("write %s: %v", file, werr)
	}
	t.Fatalf("%s did not exist: written with the current text, review and commit it", file)
	return ""
}
