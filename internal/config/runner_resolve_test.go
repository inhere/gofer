package config

import "testing"

func TestResolveRunnerName(t *testing.T) {
	t.Parallel()
	plain := &Config{}
	declared := &Config{Runners: map[string]RunnerConfig{"server": {Type: "worker", WorkerID: "w1"}}}
	cases := []struct {
		name string
		cfg  *Config
		in   string
		want string
	}{
		{"alias maps to canonical", plain, "server", "local"},
		{"canonical stays", plain, "local", "local"},
		{"spaces trimmed", plain, " server ", "local"},
		{"empty stays empty", plain, "", ""},
		{"worker id stays", plain, "w1", "w1"},
		{"nil config is spelling-only", nil, "server", "local"},
		{"declare-wins keeps server", declared, "server", "server"},
		{"declared config still maps nothing else", declared, "local", "local"},
	}
	for _, tc := range cases {
		if got := ResolveRunnerName(tc.cfg, tc.in); got != tc.want {
			t.Errorf("%s: ResolveRunnerName(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	if !IsLocalRunnerName(plain, "server") || !IsLocalRunnerName(plain, "local") {
		t.Error("both spellings must be the local runner on a plain config")
	}
	if IsLocalRunnerName(declared, "server") || !IsLocalRunnerName(declared, "local") {
		t.Error("a declared `server` runner is not the local runner; `local` still is")
	}
	if IsLocalRunnerName(plain, "w1") || IsLocalRunnerName(plain, "") {
		t.Error("a worker id / empty label is not the local runner")
	}
}
