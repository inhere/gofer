package commands

import "testing"

// TestNewApp verifies the app assembles with all top-level commands registered
// and does not panic.
func TestNewApp(t *testing.T) {
	app := NewApp("test")
	if app == nil {
		t.Fatal("NewApp returned nil")
	}
	if app.Name != "agent-bridge" {
		t.Fatalf("unexpected app name: %q", app.Name)
	}

	for _, name := range []string{"serve", "project", "agent", "job", "mcp"} {
		if !app.HasCommand(name) {
			t.Errorf("missing top-level command: %s", name)
		}
	}
}
