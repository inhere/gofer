package job

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// T9's acceptance substitute for "更新暂停期间提交的子 job 也有回执" (plan 范围: 验收例外).
// An update pauses STARTS, not receipts: a child submitted while the pause is on parks,
// runs once the update releases it, and its outcome still reaches the parent session —
// on the server this process runs, and on a parent whose worker session came back after
// a restart.

// localResidentService wires a Service with a resident LOCAL acp session agent ("acpbot",
// echoing every prompt into its stdout) and a plain exec agent for the parked child.
func localResidentService(t *testing.T, root string) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath: root, AllowedAgents: []string{"acpbot", "exec"},
				AllowedRunners: []string{"local"}, AllowExec: true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"acpbot": {Type: agent.TypeACPAgent, Command: bin, Args: acptest.CmdArgs(acptest.Options{EchoPrompt: true})},
			"exec":   {Type: agent.TypeExec},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New(), acprunner.Name: acprunner.New()}
	meta, err := jobstore.Open(jobstoreDBPath(root))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
}

// parkedChildRequest is a plain exec job that only creates the probe file: the evidence
// that the released job ran.
func parkedChildRequest(bin, probe string) JobRequest {
	return JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{bin, "write-files", probe, "ran"}, Cwd: ".", TimeoutSec: 30,
	}
}

// TestSweepPendingSaysUpdatePausedLocalParent: a child submitted during an update pause
// parks, and the receipt of its outcome still reaches the resident parent on THIS server
// once the update releases it. The say is a real turn: the fake agent echoes the prompt it
// received into the job's stdout.
func TestSweepPendingSaysUpdatePausedLocalParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := localResidentService(t, root)
	probe := filepath.Join(root, "child.txt")

	parent, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "supervise the children", Session: true, TimeoutSec: 120, IdleTimeoutSec: 120,
	})
	if err != nil {
		t.Fatalf("submit the resident parent: %v", err)
	}
	waitAwaitingInput(t, s, parent.ID, 0)

	s.PauseStartsForUpdate("apply-t9")
	child, err := s.Submit(parkedChildRequest(bin, probe))
	if err != nil {
		t.Fatalf("Submit(paused child): %v", err)
	}
	if child.Status != StatusPausedForUpdate {
		t.Fatalf("child status = %s, want %s", child.Status, StatusPausedForUpdate)
	}
	w := addChildReceipt(t, s, jobstore.WakeupRecord{
		JobID: parent.ID, FilterJobID: child.ID, AutoChild: 1,
	})

	if released, rerr := s.ResumeStartsForUpdate(); rerr != nil || released != 1 {
		t.Fatalf("ResumeStartsForUpdate = %d, %v, want the parked child released", released, rerr)
	}
	final, ok := s.Wait(child.ID)
	if !ok || final.Status != StatusDone {
		t.Fatalf("released child = %s (%s), %v", final.Status, final.Error, ok)
	}

	// The receipt reaches the parent as a new turn: the agent echoes the prompt it got.
	var logText string
	wait.For(t, 20*time.Second, "the resident parent to be woken by the child receipt", func() (bool, any) {
		parentNow, _ := s.Get(parent.ID)
		logText = readJobLog(t, parentNow, store.StdoutFile)
		return strings.Contains(logText, "ECHO:") && strings.Contains(logText, child.ID), logText
	})
	assert.True(t, strings.Contains(logText, StatusDone), "the echoed receipt = %q, want the child's state", logText)
	if row := childWakeRow(t, s, w.ID); row.ContinuationJobID == "" && row.Enabled == 1 {
		t.Fatalf("wakeup %s = %+v, want the receipt delivered (pending or consumed)", w.ID, row)
	}

	if err := s.EndSession(parent.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	ended, _ := s.Wait(parent.ID)
	assert.Eq(t, StatusDone, ended.Status, "the parent session did not end cleanly")
}

// TestSweepPendingSaysUpdatePausedWorkerParentRestart: the same child, but the parent is a
// WORKER session and the server restarts between the submit and the release (the shape of
// TestUpdatePausedSurvivesRestart). The new process reconciles, releases the parked child,
// and the receipt is still delivered — the parked receipt state lives in the database.
func TestSweepPendingSaysUpdatePausedWorkerParentRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	probe := filepath.Join(root, "child.txt")

	first, _ := pendingSayService(t, root)
	// The worker parent was mid-turn when the update started; nothing was said to it.
	addRemoteSaySession(t, first, "parent-job", StatusRunning, 1)
	first.PauseStartsForUpdate("apply-t9-worker")
	child, err := first.Submit(parkedChildRequest(bin, probe))
	if err != nil {
		t.Fatalf("Submit(paused child): %v", err)
	}
	if child.Status != StatusPausedForUpdate {
		t.Fatalf("child status = %s, want %s", child.Status, StatusPausedForUpdate)
	}
	addChildReceipt(t, first, jobstore.WakeupRecord{
		JobID: "parent-job", FilterJobID: child.ID, AutoChild: 1,
	})

	// The process restarts: only the database survives.
	second, secondSender := pendingSayService(t, root)
	if _, rerr := second.ReconcileOrphanJobs(); rerr != nil {
		t.Fatalf("ReconcileOrphanJobs: %v", rerr)
	}
	// The worker is back and reports the session idle again; the tick has nothing to say yet.
	addRemoteSaySession(t, second, "parent-job", StatusAwaitingInput, 2)
	second.SweepPendingSays(second.nowFn())
	assert.Eq(t, 0, len(sayTexts(secondSender)), "a receipt was spoken before its child ran")

	if released, rerr := second.ResumeStartsForUpdate(); rerr != nil || released != 1 {
		t.Fatalf("ResumeStartsForUpdate = %d, %v, want the parked child released", released, rerr)
	}
	final, ok := second.Wait(child.ID)
	if !ok || final.Status != StatusDone {
		t.Fatalf("released child = %s (%s), %v", final.Status, final.Error, ok)
	}

	texts := waitForSay(t, secondSender)
	assert.True(t, strings.Contains(texts[0], child.ID), "say = %q, want the released child", texts[0])
	assert.True(t, strings.Contains(texts[0], StatusDone), "say = %q, want its state", texts[0])
	assert.False(t, strings.Contains(texts[0], "supervise"), "say carried the parent's prompt: %q", texts[0])
}
