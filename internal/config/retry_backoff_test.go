package config

import (
	"encoding/json"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// TestBackoffSecAcceptsScalarAndList pins gofer-pn0x: `backoff_sec` reads both spellings
// a person writes — the list and a single number (the same wait before every retry) — in
// YAML and in JSON, and a fractional or non-numeric value is refused with a message that
// says what is accepted.
func TestBackoffSecAcceptsScalarAndList(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []int
	}{
		{"yaml list", "retry:\n  max_attempts: 3\n  backoff_sec: [300, 600]\n", []int{300, 600}},
		{"yaml single number", "retry:\n  max_attempts: 3\n  backoff_sec: 300\n", []int{300}},
		{"yaml single-item list", "retry:\n  max_attempts: 3\n  backoff_sec: [300]\n", []int{300}},
		{"yaml zero is legal", "retry:\n  max_attempts: 2\n  backoff_sec: 0\n", []int{0}},
		{"yaml absent", "retry:\n  max_attempts: 3\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc struct {
				Retry *RetryPolicy `yaml:"retry"`
			}
			if err := yaml.Unmarshal([]byte(tc.src), &doc); err != nil {
				t.Fatalf("yaml.Unmarshal(%q): %v", tc.src, err)
			}
			if doc.Retry == nil {
				t.Fatalf("retry = nil for %q", tc.src)
			}
			assertBackoff(t, doc.Retry.BackoffSec, tc.want)
		})
	}

	jsonCases := []struct {
		name string
		src  string
		want []int
	}{
		{"json list", `{"max_attempts":3,"backoff_sec":[300,600]}`, []int{300, 600}},
		{"json single number", `{"max_attempts":3,"backoff_sec":300}`, []int{300}},
		{"json absent", `{"max_attempts":3}`, nil},
	}
	for _, tc := range jsonCases {
		t.Run(tc.name, func(t *testing.T) {
			var p RetryPolicy
			if err := json.Unmarshal([]byte(tc.src), &p); err != nil {
				t.Fatalf("json.Unmarshal(%q): %v", tc.src, err)
			}
			assertBackoff(t, p.BackoffSec, tc.want)
		})
	}
}

// TestBackoffSecRejectsWhatItCannotRead: the two spellings are the whole contract, so a
// string or a fraction is an error naming the field rather than a silently ignored value.
func TestBackoffSecRejectsWhatItCannotRead(t *testing.T) {
	var doc struct {
		Retry *RetryPolicy `yaml:"retry"`
	}
	if err := yaml.Unmarshal([]byte("retry:\n  backoff_sec: soon\n"), &doc); err == nil {
		t.Fatal("yaml backoff_sec: soon decoded, want an error")
	} else if !strings.Contains(err.Error(), "backoff_sec") {
		t.Fatalf("error = %v, want it to name backoff_sec", err)
	}

	var p RetryPolicy
	if err := json.Unmarshal([]byte(`{"backoff_sec":1.5}`), &p); err == nil {
		t.Fatal("json backoff_sec 1.5 decoded, want an error")
	} else if !strings.Contains(err.Error(), "whole number") {
		t.Fatalf("error = %v, want it to say a whole number is required", err)
	}
}

// TestBackoffSecMarshalsAsList: writing a config back (workflow export, template edit)
// always emits the list form, so the file never depends on which spelling a reader
// understands.
func TestBackoffSecMarshalsAsList(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 2, BackoffSec: BackoffSec{300}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"backoff_sec":[300]`) {
		t.Fatalf("json = %s, want the list form", b)
	}
	y, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if !strings.Contains(string(y), "- 300") {
		t.Fatalf("yaml = %s, want the list form", y)
	}
}

func assertBackoff(t *testing.T, got []int, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("backoff_sec = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff_sec = %v, want %v", got, want)
		}
	}
}
