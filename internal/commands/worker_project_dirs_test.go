package commands

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestWorkerProjectDirsSkipsProjectsForOtherRunners(t *testing.T) {
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"mine":   {HostPath: "/a", AllowedRunners: []string{"local"}},
		"open":   {HostPath: "/b"},
		"theirs": {HostPath: "/c", AllowedRunners: []string{"other-worker"}},
	}}
	got := workerProjectDirs(cfg)
	if len(got) != 2 || got["mine"] == "" || got["open"] == "" {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["theirs"]; ok {
		t.Fatalf("project for another runner must not be reported: %v", got)
	}
}
