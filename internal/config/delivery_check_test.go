package config

import "testing"

func TestDeliveryCheckDefaultsAndValidation(t *testing.T) {
	var d DeliveryCheckConfig
	if d.Enabled || d.EffectiveTimeoutSec() != DefaultDeliveryCheckTimeoutSec || d.EffectiveMaxAutoResumes() != DefaultDeliveryCheckMaxAutoResumes || d.EffectiveInputMaxBytes() != DefaultDeliveryCheckInputMaxBytes {
		t.Fatalf("defaults = %+v", d)
	}
	if !d.EffectiveIncludeAcceptance() {
		t.Fatal("include_acceptance must default to true")
	}
	off := false
	if (DeliveryCheckConfig{IncludeAcceptance: &off}).EffectiveIncludeAcceptance() {
		t.Fatal("include_acceptance=false ignored")
	}
	zero := 0
	if (DeliveryCheckConfig{MaxAutoResumes: &zero}).EffectiveMaxAutoResumes() != 0 {
		t.Fatal("max_auto_resumes=0 must stay 0")
	}
	neg := -1
	for name, bad := range map[string]DeliveryCheckConfig{
		"agent whitespace": {Agent: " claude"},
		"model whitespace": {Model: "m "},
		"negative resumes": {MaxAutoResumes: &neg},
		"negative timeout": {TimeoutSec: -1},
	} {
		if err := bad.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBackgroundAgentSessionDefaultsAndValidation(t *testing.T) {
	var s BackgroundAgentSessionConfig
	if s.EffectiveMaxContextRatio() != 0.7 || s.EffectiveMaxRuns() != 20 || s.MaxAge != "" {
		t.Fatalf("defaults = %+v", s)
	}
	if err := s.validate(); err != nil {
		t.Fatalf("zero value rejected: %v", err)
	}
	if err := (BackgroundAgentSessionConfig{MaxAge: "24h"}).validate(); err != nil {
		t.Fatalf("24h rejected: %v", err)
	}
	for name, bad := range map[string]BackgroundAgentSessionConfig{
		"ratio high":     {MaxContextRatio: 1.1},
		"ratio negative": {MaxContextRatio: -0.1},
		"runs negative":  {MaxRuns: -1},
		"age junk":       {MaxAge: "soon"},
		"age negative":   {MaxAge: "-1h"},
	} {
		if err := bad.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
