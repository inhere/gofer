package serve

import (
	"os"
	"testing"

	"github.com/inhere/gofer/internal/servicemgr"
)

// The managed spec path is consumed once and must not leak to the jobs the server runs.
func TestTakeManagedSpecEnvScrubsInheritance(t *testing.T) {
	t.Setenv(servicemgr.EnvManagedSpec, "/some/spec.json")
	if got := takeManagedSpecEnv(); got != "/some/spec.json" {
		t.Fatalf("takeManagedSpecEnv() = %q", got)
	}
	if _, set := os.LookupEnv(servicemgr.EnvManagedSpec); set {
		t.Fatal("the variable must be removed so child processes do not inherit it")
	}
	if got := takeManagedSpecEnv(); got != "" {
		t.Fatalf("second take = %q, want empty", got)
	}
}
