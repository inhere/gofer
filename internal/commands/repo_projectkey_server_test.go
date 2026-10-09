package commands

import (
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

type fakeProjectLister struct {
	projects []client.ProjectMeta
	err      error
}

func (f fakeProjectLister) ListProjects() ([]client.ProjectMeta, error) { return f.projects, f.err }

// TestServerProjectKeyFallsBackToPath: a repository label the server does not know
// (gofer's own project_key: gofer) resolves to the project whose path contains it.
func TestServerProjectKeyFallsBackToPath(t *testing.T) {
	ws := fakeProjectLister{projects: []client.ProjectMeta{{Key: "my-tools-dev", ContainerPath: "/ws/my-tools-dev"}}}
	if got := serverProjectKey(ws, "gofer", "/ws/my-tools-dev/inhere-tools/gofer"); got != "my-tools-dev" {
		t.Fatalf("unknown key: got %q, want my-tools-dev", got)
	}
	if got := serverProjectKey(ws, "my-tools-dev", "/elsewhere"); got != "my-tools-dev" {
		t.Fatalf("known key must stay: got %q", got)
	}
	if got := serverProjectKey(ws, "gofer", "/elsewhere"); got != "gofer" {
		t.Fatalf("no path match keeps the key: got %q", got)
	}
	if got := serverProjectKey(fakeProjectLister{err: errors.New("down")}, "gofer", "/ws/my-tools-dev/x"); got != "gofer" {
		t.Fatalf("server error keeps the key: got %q", got)
	}
}
