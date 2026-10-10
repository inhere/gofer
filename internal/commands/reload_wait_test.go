package commands

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/wait"
)

func TestWaitForReloadResult(t *testing.T) {
	path := t.TempDir() + "/run/serve.reload.json"
	since := time.Now()
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = config.WriteReloadResult(path, config.ReloadResult{Rev: 2, Path: "config.yaml"})
	}()
	got, err := waitForReloadResult(path, since, wait.Timeout(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Rev != 2 || got.ReloadedAt.IsZero() {
		t.Fatalf("result=%+v, want rev 2 with reloaded_at", got)
	}
}

// A restarted server restarts Rev from its initial value; the fresh receipt must
// still be accepted even though its Rev is lower than the previous one.
func TestWaitForReloadResultAcceptsLowerRevAfterRestart(t *testing.T) {
	path := t.TempDir() + "/run/serve.reload.json"
	if err := config.WriteReloadResult(path, config.ReloadResult{Rev: 9}); err != nil {
		t.Fatal(err)
	}
	since := time.Now()
	time.Sleep(5 * time.Millisecond)
	if err := config.WriteReloadResult(path, config.ReloadResult{Rev: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := waitForReloadResult(path, since, wait.Timeout(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Rev != 1 {
		t.Fatalf("result=%+v, want rev 1", got)
	}
}

func TestWaitForReloadResultIgnoresStaleReceipt(t *testing.T) {
	path := t.TempDir() + "/run/serve.reload.json"
	if err := config.WriteReloadResult(path, config.ReloadResult{Rev: 3, ReloadedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := waitForReloadResult(path, time.Now(), 250*time.Millisecond); err == nil {
		t.Fatal("stale receipt accepted")
	}
}

func TestWaitForReloadResultReportsErrorReceipt(t *testing.T) {
	path := t.TempDir() + "/run/serve.reload.json"
	since := time.Now()
	_ = config.WriteReloadResult(path, config.ReloadResult{Rev: 1, Error: "bad yaml"})
	got, err := waitForReloadResult(path, since, 250*time.Millisecond)
	if err != nil || got.Error != "bad yaml" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
