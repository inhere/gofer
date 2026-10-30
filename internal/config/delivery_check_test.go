package config

import "testing"

func TestDeliveryCheckDefaultsAndValidation(t *testing.T) {
	var d DeliveryCheckConfig
	if d.EffectiveTimeoutSec() != DefaultDeliveryCheckTimeoutSec || d.EffectiveMaxAutoResumes() != DefaultDeliveryCheckMaxAutoResumes || d.EffectiveInputMaxBytes() != DefaultDeliveryCheckInputMaxBytes {
		t.Fatalf("defaults = %+v", d)
	}
	if err := (DeliveryCheckConfig{Agent: " claude"}).validate(); err == nil { t.Fatal("accepted surrounding whitespace") }
	if err := (BackgroundAgentSessionConfig{MaxContextRatio: 1.1}).validate(); err == nil { t.Fatal("accepted invalid ratio") }
}
