package client

import (
	"os"
	"testing"
)

// TestMain clears the session variables the agent running the tests exports (Claude Code,
// Codex, suga — suga puts SUGA_SESSION_ID into every command it starts) and the job id of a
// gofer job, so the runner's own session never becomes "the current session" of the code
// under test (G053: the test machine's environment is not the fixture).
func TestMain(m *testing.M) {
	for _, k := range AgentSessionEnvKeys {
		_ = os.Unsetenv(k)
	}
	_ = os.Unsetenv("GOFER_JOB_ID")
	os.Exit(m.Run())
}
