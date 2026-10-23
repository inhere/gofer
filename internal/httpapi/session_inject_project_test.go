package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// gofer-vin1: a session whose cwd lies outside every configured project (so it has no
// project key) used to make every delivery fail — `deliver_failed: invalid request:
// invalid project key ""` — because the relay's internal job carried the empty key to
// job admission. The delivery now runs in the execution machine's default workspace,
// which is the one place that exists on every machine and belongs to nobody's project.

// newNoProjectServer is a server whose config carries only the built-in default
// workspace project (a self project is added by the shared fixture, so the test also
// pins that the delivery does NOT land there).
func newNoProjectServer(t *testing.T) (*Server, string) {
	t.Helper()
	ws := t.TempDir()
	t.Setenv("GOFER_WORKSPACE", ws) // the machine's default workspace (config.WorkspaceDir)
	s := newTestServerCfgWith(t, config.ServerConfig{}, func(cfg *config.Config) {
		config.InjectDefaultProject(cfg)
	})
	if _, ok := s.projects.Config().Projects[config.DefaultProjectKey]; !ok {
		t.Fatal("the fixture has no built-in default project")
	}
	return s, ws
}

// TestDeliveryWithoutAProjectRunsInTheMachineWorkspace: path A/C's internal job lands in
// the default project, in that project's root, and really runs there.
func TestDeliveryWithoutAProjectRunsInTheMachineWorkspace(t *testing.T) {
	s, ws := newNoProjectServer(t)
	bin := testcmd.Path(t)
	probe := filepath.Join(ws, "delivered.txt")

	res, err := s.msgr.InjectSession(context.Background(), sessionrelay.InjectRequest{
		ProjectKey: "", Runner: "local", Cwd: ".", Title: "relay deliver → sid-noproj",
		Cmd: []string{bin, "write-files", probe, "ran"}, TimeoutSec: 30, UpdateExempt: true,
	})
	if err != nil {
		t.Fatalf("InjectSession(no project): %v (this is the bug: it used to be invalid project key \"\")", err)
	}
	if res.Status != "" || res.ExitCode != 0 {
		t.Fatalf("delivery result = %+v, want a job that ran to completion", res)
	}
	got, ok := s.jobs.Get(res.JobID)
	if !ok {
		t.Fatalf("job %s not found", res.JobID)
	}
	if got.ProjectKey != config.DefaultProjectKey {
		t.Fatalf("job project = %q, want %q", got.ProjectKey, config.DefaultProjectKey)
	}
	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("the delivery did not run in the default workspace: %v", err)
	}
	if selfProbe, err := os.Stat(filepath.Join(s.projects.Config().Projects["self"].HostPath, "delivered.txt")); err == nil {
		t.Fatalf("the delivery ran in another project's tree: %s", selfProbe.Name())
	}
}

// TestMessengerWithoutAProjectRunsInTheMachineWorkspace: the one-shot messenger (the
// 传话人 path) takes the same fallback, so a Claude session outside every project can
// still be woken.
func TestMessengerWithoutAProjectRunsInTheMachineWorkspace(t *testing.T) {
	s, ws := newNoProjectServer(t)
	bin := testcmd.Path(t)
	probe := filepath.Join(ws, "messenger.txt")

	id, err := s.msgr.SubmitMessenger("", "local", ".",
		[]string{bin, "write-files", probe, "ran"}, "session messenger · peer-x", "alice")
	if err != nil {
		t.Fatalf("SubmitMessenger(no project): %v", err)
	}
	if final, ok := s.jobs.Wait(id); !ok || final.Status != job.StatusDone {
		t.Fatalf("messenger job = %s (%s), %v, want done", final.Status, final.Error, ok)
	}
	got, ok := s.jobs.Get(id)
	if !ok || got.ProjectKey != config.DefaultProjectKey {
		t.Fatalf("messenger job project = %q, want %q", got.ProjectKey, config.DefaultProjectKey)
	}
	if _, err := os.Stat(probe); err != nil {
		t.Fatalf("the messenger did not run in the default workspace: %v", err)
	}
}
