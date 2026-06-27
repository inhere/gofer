package config

import "testing"

// TestRunMode proves GOFER_RUN_MODE resolves to worker only for "worker"
// (case-insensitive) and defaults to server for empty/unknown values (E38②).
func TestRunMode(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"", RunModeServer},
		{"server", RunModeServer},
		{"worker", RunModeWorker},
		{"Worker", RunModeWorker},
		{"  WORKER ", RunModeWorker},
		{"mcp", RunModeServer},     // not a run-mode value → default server
		{"garbage", RunModeServer}, // unknown → default server
	}
	for _, tc := range cases {
		t.Setenv(EnvRunMode, tc.env)
		if got := RunMode(); got != tc.want {
			t.Fatalf("RunMode() with %q = %q, want %q", tc.env, got, tc.want)
		}
	}
}
