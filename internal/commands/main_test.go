package commands

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/testutil/gitenv"
)

// isolateAgentSessionEnv clears the agent-session variables the terminal running the tests
// may export (client.AgentSessionEnvKeys — Claude Code and Codex both do), so the runner's own
// session cannot leak into the command under test: a test that asserts "no session header"
// would fail on a machine inside an agent, and one that expects exactly one candidate would
// see two and attach nothing (G053: the test machine's environment is not the fixture).
func isolateAgentSessionEnv(t *testing.T) {
	t.Helper()
	for _, k := range client.AgentSessionEnvKeys {
		t.Setenv(k, "")
	}
}

// TestMain isolates the package from process-level state a test could otherwise
// inherit from the developer's shell or from the order tests run in:
//   - the server time zone is never fetched over the network (without this a test
//     that formats a timestamp before any other test cached it hits GET /v1/stats);
//   - GOFER_*_TOKEN variables of the host are dropped;
//   - the pristine flag option structs are snapshotted for resetFlagGlobals.
func TestMain(m *testing.M) {
	serverTZFetch = func() (int, bool) { return 0, false }
	// The developer's own user-level relay hooks must not change prime output.
	primeUserHome = func() (string, error) { return "", errors.New("no user home in tests") }
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GOFER_") && strings.HasSuffix(name, "_TOKEN") {
			_ = os.Unsetenv(name)
		}
	}
	// The agent running the tests (Claude Code, Codex, suga — suga exports SUGA_SESSION_ID
	// into every command it starts, gofer jobs carry GOFER_JOB_ID) must not become "the
	// current session" of the commands under test (G053).
	for _, k := range client.AgentSessionEnvKeys {
		_ = os.Unsetenv(k)
	}
	_ = os.Unsetenv("GOFER_JOB_ID")
	// The developer's config directory and server stay out of the tests: commands
	// that open <config-dir>/tracker (the global memory copy) or sync it would
	// otherwise touch the real ones. A test that needs either sets its own.
	cfgDir, err := os.MkdirTemp("", "gofer-commands-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("GOFER_CONFIG_DIR", cfgDir)
	_ = os.Unsetenv("GOFER_SERVER_ADDR")
	// The tests commit in TempDir repositories; git's detached auto maintenance
	// must not outlive them (see gitenv).
	gitenv.DisableAutoMaintenance()
	snapshotFlagGlobals()
	code := m.Run()
	_ = os.RemoveAll(cfgDir)
	os.Exit(code)
}
