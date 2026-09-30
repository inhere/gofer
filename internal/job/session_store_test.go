package job

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func TestSessionStoreScanFallback(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "work")
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)
	writeSession := func(name, dir, id string, mtime time.Time) {
		t.Helper()
		path := filepath.Join(root, name+"_"+id+".jsonl")
		body := fmt.Sprintf("{\"type\":\"session\",\"id\":%q,\"cwd\":%q}\n", id, dir)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	writeSession("old", cwd, "old-session-id", start.Add(-time.Second))
	writeSession("wrong-cwd", other, "other-session-id", start.Add(20*time.Second))
	writeSession("right", cwd, "right-session-id", start.Add(10*time.Second))
	got := scanSessionStore(filepath.Join(root, "*.jsonl"), `([a-z-]+session-id)`, cwd, start)
	if got != "right-session-id" {
		t.Fatalf("scanSessionStore = %q, want right-session-id", got)
	}
}

func TestSessionStoreScanFallbackRunningJob(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "session-123e4567-e89b-42d3-a456-426614174000.jsonl")
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"fake"}, AllowedRunners: []string{"local"}, AllowInteractive: boolPtr(true)},
		},
		Agents: map[string]config.AgentConfig{
			"fake": {
				Type: agent.TypeCLIAgent, Command: testcmd.Path(t), InteractiveArgs: []string{"pty-store-session", sessionPath, "123e4567-e89b-42d3-a456-426614174000"},
				ExitKeys: []string{"/exit", "enter"}, SessionStoreGlob: filepath.Join(root, "*.jsonl"),
				SessionStoreIDRegex: `([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`,
			},
		},
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	s := drainOnClose(t, NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg), map[string]runner.Runner{"local": ptyrunner.New()}, meta, nil))
	res, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "fake", Runner: "local", Interactive: true, Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := s.Get(res.ID)
		if ok && got.SessionID != "" {
			if got.SessionID != "123e4567-e89b-42d3-a456-426614174000" {
				t.Fatalf("session id = %q", got.SessionID)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := s.Get(res.ID)
	if got.SessionID == "" {
		t.Fatal("session id was not discovered while job was running")
	}
	if err := s.Cancel(res.ID); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Wait(res.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", final.Status)
	}
}

func TestSessionStoreCandidateYieldsToPtyBanner(t *testing.T) {
	root := t.TempDir()
	s := newCodexCaptureService(t, root, "")
	resultDir := t.TempDir()
	const bannerID = "123e4567-e89b-42d3-a456-426614174001"
	if err := os.WriteFile(filepath.Join(resultDir, store.PtyTranscriptFile), []byte("codex resume "+bannerID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := &jobEntry{result: JobResult{ID: "banner-test", Agent: "codex", Interactive: true, SessionID: "123e4567-e89b-42d3-a456-426614174000"}, storeSessionCandidate: true}
	s.captureSession(entry, resultDir)
	if entry.result.SessionID != bannerID {
		t.Fatalf("session id = %q, want banner id", entry.result.SessionID)
	}
}

func TestInteractiveWithoutSessionRecoveryKeepsEmpty(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "session-123e4567-e89b-42d3-a456-426614174000.jsonl")
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"plain"}, AllowedRunners: []string{"local"}, AllowInteractive: boolPtr(true)},
		},
		Agents: map[string]config.AgentConfig{
			"plain": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), InteractiveArgs: []string{"pty-store-session", sessionPath, "123e4567-e89b-42d3-a456-426614174000"}, ExitKeys: []string{"/exit", "enter"}},
		},
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	s := drainOnClose(t, NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg), map[string]runner.Runner{"local": ptyrunner.New()}, meta, nil))
	res, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "plain", Runner: "local", Interactive: true, Cwd: ".", TimeoutSec: 30})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sessionPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("fake agent did not create its session file: %v", err)
	}
	if err := s.Cancel(res.ID); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Wait(res.ID)
	if final.Status != StatusCancelled || final.SessionID != "" {
		t.Fatalf("final = %s/%q, want cancelled with no session", final.Status, final.SessionID)
	}
}
