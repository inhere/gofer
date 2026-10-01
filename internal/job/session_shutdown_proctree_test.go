package job

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/proctree"
	"github.com/inhere/gofer/internal/store"
)

func TestSessionJobShutdownKillsAgentProcessTree(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	opts := acptest.Options{GrandchildPidFile: pidFile, GrandchildHold: time.Minute}
	s := newACPService(t, root, opts)
	created := submitSmokeSession(t, s, 30)
	waitSessionTurn(t, s, created.ID, 1)
	childPID := waitChildPID(t, pidFile)
	if !proctree.Alive(childPID) {
		t.Fatal("fake agent child exited before shutdown")
	}
	if err := s.ShutdownResidentSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertChildGone(t, childPID)
	current, _ := s.Get(created.ID)
	if current.Status != StatusAwaitingInput || current.SessionID == "" {
		t.Fatalf("shutdown lost recoverable state: %+v", current)
	}

	// The next serve instance opens the same temporary store and must load the session.
	s2 := newACPService(t, root, opts)
	if _, err := s2.ReconcileOrphanJobs(); err != nil {
		t.Fatal(err)
	}
	waitSessionTurn(t, s2, created.ID, 1)
	if err := s2.SaySession(created.ID, "after restart"); err != nil {
		t.Fatal(err)
	}
	waitSessionTurn(t, s2, created.ID, 2)
	if err := s2.EndSession(created.ID); err != nil {
		t.Fatal(err)
	}
	final, _ := s2.Wait(created.ID)
	if final.Status != StatusDone || !strings.Contains(readJobLog(t, final, store.StderrFile), "session/load sid=") {
		t.Fatalf("session/load recovery failed: %+v", final)
	}
}
