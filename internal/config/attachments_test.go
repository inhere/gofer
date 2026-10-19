package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// server.attachments and storage.attachment_retention_days: strict defaults, overrides, and
// every invalid number is a load error naming the key.
func TestAttachmentsConfig(t *testing.T) {
	load := func(body string) (*Config, error) {
		p := filepath.Join(t.TempDir(), "cfg.yaml")
		write(t, p, body)
		cfg, _, err := Load(p)
		return cfg, err
	}
	base := "projects:\n  demo:\n    host_path: /tmp/demo\n"

	cfg, err := load(base)
	if err != nil {
		t.Fatal(err)
	}
	want := AttachmentSettings{MaxBytes: 10 << 20, MaxPerMessage: 6, HourlyPerCaller: 200}
	if got := cfg.EffectiveAttachments(); got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	if got := (*Config)(nil).EffectiveAttachments(); got != want {
		t.Fatalf("nil config = %+v, want the defaults", got)
	}
	if got := cfg.Storage.AttachmentRetention(); got != 14*24*time.Hour {
		t.Fatalf("default retention = %s, want 14 days", got)
	}

	cfg, err = load(base + "server:\n  attachments:\n    max_bytes: 1048576\n    max_per_message: 3\n    hourly_per_caller: 10\nstorage:\n  attachment_retention_days: 3\n")
	if err != nil {
		t.Fatal(err)
	}
	want = AttachmentSettings{MaxBytes: 1 << 20, MaxPerMessage: 3, HourlyPerCaller: 10}
	if got := cfg.EffectiveAttachments(); got != want {
		t.Fatalf("overrides = %+v, want %+v", got, want)
	}
	if got := cfg.Storage.AttachmentRetention(); got != 72*time.Hour {
		t.Fatalf("retention = %s, want 72h", got)
	}
	if got := cfg.Clone().EffectiveAttachments(); got != want {
		t.Fatalf("a clone lost the attachments block: %+v", got)
	}

	// A negative retention means keep forever (0 duration).
	cfg, err = load(base + "storage:\n  attachment_retention_days: -1\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Storage.AttachmentRetention(); got != 0 {
		t.Fatalf("negative retention = %s, want 0 (keep forever)", got)
	}

	for _, key := range []string{"max_bytes", "max_per_message", "hourly_per_caller"} {
		_, err := load(base + "server:\n  attachments:\n    " + key + ": -1\n")
		if err == nil || !strings.Contains(err.Error(), "server.attachments."+key) {
			t.Fatalf("server.attachments.%s: -1 must be rejected, err=%v", key, err)
		}
	}
	if _, err := load(base + "server:\n  attachments:\n    max_bytes: 1073741824\n"); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("an upload ceiling beyond the memory cap must be rejected, err=%v", err)
	}
	if fp, ok := FieldPolicyFor("server.attachments"); !ok || (!fp.Editable && !fp.HotReload) {
		t.Fatalf("server.attachments must be classified: %+v ok=%v", fp, ok)
	}
}
