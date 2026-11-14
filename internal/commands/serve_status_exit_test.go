package commands

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/errorx"
	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/config"
)

func healthyServeStatus() serveStatusOutput {
	return serveStatusOutput{
		Name: "gofer", Registered: true, Active: true, ServerPID: 42, ServerVerified: true,
		PortOwned: true, Health: "healthy", ConfigFile: "/cfg.yaml",
		RegisteredVersion: "v1.0.0", RunningVersion: "v1.0.0",
	}
}

// TestClassifyServeStatusExitCodes builds one status per exit code from the fields
// (design: 0 running, 3 stopped, 4 degraded, 5 unconfigured).
func TestClassifyServeStatusExitCodes(t *testing.T) {
	mut := func(f func(*serveStatusOutput)) serveStatusOutput {
		s := healthyServeStatus()
		f(&s)
		return s
	}
	cases := []struct {
		name  string
		out   serveStatusOutput
		state string
		code  int
	}{
		{"healthy", healthyServeStatus(), "running", 0},
		{"no spec", serveStatusOutput{Name: "gofer", Health: "unverified"}, "unconfigured", 5},
		{"spec but native registration missing", serveStatusOutput{Name: "gofer", ConfigFile: "/cfg.yaml", Health: "unverified"}, "stopped", 3},
		{"registered not active", mut(func(s *serveStatusOutput) { s.Active = false; s.ServerPID = 0; s.ServerVerified = false }), "stopped", 3},
		{"active but no server pid", mut(func(s *serveStatusOutput) { s.ServerPID = 0; s.ServerVerified = false }), "stopped", 3},
		{"port nobody listens", mut(func(s *serveStatusOutput) { s.PortOwned = false; s.Health = "unverified" }), "stopped", 3},
		{"port held by another process", mut(func(s *serveStatusOutput) { s.PortOwned = false; s.portListening = true; s.Health = "unverified" }), "degraded", 4},
		{"identity not verified", mut(func(s *serveStatusOutput) { s.ServerVerified = false }), "degraded", 4},
		{"health unreachable", mut(func(s *serveStatusOutput) { s.Health = "unreachable" }), "degraded", 4},
		{"health http 503", mut(func(s *serveStatusOutput) { s.Health = "http_503" }), "degraded", 4},
		{"health unverified", mut(func(s *serveStatusOutput) { s.Health = "unverified" }), "degraded", 4},
		{"stats auth mismatch", mut(func(s *serveStatusOutput) { s.VersionAuthMismatch = &versionAuthMismatch{} }), "degraded", 4},
		{"version drift", mut(func(s *serveStatusOutput) { s.RunningVersion = "v0.9.0" }), "degraded", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, code := classifyServeStatus(tc.out)
			assert.Eq(t, tc.state, state)
			assert.Eq(t, tc.code, code)
			err := serveStatusExitError(state, code)
			if tc.code == 0 {
				assert.NoErr(t, err)
				return
			}
			var ec errorx.ErrorCoder
			if !errors.As(err, &ec) {
				t.Fatalf("err %v is not a coded error", err)
			}
			assert.Eq(t, tc.code, ec.Code())
		})
	}
}

// TestServeStatusJSONCarriesStateAndExitCode pins the --json additions.
func TestServeStatusJSONCarriesStateAndExitCode(t *testing.T) {
	out := healthyServeStatus()
	out.Health = "unreachable"
	state, code := classifyServeStatus(out)
	raw, err := json.Marshal(serveStatusJSON{serveStatusOutput: out, State: state, ExitCode: code})
	assert.NoErr(t, err)
	var m map[string]any
	assert.NoErr(t, json.Unmarshal(raw, &m))
	assert.Eq(t, "degraded", m["state"])
	assert.Eq(t, float64(4), m["exit_code"])
	assert.Eq(t, "unreachable", m["health"]) // inspected fields stay flat
	assert.False(t, strings.Contains(string(raw), "portListening"))
}

// TestServeStatusCommandUnconfiguredExitsFive runs the real command with no
// registered server: state line printed, exit code 5.
func TestServeStatusCommandUnconfiguredExitsFive(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	prev := manageOpts
	t.Cleanup(func() { manageOpts = prev })
	var code int
	out := captureOutput(t, func() { code = NewApp("test").Run([]string{"serve", "status", "--name", "no-such-instance"}) })
	assert.Eq(t, 5, code)
	assert.StrContains(t, out, "state=unconfigured")
}

// A check that cannot even run (here: an invalid instance name) exits 1, the code the
// docs give for "the check itself failed", not gcli's generic error code.
func TestServeStatusCommandCheckFailureExitsOne(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	prev := manageOpts
	t.Cleanup(func() { manageOpts = prev })
	var code int
	out := captureOutput(t, func() { code = NewApp("test").Run([]string{"serve", "status", "--name", "bad/name"}) })
	assert.Eq(t, 1, code)
	assert.StrContains(t, out, "invalid service name")
}
