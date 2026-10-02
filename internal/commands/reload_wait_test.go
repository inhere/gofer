package commands

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

func TestWaitForReloadResult(t *testing.T) {
	path := t.TempDir() + "/run/serve.reload.json"
	before := reloadReceiptState{}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = config.WriteReloadResult(path, config.ReloadResult{Rev: 2, Path: "config.yaml"})
	}()
	got, err := waitForReloadResult(path, before, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rev != 2 {
		t.Fatalf("result=%+v, want rev 2", got)
	}
}
