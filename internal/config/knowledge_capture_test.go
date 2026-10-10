package config

import (
	"strings"
	"testing"
)

func TestKnowledgeCaptureMode(t *testing.T) {
	if got := (ProjectConfig{}).EffectiveKnowledgeCapture(); got != ScopeDisciplineAuto {
		t.Fatalf("default = %q, want auto", got)
	}
	if got := (ProjectConfig{KnowledgeCapture: " OFF "}).EffectiveKnowledgeCapture(); got != ScopeDisciplineOff {
		t.Fatalf("normalised = %q, want off", got)
	}
	for mode, valid := range map[string]bool{"": true, "auto": true, "on": true, "off": true, "sometimes": false} {
		err := validate(&Config{Projects: map[string]ProjectConfig{"p": {HostPath: t.TempDir(), KnowledgeCapture: mode}}})
		if valid && err != nil && strings.Contains(err.Error(), "knowledge_capture") {
			t.Fatalf("%q: unexpected %v", mode, err)
		}
		if !valid && (err == nil || !strings.Contains(err.Error(), "knowledge_capture")) {
			t.Fatalf("%q: want a knowledge_capture error, got %v", mode, err)
		}
	}
}
