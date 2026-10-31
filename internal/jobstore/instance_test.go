package jobstore

import (
	"path/filepath"
	"testing"
)

// TestInstanceID: the id is created once, survives a reopen, differs between databases,
// and does not count as a settings write.
func TestInstanceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	a, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id, err := a.InstanceID()
	if err != nil || id == "" {
		t.Fatalf("InstanceID = %q, %v", id, err)
	}
	if again, _ := a.InstanceID(); again != id {
		t.Fatalf("second call = %q, want %q", again, id)
	}
	if v, err := a.SettingsVersion(); err != nil || v != 0 {
		t.Fatalf("SettingsVersion = %d, %v; the instance id must not bump it", v, err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got, _ := reopened.InstanceID(); got != id {
		t.Fatalf("after reopen = %q, want %q", got, id)
	}
	other := openSettingsTestStore(t)
	if oid, _ := other.InstanceID(); oid == id {
		t.Fatalf("two databases share instance id %q", id)
	}
}
