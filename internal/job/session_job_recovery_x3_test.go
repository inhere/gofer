package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

func seedRecoverableSession(t *testing.T, s *Service, root, id, status string) {
	t.Helper()
	request, err := json.Marshal(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "first", Session: true, TimeoutSec: 30, IdleTimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "self", id)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, store.StdoutFile), []byte("old turn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Status: status, Cwd: root, ResultDir: dir, RequestJSON: string(request),
		SessionID: acptest.SessionID, SessionStateJSON: `{"session":true,"turn_no":1,"idle_timeout_sec":30}`,
		StartedAt: time.Now().Unix() - 10, UpdatedAt: time.Now().Unix() - 10,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionJobRecoversWithSessionLoad(t *testing.T) {
	for _, status := range []string{StatusAwaitingInput, StatusRunning} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			s := newACPService(t, root, acptest.Options{})
			seedRecoverableSession(t, s, root, "recover-session", status)
			if _, err := s.ReconcileOrphanJobs(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(6 * time.Second)
			for time.Now().Before(deadline) {
				current, _ := s.Get("recover-session")
				if s.entry("recover-session") != nil && current.Status == StatusAwaitingInput {
					if err := s.SaySession(current.ID, "next"); err != nil {
						t.Fatal(err)
					}
					waitSessionTurn(t, s, current.ID, 2)
					if err := s.EndSession(current.ID); err != nil {
						t.Fatal(err)
					}
					final, _ := s.Wait(current.ID)
					if final.Status != StatusDone || final.ID != current.ID {
						t.Fatalf("recovered final=%+v", final)
					}
					if out := readJobLog(t, final, store.StdoutFile); !strings.Contains(out, "old turn") {
						t.Fatalf("recovery replaced earlier output: %q", out)
					}
					if log := readJobLog(t, final, store.StderrFile); !strings.Contains(log, "session/load sid="+acptest.SessionID) {
						t.Fatalf("agent did not load session: %s", log)
					}
					return
				}
				if IsTerminal(current.Status) {
					t.Fatalf("recovery reached %s: %s", current.Status, current.Error)
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("session was not reloaded into awaiting_input")
		})
	}
}

func waitSessionTurn(t *testing.T, s *Service, id string, turn int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, _ := s.Get(id)
		if result.Status == StatusAwaitingInput && result.TurnNo == turn {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s did not finish turn %d", id, turn)
}

func TestSessionJobFailsWhenSessionLoadUnsupported(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{RefuseLoad: true})
	seedRecoverableSession(t, s, root, "no-load-session", StatusAwaitingInput)
	if _, err := s.ReconcileOrphanJobs(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		result, _ := s.Get("no-load-session")
		if result.Status == StatusFailed {
			if !strings.Contains(result.Error, "session/load") {
				t.Fatalf("failure does not explain session/load: %s", result.Error)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("unsupported session/load was not failed")
}

func TestSessionJobReleasesResourcesAfterRecoveryFailure(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{RefuseLoad: true})
	seedRecoverableSession(t, s, root, "release-on-fail", StatusAwaitingInput)
	if _, err := s.ReconcileOrphanJobs(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		result, _ := s.Get("release-on-fail")
		if result.Status == StatusFailed {
			next, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "next", TimeoutSec: 5})
			if err != nil {
				t.Fatal(err)
			}
			if final, ok := s.WaitFor(next.ID, 5*time.Second); !ok || final.Status != StatusDone {
				t.Fatalf("next job blocked by recovery: ok=%v result=%+v", ok, final)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("recovery did not fail")
}

func TestSessionJobRejectsPeerRunner(t *testing.T) {
	s := newWorkerTestService(t, t.TempDir(), &stubWorkerRunner{})
	_, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "peer", Cmd: []string{"echo", "hi"}, Session: true})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "持续会话目前仅支持本机 runner") {
		t.Fatalf("remote session admission error=%v", err)
	}
}
