package hookrelay

import (
	"testing"
)

// TestSessionRelayInstalled: the PreToolUse entry `repo init` installs for
// command-time memories is not the relay; a full relay install counts at the
// project or the user level.
func TestSessionRelayInstalled(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	if SessionRelayInstalled(root, home) {
		t.Fatal("nothing installed yet")
	}
	for _, agent := range []string{AgentClaude, AgentCodex} {
		if _, err := InstallCommandMemory(agent, root); err != nil {
			t.Fatal(err)
		}
	}
	if SessionRelayInstalled(root, home) {
		t.Fatal("the command-memory PreToolUse entry alone must not count as the relay")
	}
	path, err := ConfigFileFor(AgentClaude, home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(AgentClaude, path, false, false); err != nil {
		t.Fatal(err)
	}
	if !SessionRelayInstalled(root, home) || !SessionRelayInstalled("", home) {
		t.Fatal("user-level relay hooks not detected")
	}
	if SessionRelayInstalled(root, "") {
		t.Fatal("project root still has only the command-memory entry")
	}
	path, _ = ConfigFileFor(AgentCodex, root)
	if _, err := Install(AgentCodex, path, false, false); err != nil {
		t.Fatal(err)
	}
	if !SessionRelayInstalled(root, "") {
		t.Fatal("project-level relay hooks not detected")
	}
}
