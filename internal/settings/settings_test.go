package settings

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// openStore opens a metadata store in a temp dir for the service under test.
func openStore(t *testing.T) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestRegistryIsWellFormed: every key is unique, typed, described, and its default passes
// its own validation — a registry row with a default the store would refuse is a bug the
// listing could not even render.
func TestRegistryIsWellFormed(t *testing.T) {
	seen := make(map[string]struct{}, len(registry))
	for _, d := range registry {
		if _, dup := seen[d.Key]; dup {
			t.Errorf("%s is registered twice", d.Key)
		}
		seen[d.Key] = struct{}{}
		if !d.Kind.Valid() {
			t.Errorf("%s: unknown kind %q", d.Key, d.Kind)
		}
		if d.Group == "" || d.Description == "" {
			t.Errorf("%s: group / description must be set", d.Key)
		}
		if d.Kind == KindInt && d.Min > d.Max {
			t.Errorf("%s: min %d > max %d", d.Key, d.Min, d.Max)
		}
		raw, err := encode(d, d.Default)
		if err != nil {
			t.Errorf("%s: the default is refused by its own validation: %v", d.Key, err)
			continue
		}
		v, err := decode(d, raw)
		if err != nil {
			t.Errorf("%s: decode of the encoded default: %v", d.Key, err)
			continue
		}
		if d.Kind == KindStrings || d.Kind == KindPatterns {
			if _, ok := v.([]string); !ok {
				t.Errorf("%s: decoded default is %T, want []string", d.Key, v)
			}
		}
	}
	// The first batch the design names is registered.
	for _, key := range []string{
		KeyWorkDraftMinPrompts, KeyWorkDraftExcludeProjects, KeyWorkDraftExcludeCwds,
		KeyWorkDraftExcludeTitlePatterns, KeyWorkShortSessionMaxMinutes, KeyWorkShortSessionMaxPrompts,
		KeyPromptStewardRole, KeyPromptStewardRoleExtra, KeyPromptStewardReview,
		KeyPromptStewardReviewExtra, KeyPromptWorkSummarizer, KeyPromptWorkSummarizerExtra,
	} {
		if _, ok := Lookup(key); !ok {
			t.Errorf("key %s is not registered", key)
		}
	}
}

// TestDefaultsWhenUnset: a key without a stored row reads its default, and the listing
// reports modified=false.
func TestDefaultsWhenUnset(t *testing.T) {
	svc := NewService(openStore(t))
	if got, err := svc.GetInt(KeyWorkDraftMinPrompts); err != nil || got != 2 {
		t.Fatalf("default draft min_prompts = %d, %v; want 2", got, err)
	}
	if got, err := svc.GetInt(KeyWorkShortSessionMaxMinutes); err != nil || got != 10 {
		t.Fatalf("default short_session.max_minutes = %d, %v; want 10", got, err)
	}
	if got, err := svc.GetInt(KeyWorkShortSessionMaxPrompts); err != nil || got != 3 {
		t.Fatalf("default short_session.max_prompts = %d, %v; want 3", got, err)
	}
	if got, err := svc.GetStrings(KeyWorkDraftExcludeProjects); err != nil || len(got) != 0 {
		t.Fatalf("default exclude_projects = %v, %v; want empty", got, err)
	}
	if got, err := svc.GetString(KeyPromptWorkSummarizerExtra); err != nil || got != "" {
		t.Fatalf("default summarizer extra = %q, %v; want empty", got, err)
	}
	items, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range items {
		if item.Modified {
			t.Errorf("%s is marked modified on a fresh store", item.Key)
		}
	}
	if _, err := svc.Get("work.nope"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Get(unknown) = %v, want ErrUnknownKey", err)
	}
}

// TestValidation: the type and the bounds are enforced on write, a bad regex is refused
// with the compiler's reason, and the prompt cap holds.
func TestValidation(t *testing.T) {
	svc := NewService(openStore(t))
	cases := []struct {
		name  string
		key   string
		value any
		want  string
	}{
		{"int below the minimum", KeyWorkDraftMinPrompts, 0, "between 1 and 20"},
		{"int above the maximum", KeyWorkDraftMinPrompts, 21, "between 1 and 20"},
		{"int is not a number", KeyWorkDraftMinPrompts, "many", "whole number"},
		{"int is a fraction", KeyWorkDraftMinPrompts, 2.5, "whole number"},
		{"short session minutes above the maximum", KeyWorkShortSessionMaxMinutes, 1441, "between 0 and 1440"},
		{"short session prompts above the maximum", KeyWorkShortSessionMaxPrompts, 101, "between 0 and 100"},
		{"regex does not compile", KeyWorkDraftExcludeTitlePatterns, []any{"ok", "("}, "does not compile"},
		{"pattern list given a number", KeyWorkDraftExcludeCwds, 7, "list of strings"},
		{"prompt over the cap", KeyPromptWorkSummarizer, strings.Repeat("x", MaxPromptLen+1), "longer than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := svc.Set(tc.key, tc.value, "alice")
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("Set(%s, %v) = %v, want a ValidationError", tc.key, tc.value, err)
			}
			if !strings.Contains(invalid.Reason, tc.want) {
				t.Errorf("reason = %q, want it to contain %q", invalid.Reason, tc.want)
			}
			// Nothing was stored: the default still applies.
			if items, err := svc.List(); err != nil {
				t.Fatalf("List: %v", err)
			} else {
				for _, item := range items {
					if item.Key == tc.key && item.Modified {
						t.Errorf("%s was stored despite the refusal", tc.key)
					}
				}
			}
		})
	}
	// The bounds themselves are accepted.
	if _, _, err := svc.Set(KeyWorkDraftMinPrompts, 1, "alice"); err != nil {
		t.Errorf("Set(min boundary) = %v", err)
	}
	if _, _, err := svc.Set(KeyWorkShortSessionMaxMinutes, 0, "alice"); err != nil {
		t.Errorf("Set(0 minutes) = %v", err)
	}
}

// TestSetIsImmediateInProcess: a write of this process is visible to the next read without
// waiting for the cache TTL.
func TestSetIsImmediateInProcess(t *testing.T) {
	svc := NewService(openStore(t))
	// Read once so the cache is populated with the default.
	if got, _ := svc.GetInt(KeyWorkDraftMinPrompts); got != 2 {
		t.Fatalf("default = %d, want 2", got)
	}
	item, ver, err := svc.Set(KeyWorkDraftMinPrompts, 5, "alice")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if ver == 0 {
		t.Error("Set reported generation 0")
	}
	if !item.Modified || item.Value != 5 || item.UpdatedBy != "alice" {
		t.Errorf("item = %+v, want 5 by alice, modified", item)
	}
	if got, err := svc.GetInt(KeyWorkDraftMinPrompts); err != nil || got != 5 {
		t.Fatalf("read right after the write = %d, %v; want 5", got, err)
	}
}

// TestCrossProcessWriteIsPickedUp: another process's write (a second Service over the same
// store) is invisible inside the TTL and visible after it — the same 5s contract the
// authorization cache has.
func TestCrossProcessWriteIsPickedUp(t *testing.T) {
	st := openStore(t)
	writer := NewService(st)
	reader := NewService(st)
	now := time.Unix(1700000000, 0)
	for _, svc := range []*Service{writer, reader} {
		svc.SetClock(func() time.Time { return now })
	}
	if got, err := reader.GetInt(KeyWorkDraftMinPrompts); err != nil || got != 2 {
		t.Fatalf("first read = %d, %v; want the default 2", got, err)
	}
	if _, _, err := writer.Set(KeyWorkDraftMinPrompts, 7, "bob"); err != nil {
		t.Fatalf("writer Set: %v", err)
	}
	if got, _ := reader.GetInt(KeyWorkDraftMinPrompts); got != 2 {
		t.Fatalf("reader saw %d inside the ttl, want the cached 2", got)
	}
	now = now.Add(6 * time.Second)
	if got, err := reader.GetInt(KeyWorkDraftMinPrompts); err != nil || got != 7 {
		t.Fatalf("reader after the ttl = %d, %v; want 7", got, err)
	}
}

// TestResetRestoresDefault: reset drops the stored value and the default applies again;
// resetting an unmodified key is a no-op.
func TestResetRestoresDefault(t *testing.T) {
	svc := NewService(openStore(t))
	if _, _, err := svc.Set(KeyWorkShortSessionMaxPrompts, 9, "alice"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	existed, err := svc.Reset(KeyWorkShortSessionMaxPrompts)
	if err != nil || !existed {
		t.Fatalf("Reset = %t, %v; want true, no error", existed, err)
	}
	if got, err := svc.GetInt(KeyWorkShortSessionMaxPrompts); err != nil || got != 3 {
		t.Fatalf("after reset = %d, %v; want the default 3", got, err)
	}
	existed, err = svc.Reset(KeyWorkShortSessionMaxPrompts)
	if err != nil || existed {
		t.Fatalf("second Reset = %t, %v; want false, no error", existed, err)
	}
}

// TestPatternsCompiled: a saved pattern list reads back as compiled expressions.
func TestPatternsCompiled(t *testing.T) {
	svc := NewService(openStore(t))
	if _, _, err := svc.Set(KeyWorkDraftExcludeTitlePatterns, []any{"^scratch", "smoke$"}, "alice"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	res, err := svc.Patterns(KeyWorkDraftExcludeTitlePatterns)
	if err != nil {
		t.Fatalf("Patterns: %v", err)
	}
	if len(res) != 2 || !res[0].MatchString("scratch it") || !res[1].MatchString("a smoke") {
		t.Fatalf("patterns = %v, want two working expressions", res)
	}
}

// TestExportImport: the export carries only modified keys; import previews the changes
// without writing, then writes them (with the resets and the refused keys reported).
func TestExportImport(t *testing.T) {
	svc := NewService(openStore(t))
	if _, _, err := svc.Set(KeyWorkDraftMinPrompts, 4, "alice"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, _, err := svc.Set(KeyWorkDraftExcludeProjects, []any{"noise"}, "alice"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	doc, err := svc.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	text := string(doc)
	if !strings.Contains(text, `work.draft.min_prompts: 4`) || !strings.Contains(text, `work.draft.exclude_projects: ["noise"]`) {
		t.Fatalf("export = %q, want the two modified keys", text)
	}
	if strings.Contains(text, "prompt.") {
		t.Fatalf("export = %q, want no unmodified keys", text)
	}

	// Dry run first: it reports what would change and writes nothing.
	preview, err := svc.Import([]byte("work.draft.min_prompts: 6\nwork.short_session.max_prompts: 50\n"), true, "bob")
	if err != nil {
		t.Fatalf("Import dry run: %v", err)
	}
	if !preview.DryRun || preview.Applied {
		t.Errorf("preview = %+v, want DryRun true, Applied false", preview)
	}
	if got, _ := svc.GetInt(KeyWorkDraftMinPrompts); got != 4 {
		t.Fatalf("dry run wrote the value: %d, want 4", got)
	}

	res, err := svc.Import([]byte("work.draft.min_prompts: 6\nwork.short_session.max_prompts: 500\n"), false, "bob")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Errors != 1 || res.Applied {
		t.Errorf("result = %+v, want one refused key and Applied false", res)
	}
	if got, _ := svc.GetInt(KeyWorkDraftMinPrompts); got != 6 {
		t.Errorf("valid key was not written: %d, want 6", got)
	}
	if got, _ := svc.GetInt(KeyWorkShortSessionMaxPrompts); got != 3 {
		t.Errorf("refused key was written: %d, want the default 3", got)
	}

	// A null value resets a modified key.
	res, err = svc.Import([]byte("work.draft.min_prompts: null\n"), false, "bob")
	if err != nil {
		t.Fatalf("Import reset: %v", err)
	}
	if len(res.Changes) != 1 || res.Changes[0].Action != "reset" {
		t.Fatalf("changes = %+v, want one reset", res.Changes)
	}
	if got, _ := svc.GetInt(KeyWorkDraftMinPrompts); got != 2 {
		t.Errorf("after the reset = %d, want the default 2", got)
	}

	// A document that is not YAML is an error of the whole import.
	if _, err := svc.Import([]byte("key: [unclosed\n"), false, "bob"); err == nil {
		t.Error("Import of a broken document returned no error")
	}
}
