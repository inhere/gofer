package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func chainPtr(n int) *int { return &n }

// TestEffectiveContinueMaxChain: unset is the default 3, an explicit 0 stays 0
// (unlimited), explicit values are carried, an unknown project gets the default.
func TestEffectiveContinueMaxChain(t *testing.T) {
	cfg := &Config{Projects: map[string]ProjectConfig{
		"unset":   {},
		"zero":    {ContinueMaxChain: chainPtr(0)},
		"five":    {ContinueMaxChain: chainPtr(5)},
		"ceiling": {ContinueMaxChain: chainPtr(MaxContinueMaxChain)},
	}}
	for key, want := range map[string]int{"unset": 3, "zero": 0, "five": 5, "ceiling": 10, "unknown": 3} {
		if got := cfg.EffectiveContinueMaxChain(key); got != want {
			t.Errorf("%s: EffectiveContinueMaxChain = %d, want %d", key, got, want)
		}
	}
	if DefaultContinueMaxChain != 3 || MaxContinueMaxChain != 10 {
		t.Errorf("default/max = %d/%d, want 3/10", DefaultContinueMaxChain, MaxContinueMaxChain)
	}
}

// TestContinueMaxChainValidation: 0..10 load, outside it fails with a message naming the field.
func TestContinueMaxChainValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    *int
		err  bool
	}{
		{"unset", nil, false}, {"zero", chainPtr(0), false}, {"one", chainPtr(1), false},
		{"ten", chainPtr(10), false}, {"eleven", chainPtr(11), true}, {"negative", chainPtr(-1), true},
	} {
		err := validate(&Config{Projects: map[string]ProjectConfig{
			"p": {HostPath: "/tmp/p", ContinueMaxChain: tc.n},
		}})
		if !tc.err {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "continue_max_chain") {
			t.Errorf("%s: err = %v, want it to mention continue_max_chain", tc.name, err)
		}
	}
}

// TestContinueMaxChainYAML: the yaml spelling keeps unset and 0 apart.
func TestContinueMaxChainYAML(t *testing.T) {
	var cfg Config
	src := "projects:\n  a:\n    host_path: /x\n  b:\n    host_path: /y\n    continue_max_chain: 0\n  c:\n    host_path: /z\n    continue_max_chain: 7\n"
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for key, want := range map[string]int{"a": 3, "b": 0, "c": 7} {
		if got := cfg.EffectiveContinueMaxChain(key); got != want {
			t.Errorf("%s: got %d, want %d", key, got, want)
		}
	}
	if _, ok := FieldPolicyFor("projects.*.continue_max_chain"); !ok {
		t.Error("no field policy for projects.*.continue_max_chain")
	}
}
