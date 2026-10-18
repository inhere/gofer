package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The `mail:` block (plan M5): strict defaults that match the design, overrides, and a
// negative number is a load error.
func TestMailConfig(t *testing.T) {
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
	want := MailSettings{
		Tick: 30 * time.Second, WakeIdle: 5 * time.Minute, WakeInterval: 10 * time.Minute,
		DeliverBy: 24 * time.Hour, Lease: time.Minute,
	}
	if got := cfg.EffectiveMail(); got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	if got := (*Config)(nil).EffectiveMail(); got != want {
		t.Fatalf("nil config = %+v, want the defaults", got)
	}

	cfg, err = load(base + "mail:\n  tick_sec: 5\n  wake_idle_sec: 60\n  wake_interval_sec: 120\n  deliver_by_sec: 3600\n  claim_lease_sec: 20\n")
	if err != nil {
		t.Fatal(err)
	}
	want = MailSettings{Tick: 5 * time.Second, WakeIdle: time.Minute, WakeInterval: 2 * time.Minute, DeliverBy: time.Hour, Lease: 20 * time.Second}
	if got := cfg.EffectiveMail(); got != want {
		t.Fatalf("overrides = %+v, want %+v", got, want)
	}
	if got := cfg.Clone().EffectiveMail(); got != want {
		t.Fatalf("a clone lost the mail block: %+v", got)
	}

	for _, key := range []string{"tick_sec", "wake_idle_sec", "wake_interval_sec", "deliver_by_sec", "claim_lease_sec"} {
		_, err := load(base + "mail:\n  " + key + ": -1\n")
		if err == nil || !strings.Contains(err.Error(), "mail."+key) {
			t.Fatalf("mail.%s: -1 must be rejected, err=%v", key, err)
		}
	}
	if fp, ok := FieldPolicyFor("mail"); !ok || !fp.Editable {
		t.Fatalf("mail must be a classified, editable block: %+v ok=%v", fp, ok)
	}
}
