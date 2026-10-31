package serve

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// The prune loop sweeps expired image attachments (rows and files) even when job retention,
// cast and mail retention are all off, and buildAttachments roots the store at
// <config-dir>/attachments with the live config limits.
func TestPruneLoopSweepsExpiredAttachments(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	cr, _ := buildGateCore(t)

	svc := buildAttachments(cr)
	if svc == nil {
		t.Fatal("buildAttachments returned nil")
	}
	if want := filepath.Join(cfgDir, "attachments"); svc.Root() != want {
		t.Fatalf("root = %s, want %s", svc.Root(), want)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	// Uploaded two weeks ago under the default 14-day retention: expired by now.
	svc.SetClock(func() time.Time { return time.Now().Add(-15 * 24 * time.Hour) })
	rec, err := svc.Save("alice", &buf)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetClock(time.Now)
	file := filepath.Join(svc.Root(), filepath.FromSlash(rec.Path))
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	defer close(stop)
	startPruneLoop(gcli.NewCommand("t", ""), cr.Jobs, config.RetentionConfig{}, false, 0, 0, svc.Prune, nil, stop)

	wait.Until(t, 10*time.Second, "expired attachment file removed", func() bool {
		_, err := os.Stat(file)
		return os.IsNotExist(err)
	})
	if _, ok, err := cr.Store.GetAttachment(rec.ID); err != nil || ok {
		t.Fatalf("expired attachment row still there: ok=%v err=%v", ok, err)
	}
}
