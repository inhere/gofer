package job

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

func TestDirLockWaitErrorShowsLockPaths(t *testing.T) {
	root := t.TempDir()
	s := dirlockService(t, root, []string{"excl-guard", "marker", "3s"}, func(cfg *config.Config) {
		cfg.Server.DirLockMaxWaitSec = intPtr(1)
	})
	lockPath := filepath.Join(root, "locked")
	holder := mustSubmit(t, s, JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", LockPaths: []string{"locked"}, Prompt: "holder"})
	waitForStatus(t, s, holder.ID, StatusRunning, 10*time.Second)
	req := exclusiveCmd(t, 30)
	req.LockPaths = []string{"locked"}
	waiter := mustSubmit(t, s, req)
	final := waitStatus(t, s, waiter.ID, 10*time.Second, StatusFailed, StatusDone)
	if final.Status != StatusFailed || !strings.Contains(final.Error, lockPath) || strings.Contains(final.Error, "dir "+root+")") {
		t.Fatalf("wait error = %q, want actual lock path %q rather than cwd %q", final.Error, lockPath, root)
	}
	events, err := s.ListJobEvents(waiter.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != EventJobDirWaitTimeout {
			continue
		}
		var detail struct {
			Dir string `json:"dir"`
		}
		if err := json.Unmarshal([]byte(event.Detail), &detail); err != nil {
			t.Fatal(err)
		}
		found = detail.Dir == lockPath
	}
	if !found {
		t.Fatalf("timeout event missing actual lock path %q: %+v", lockPath, events)
	}
}

func TestLockWaitOverridesServerCap(t *testing.T) {
	for _, tc := range []struct {
		name       string
		serverWait int
		jobWait    int
		want       string
	}{
		{"longer", 1, 3, StatusDone},
		{"shorter", 3, 1, StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := dirlockService(t, root, []string{"excl-guard", "marker", "2s"}, func(cfg *config.Config) {
				cfg.Server.DirLockMaxWaitSec = &tc.serverWait
			})
			holder := mustSubmit(t, s, JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", Prompt: "holder"})
			waitForStatus(t, s, holder.ID, StatusRunning, 10*time.Second)
			req := exclusiveCmd(t, 30)
			req.LockWaitSec = &tc.jobWait
			waiter := mustSubmit(t, s, req)
			final := waitStatus(t, s, waiter.ID, 10*time.Second, StatusDone, StatusFailed)
			if final.Status != tc.want {
				t.Fatalf("status=%s error=%q, want %s", final.Status, final.Error, tc.want)
			}
		})
	}
}

func TestLockWaitZeroRespectsServerSwitch(t *testing.T) {
	zero := 0
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[allowed], func(t *testing.T) {
			root := t.TempDir()
			s := dirlockService(t, root, []string{"excl-guard", "marker", "2s"}, func(cfg *config.Config) {
				cfg.Server.DirLockMaxWaitSec = intPtr(1)
				cfg.Server.DirLockAllowUnboundedWait = &allowed
			})
			req := exclusiveCmd(t, 30)
			req.LockWaitSec = &zero
			if !allowed {
				if _, err := s.Submit(req); err == nil || !strings.Contains(err.Error(), "dir_lock_allow_unbounded_wait") {
					t.Fatalf("zero wait should be rejected with switch hint, got %v", err)
				}
				return
			}
			holder := mustSubmit(t, s, JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", Prompt: "holder"})
			waitForStatus(t, s, holder.ID, StatusRunning, 10*time.Second)
			waiter := mustSubmit(t, s, req)
			final := waitStatus(t, s, waiter.ID, 10*time.Second, StatusDone, StatusFailed)
			if final.Status != StatusDone {
				t.Fatalf("unbounded wait status=%s error=%q", final.Status, final.Error)
			}
		})
	}
}
