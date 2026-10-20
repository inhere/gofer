package jobstore

import (
	"path/filepath"
	"testing"
)

// openSettingsTestStore opens a fresh metadata store in a temp dir.
func openSettingsTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestSettingsRoundTrip: a written value reads back, a missing key is not an error (the
// caller falls back to the registry default) and a delete drops the row.
func TestSettingsRoundTrip(t *testing.T) {
	st := openSettingsTestStore(t)

	if _, ok, err := st.GetSetting("work.draft.min_prompts"); err != nil || ok {
		t.Fatalf("GetSetting(unwritten) = ok %t, err %v; want ok false, no error", ok, err)
	}
	if ver, err := st.SettingsVersion(); err != nil || ver != 0 {
		t.Fatalf("SettingsVersion(fresh) = %d, %v; want 0", ver, err)
	}

	ver, err := st.PutSetting("work.draft.min_prompts", "3", "alice")
	if err != nil {
		t.Fatalf("PutSetting: %v", err)
	}
	if ver != 1 {
		t.Fatalf("generation after the first write = %d, want 1", ver)
	}
	got, ok, err := st.GetSetting("work.draft.min_prompts")
	if err != nil || !ok {
		t.Fatalf("GetSetting after put: ok %t, err %v", ok, err)
	}
	if got.Value != "3" || got.UpdatedBy != "alice" || got.UpdatedAt == 0 {
		t.Fatalf("row = %+v, want value 3 by alice with a stamp", got)
	}

	// An overwrite bumps the generation again and keeps one row per key.
	if _, err := st.PutSetting("work.draft.min_prompts", "4", "bob"); err != nil {
		t.Fatalf("PutSetting overwrite: %v", err)
	}
	rows, err := st.ListSettings()
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	if len(rows) != 1 || rows[0].Value != "4" || rows[0].UpdatedBy != "bob" {
		t.Fatalf("rows = %+v, want one row with 4 by bob", rows)
	}
	if ver, err := st.SettingsVersion(); err != nil || ver != 2 {
		t.Fatalf("SettingsVersion = %d, %v; want 2", ver, err)
	}

	// Deleting the row brings the default back; deleting it again changes nothing and does
	// not bump the generation.
	existed, ver, err := st.DeleteSetting("work.draft.min_prompts")
	if err != nil || !existed {
		t.Fatalf("DeleteSetting = existed %t, err %v; want true, no error", existed, err)
	}
	if ver != 3 {
		t.Fatalf("generation after delete = %d, want 3", ver)
	}
	existed, ver, err = st.DeleteSetting("work.draft.min_prompts")
	if err != nil || existed || ver != 3 {
		t.Fatalf("second DeleteSetting = existed %t, ver %d, err %v; want false, 3, no error", existed, ver, err)
	}
	if _, ok, err := st.GetSetting("work.draft.min_prompts"); err != nil || ok {
		t.Fatalf("GetSetting after delete: ok %t, err %v", ok, err)
	}
}

// TestSettingsListOrdered: the listing is ordered by key, which the export and the HTTP
// listing rely on for a stable rendering.
func TestSettingsListOrdered(t *testing.T) {
	st := openSettingsTestStore(t)
	for _, key := range []string{"prompt.work.summarizer", "work.draft.min_prompts", "work.short_session.max_prompts"} {
		if _, err := st.PutSetting(key, "x", "alice"); err != nil {
			t.Fatalf("PutSetting(%s): %v", key, err)
		}
	}
	rows, err := st.ListSettings()
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	want := []string{"prompt.work.summarizer", "work.draft.min_prompts", "work.short_session.max_prompts"}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %d", rows, len(want))
	}
	for i, key := range want {
		if rows[i].Key != key {
			t.Errorf("row %d = %s, want %s", i, rows[i].Key, key)
		}
	}
}
