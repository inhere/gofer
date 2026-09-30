package job

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStoreScanFallback(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)
	writeSession := func(name, dir, id string, mtime time.Time) {
		t.Helper()
		path := filepath.Join(root, name+"_"+id+".jsonl")
		body := fmt.Sprintf("{\"type\":\"session\",\"id\":%q,\"cwd\":%q}\n", id, dir)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	writeSession("old", cwd, "old-session-id", start.Add(-time.Second))
	writeSession("wrong-cwd", other, "other-session-id", start.Add(20*time.Second))
	writeSession("right", cwd, "right-session-id", start.Add(10*time.Second))
	got := scanSessionStore(filepath.Join(root, "*.jsonl"), `([a-z-]+session-id)`, cwd, start)
	if got != "right-session-id" {
		t.Fatalf("scanSessionStore = %q, want right-session-id", got)
	}
}
