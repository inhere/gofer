package worker_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/worker"
)

// buildWorkerWithACP stands up a worker side whose only agent is the repo's fake ACP
// agent under an `ask` approval policy (GATE-01 §1). The worker executes the job with
// its own job.Service, so the permission interaction is raised THERE and must be
// mirrored to the hub like any other interaction.
func buildWorkerWithACP(t *testing.T, hubURL string) *worker.Client {
	t.Helper()
	host := t.TempDir()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"alpha": {
				HostPath:       host,
				AllowedAgents:  []string{"acpbot"},
				AllowedRunners: []string{"local"},
				Approval:       &config.ApprovalConfig{Mode: config.ApprovalAsk, TimeoutSec: 120},
			},
		},
		Agents: map[string]config.AgentConfig{
			"acpbot": {Type: agent.TypeACPAgent, Command: testcmd.Path(t), Args: acptest.CmdArgs(acptest.Options{})},
		},
	}
	config.ApplyDefaults(cfg)
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	st, err := jobstore.Open(root + "/worker.db")
	if err != nil {
		t.Fatalf("open worker jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	localJobs := job.NewService(cfg, projReg, agentReg, map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		acprunner.Name:   acprunner.New(),
	}, st, nil)
	localJobs.SetWorkflow(workflow.NewEngine(localJobs))

	wsURL := "ws" + strings.TrimPrefix(hubURL, "http") + "/v1/workers/connect"
	return worker.New(worker.Config{
		WorkerID: e2eWorkerID,
		URLs:     []string{wsURL},
		Token:    e2eToken,
		Projects: []string{"alpha"},
		Agents:   []string{"acpbot"},
	}, localJobs)
}

// TestPermissionInteractionMirroredToHub is the S1 worker acceptance gate: an
// acp-agent job running ON the worker parks on its approval gate, the `permission`
// interaction (tool call + ACP options) mirrors up to the hub, and answering it on
// the hub flows back over WS so the blocked agent receives the option and the job
// completes. The worker's frames carry the new fields verbatim; an old worker simply
// never raises such an interaction.
func TestPermissionInteractionMirroredToHub(t *testing.T) {
	hub := buildHubSide(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cli := buildWorkerWithACP(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cli.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "List the files in this directory.", Cwd: ".", TimeoutSec: 60,
	})
	if created.ID == "" {
		t.Fatal("created job has no id")
	}

	// 1) the worker's approval gate surfaces on the HUB as a permission interaction,
	//    carrying the tool call the agent wants to run and the agent's own options.
	overall := time.Now().Add(45 * time.Second)
	var got map[string]any
	for time.Now().Before(overall) {
		for _, it := range hubInteractions(t, hub.ts, created.ID) {
			if it["status"] == "pending" {
				got = it
				break
			}
		}
		if got != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got == nil {
		snap, _ := hub.jobs.Get(created.ID)
		t.Fatalf("no pending interaction surfaced on the hub; hub job status=%s", snap.Status)
	}
	if got["type"] != job.InteractionTypePermission {
		t.Fatalf("hub interaction type = %v, want %q", got["type"], job.InteractionTypePermission)
	}
	tc, _ := got["tool_call"].(map[string]any)
	if tc == nil || tc["id"] != acptest.ToolCallID || tc["kind"] != "edit" {
		t.Fatalf("hub interaction tool_call = %v, want id=%s kind=edit", got["tool_call"], acptest.ToolCallID)
	}
	opts, _ := got["options"].([]any)
	if len(opts) != 3 {
		t.Fatalf("hub interaction options = %v, want the agent's three", got["options"])
	}
	if first, _ := opts[0].(map[string]any); first["kind"] != "allow_once" || first["id"] != acptest.AllowOnceOptionID {
		t.Fatalf("hub option[0] = %v, want the ACP kind+id of allow_once", opts[0])
	}
	if hint, _ := got["policy_hint"].(string); !strings.Contains(hint, "ask") {
		t.Fatalf("hub interaction policy_hint = %q, want it to name the mode", got["policy_hint"])
	}
	if snap, _ := hub.jobs.Get(created.ID); snap.Status != job.StatusPendingInteraction {
		t.Fatalf("hub job status = %s, want pending_interaction", snap.Status)
	}

	// 2) answering on the hub reaches the blocked agent: the turn runs to done and the
	//    agent's own record of the answer mirrors back in the job's stderr log.
	iid, _ := got["id"].(string)
	answerHub(t, hub.ts, created.ID, iid, acptest.AllowOnceOptionID)

	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("hub job status = %s (err=%s), want done", final.Status, final.Error)
	}
	stderr := getLogs(t, hub.ts, created.ID, "stderr")
	if !strings.Contains(stderr, "permission outcome=selected option="+acptest.AllowOnceOptionID) {
		t.Fatalf("worker agent did not receive the hub answer; stderr=%q", stderr)
	}

	stopWorkerClient(t, cli, cancel, clientErr)
}
