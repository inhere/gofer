package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
)

func TestSessionEndDuringShutdownRecordsDone(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{Delay: 800 * time.Millisecond})
	created := submitSmokeSession(t, s, 30)
	waitSessionStatus(t, s, created.ID, StatusRunning, 5*time.Second)
	if err := s.EndSession(created.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Get(created.ID)
	if !current.SessionEnding {
		t.Fatal("end request is not visible as ending")
	}
	rec, ok, err := s.meta.GetJob(created.ID)
	if err != nil || !ok || !strings.Contains(rec.SessionStateJSON, `"session_ending":true`) {
		t.Fatalf("ending was not persisted: ok=%v err=%v state=%s", ok, err, rec.SessionStateJSON)
	}
	if err := s.ShutdownResidentSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	final, ok := s.WaitFor(created.ID, 5*time.Second)
	if !ok || final.Status != StatusDone || final.SessionEndReason != "manual_end" {
		t.Fatalf("manual end during shutdown: ok=%v result=%+v", ok, final)
	}
}

func TestSessionEndCompletesPromptly(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{
		GrandchildPidFile: filepath.Join(t.TempDir(), "child.pid"),
		GrandchildHold:    time.Minute,
		IgnoreStdinEOF:    true,
	})
	created := submitSmokeSession(t, s, 30)
	waitSessionTurn(t, s, created.ID, 1)
	maxEndDuration := 3 * time.Second
	started := time.Now()
	if err := s.EndSession(created.ID); err != nil {
		t.Fatal(err)
	}
	final, ok := s.WaitFor(created.ID, maxEndDuration)
	if !ok || final.Status != StatusDone || time.Since(started) > maxEndDuration {
		t.Fatalf("end exceeded %s: ok=%v result=%+v elapsed=%s", maxEndDuration, ok, final, time.Since(started))
	}
	raw, err := os.ReadFile(filepath.Join(final.ResultDir, "artifacts", "acp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"t":"turn_ended"`) || !strings.Contains(string(raw), `"t":"stop"`) {
		t.Fatalf("ACP tail lost turn or stop frame: %s", raw)
	}
}
