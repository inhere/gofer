package config

import (
	"strings"
	"testing"
)

func TestNormalizeRunnerName(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{"alias maps to canonical", "server", "local"},
		{"canonical stays", "local", "local"},
		{"spaces trimmed", " server ", "local"},
		{"case-insensitive", "Server", "local"},
		{"case-insensitive canonical", "LOCAL", "local"},
		{"empty stays empty", "", ""},
		{"worker id stays", "w1", "w1"},
	}
	for _, tc := range cases {
		if got := NormalizeRunnerName(tc.in); got != tc.want {
			t.Errorf("%s: NormalizeRunnerName(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	if !IsLocalRunnerName("server") || !IsLocalRunnerName("local") {
		t.Error("both spellings must be the local runner")
	}
	if IsLocalRunnerName("w1") || IsLocalRunnerName("") {
		t.Error("a worker id / empty label is not the local runner")
	}
}

// TestSameRunnerLabel pins the ONE "same runner" equivalence (plan R1): the two
// spellings of the built-in runner are one runner, every other label compares
// case-insensitively after trimming. A plain string comparison would miss the
// other spelling, which is stored exactly as the hook reported it (G043).
func TestSameRunnerLabel(t *testing.T) {
	t.Parallel()
	same := [][2]string{
		{"local", "local"},
		{"local", "server"},
		{"server", "local"},
		{" Server ", "LOCAL"},
		{"w-lab", " W-LAB "},
		{"", ""},
	}
	for _, c := range same {
		if !SameRunnerLabel(c[0], c[1]) {
			t.Errorf("SameRunnerLabel(%q, %q) = false, want true", c[0], c[1])
		}
	}
	different := [][2]string{
		{"local", "w-lab"},
		{"local", ""},
		{"", "w-lab"},
		{"w-lab", "w-lab-2"},
	}
	for _, c := range different {
		if SameRunnerLabel(c[0], c[1]) {
			t.Errorf("SameRunnerLabel(%q, %q) = true, want false", c[0], c[1])
		}
	}
}

// TestRunnerDisplayName pins the display spelling (G043): a person reads `server`
// for the built-in runner, every other runner keeps its own name. Display only —
// the wire, the DB and every comparison keep `local`.
func TestRunnerDisplayName(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"local", "server"},
		{"server", "server"},
		{" Server ", "server"},
		{"w-lab", "w-lab"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := RunnerDisplayName(tc.in); got != tc.want {
			t.Errorf("RunnerDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestReservedRunnerNames: server / local belong to the built-in runner. The only
// legal declaration is the built-in itself (type: local); anything else, and any
// worker id spelled that way, is refused with an explanation.
func TestReservedRunnerNames(t *testing.T) {
	t.Parallel()
	mk := func(runners map[string]RunnerConfig) *Config {
		c := &Config{Runners: runners}
		c.Projects = map[string]ProjectConfig{}
		return c
	}
	ok := []map[string]RunnerConfig{
		{"local": {Type: "local"}},
		{"server": {Type: "local"}},
		{"builder": {Type: "worker", WorkerID: "w1"}},
	}
	for i, r := range ok {
		if err := validate(mk(r)); err != nil {
			t.Errorf("ok case %d: %v", i, err)
		}
	}
	bad := []map[string]RunnerConfig{
		{"server": {Type: "worker", WorkerID: "w1"}},
		{"local": {Type: "peer-http", BaseURL: "http://x"}},
		{" Server ": {Type: "worker", WorkerID: "w1"}},
		{"server": {}},
		{"w1": {Type: "worker", WorkerID: "server"}},
	}
	for i, r := range bad {
		err := validate(mk(r))
		if err == nil {
			t.Errorf("bad case %d: want an error", i)
			continue
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("bad case %d: error %q should explain the reservation", i, err)
		}
	}
	c := mk(nil)
	c.Server.Workers = map[string]WorkerAuthConfig{"server": {Token: "t"}}
	if err := validate(c); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("server.workers key `server`: err = %v", err)
	}
	if err := CheckWorkerID("Local"); err == nil {
		t.Error("CheckWorkerID(Local) must fail")
	}
	if err := CheckWorkerID("w-1"); err != nil {
		t.Errorf("CheckWorkerID(w-1) = %v", err)
	}
}
