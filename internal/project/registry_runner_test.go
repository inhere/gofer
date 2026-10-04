package project

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestAllowsLocalRunnerDeclareWins(t *testing.T) {
	t.Parallel()
	plain := &config.Config{}
	declared := &config.Config{Runners: map[string]config.RunnerConfig{"server": {Type: "worker", WorkerID: "w1"}}}
	cases := []struct {
		cfg     *config.Config
		allowed []string
		want    bool
	}{
		{plain, nil, true},
		{plain, []string{"server"}, true},
		{plain, []string{"local"}, true},
		{plain, []string{"w1"}, false},
		{declared, []string{"server"}, false}, // the declared worker, not the built-in
		{declared, []string{"server", "local"}, true},
		{nil, []string{"server"}, true},
	}
	for i, tc := range cases {
		if got := AllowsLocalRunner(tc.cfg, tc.allowed); got != tc.want {
			t.Errorf("case %d: AllowsLocalRunner(%v) = %v, want %v", i, tc.allowed, got, tc.want)
		}
	}
}
