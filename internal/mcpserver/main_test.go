package mcpserver

import (
	"github.com/inhere/gofer/internal/testutil/machinelang"
	"os"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

// TestMain clears the session variables the agent running the tests exports (Claude Code,
// Codex, suga — suga puts SUGA_SESSION_ID into every command it starts) and the job id of a
// gofer job, so the runner's own session never becomes "the current session" of a tool under
// test (G053: the test machine's environment is not the fixture).
func TestMain(m *testing.M) {
	for _, k := range client.AgentSessionEnvKeys {
		_ = os.Unsetenv(k)
	}
	_ = os.Unsetenv("GOFER_JOB_ID")
	machinelang.PinZh()
	os.Exit(m.Run())
}
