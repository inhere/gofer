package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// serverRestartState polls GET /v1/server/restart for the restart's phase.
func serverRestartState(t *testing.T, s *Server) *ServerRestartState {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/server/restart", testToken, nil)
	defer resp.Body.Close()
	var view serverRestartView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode GET /v1/server/restart: %v", err)
	}
	return view.State
}

// A restart is refused by a server that cannot replace itself (a foreground `gofer
// serve`); the same answer greys the console's button out.
func TestServerRestartNeedsManagedServer(t *testing.T) {
	s := newTestServer(t, testToken, false)
	s.SetUpdateApply(t.TempDir(), nil)

	resp := do(t, s, http.MethodGet, "/v1/server/restart", testToken, nil)
	var view serverRestartView
	_ = json.NewDecoder(resp.Body).Decode(&view)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || view.Managed || view.State != nil {
		t.Fatalf("GET = %d %+v, want 200 and unmanaged with no state", resp.StatusCode, view)
	}
	if resp := do(t, s, http.MethodPost, "/v1/server/restart", testToken, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST = %d, want 409", resp.StatusCode)
	}
}

// A managed restart waits for idle and hands a staged copy of the running executable to
// the managed handover; the next process records the restart as done.
func TestServerRestartHandsOverStagedCopyAndResumesAsDone(t *testing.T) {
	s, handed := newWaitIdleServer(t)
	setApplyWaitTuning(t, 60, 20*time.Millisecond)

	body, _ := json.Marshal(updateApplyRequest{WaitIdleSec: 60})
	resp := rawDo(t, s, http.MethodPost, "/v1/server/restart", testToken, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST = %d, want 202", resp.StatusCode)
	}
	var binary string
	select {
	case binary = <-handed:
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("the managed handover was not called")
	}
	if !strings.Contains(binary, restartStageDir) {
		t.Fatalf("handover binary = %q, want a staged copy under %s", binary, restartStageDir)
	}
	if st := serverRestartState(t, s); st == nil || st.Phase != applyRestarting {
		t.Fatalf("state while handing over = %+v, want restarting", st)
	}
	// The restarted server's own startup settles the state.
	s.ResumeServerRestart()
	if st := serverRestartState(t, s); st == nil || st.Phase != applyDone {
		t.Fatalf("state after the restart = %+v, want done", st)
	}
}

// A cancel stops a restart that is still waiting for the machine to go idle: nothing is
// handed over and the parked jobs run again.
func TestServerRestartWaitCancelled(t *testing.T) {
	s, handed := newWaitIdleServer(t)
	setApplyWaitTuning(t, 3600, 20*time.Millisecond)
	blocker := submitCmdJob(t, s, []string{testcmd.Path(t), "stdout-lines", "tick", "200", "50ms"})
	waitJobStatus(t, s, blocker.ID, "running")

	body, _ := json.Marshal(updateApplyRequest{WaitIdleSec: 3600})
	resp := rawDo(t, s, http.MethodPost, "/v1/server/restart", testToken, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST = %d, want 202", resp.StatusCode)
	}
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the restart to report what it waits for", func() bool {
		st := serverRestartState(t, s)
		return st != nil && st.Phase == applyWaitingIdle && len(st.Waiting) > 0
	})

	if resp := do(t, s, http.MethodDelete, "/v1/server/restart", testToken, nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("DELETE = %d, want 202", resp.StatusCode)
	}
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the restart to be cancelled", func() bool {
		st := serverRestartState(t, s)
		return st != nil && st.Phase == applyCancelled
	})
	select {
	case b := <-handed:
		t.Fatalf("a cancelled wait handed over %q", b)
	case <-time.After(200 * time.Millisecond):
	}
	// A cancelled wait releases what it parked, exactly like the update's cancel.
	waitJobStatus(t, s, blocker.ID, "running")
}

// GET carries the live wait census, so the console can list the jobs the restart is
// waiting for instead of only saying "waiting".
func TestServerRestartStatusReportsWaitCensus(t *testing.T) {
	s, _ := newWaitIdleServer(t)
	setApplyWaitTuning(t, 3600, 20*time.Millisecond)
	stopRestartWaitOnCleanup(t, s)
	blocker := submitCmdJob(t, s, []string{testcmd.Path(t), "stdout-lines", "tick", "200", "50ms"})
	waitJobStatus(t, s, blocker.ID, "running")

	body, _ := json.Marshal(updateApplyRequest{WaitIdleSec: 3600})
	resp := rawDo(t, s, http.MethodPost, "/v1/server/restart", testToken, body)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the census to list the blocking job", func() bool {
		st := serverRestartState(t, s)
		if st == nil {
			return false
		}
		for _, w := range st.Waiting {
			if w.ID == blocker.ID {
				return true
			}
		}
		return false
	})
}

// stopRestartWaitOnCleanup cancels a restart that is still waiting for idle when the test
// ends and waits until its goroutine has left the wait loop. Registered AFTER
// setApplyWaitTuning, it runs BEFORE that cleanup restores the tuning globals — the wait
// loop reads them, so a restart left waiting raced the restore under -race (G051).
func stopRestartWaitOnCleanup(t *testing.T, s *Server) {
	t.Helper()
	t.Cleanup(func() {
		st := serverRestartState(t, s)
		if st == nil || st.Phase != applyWaitingIdle {
			return
		}
		resp := do(t, s, http.MethodDelete, "/v1/server/restart", testToken, nil)
		resp.Body.Close()
		wait.Until(t, wait.Timeout(t, 10*time.Second), "the restart wait to end", func() bool {
			st := serverRestartState(t, s)
			return st == nil || st.Phase != applyWaitingIdle
		})
	})
}
