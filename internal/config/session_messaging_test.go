package config

import (
	"testing"
	"time"
)

// TestSessionMessagingRotationLimits pins the gofer-li3t knobs: an unset key keeps
// the shipped default, and an explicit 0 means "no limit" (unset≠zero).
func TestSessionMessagingRotationLimits(t *testing.T) {
	var unset SessionMessagingConfig
	if got := unset.MaxMessages(); got != DefaultMessengerMaxMessages {
		t.Fatalf("unset max messages = %d, want the default %d", got, DefaultMessengerMaxMessages)
	}
	if got := unset.MaxAge(); got != time.Duration(DefaultMessengerMaxAgeSec)*time.Second {
		t.Fatalf("unset max age = %v, want the default", got)
	}

	zero, negative := 0, -5
	off := SessionMessagingConfig{MessengerMaxMessages: &zero, MessengerMaxAgeSec: &zero}
	if off.MaxMessages() != 0 || off.MaxAge() != 0 {
		t.Fatalf("explicit 0 = %d/%v, want both unlimited", off.MaxMessages(), off.MaxAge())
	}
	neg := SessionMessagingConfig{MessengerMaxMessages: &negative, MessengerMaxAgeSec: &negative}
	if neg.MaxMessages() != DefaultMessengerMaxMessages || neg.MaxAge() != time.Duration(DefaultMessengerMaxAgeSec)*time.Second {
		t.Fatalf("negative = %d/%v, want the defaults", neg.MaxMessages(), neg.MaxAge())
	}

	if got := (ServerConfig{}).EffectiveSessionMessaging(); got.MaxMessages() != DefaultMessengerMaxMessages {
		t.Fatalf("effective (no block) max messages = %d", got.MaxMessages())
	}
	msgs, age := 7, 60
	cfg := ServerConfig{SessionMessaging: SessionMessagingConfig{MessengerMaxMessages: &msgs, MessengerMaxAgeSec: &age}}
	eff := cfg.EffectiveSessionMessaging()
	if eff.MaxMessages() != 7 || eff.MaxAge() != time.Minute {
		t.Fatalf("effective = %d/%v, want 7/1m", eff.MaxMessages(), eff.MaxAge())
	}
}
