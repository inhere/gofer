package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp"
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
)

// acpTestPrompt is the prompt every acp-agent test job runs.
const acpTestPrompt = "List the files in this directory and explain what this repository is in three sentences."

// newACPService wires a Service whose only agent is an acp-agent running the repo's
// fake ACP server, with the acp runner registered under its own key (the local
// runner stays registered too, mirroring core.Build). The project sets no approval
// policy, i.e. the gate is OFF (the S0 auto-allow behaviour).
func newACPService(t *testing.T, root string, o acptest.Options) *Service {
	t.Helper()
	return newACPServiceWith(t, root, o, nil, "")
}

// newACPServiceWith is newACPService with the approval gate configured: ap is the
// project's `approval` block (nil = unset) and agentPolicy the agent's
// acp.permission_policy tightening ("" = not set).
func newACPServiceWith(t *testing.T, root string, o acptest.Options, ap *config.ApprovalConfig, agentPolicy string) *Service {
	t.Helper()
	agentCfg := config.AgentConfig{Type: agent.TypeACPAgent, Command: testcmd.Path(t), Args: acptest.CmdArgs(o)}
	if agentPolicy != "" {
		agentCfg.ACP = &config.ACPConfig{PermissionPolicy: agentPolicy}
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"acpbot"},
				AllowedRunners: []string{"local"},
				Approval:       ap,
			},
		},
		Agents: map[string]config.AgentConfig{"acpbot": agentCfg},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		acprunner.Name:   acprunner.New(),
	}
	meta, err := jobstore.Open(jobstoreDBPath(root))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	return NewService(cfg, projReg, agentReg, runners, meta, nil)
}

// acpSubmit runs one acp-agent prompt job and returns the terminal result.
func acpSubmit(t *testing.T, s *Service, timeoutSec int) JobResult {
	t.Helper()
	return submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt,
		Cwd:    ".", TimeoutSec: timeoutSec,
	})
}

// readACPJSONL returns the parsed acp.jsonl event lines under a job's result dir.
func readACPJSONL(t *testing.T, resultDir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(resultDir, "artifacts", "acp.jsonl"))
	if err != nil {
		t.Fatalf("read acp.jsonl: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("acp.jsonl line is not valid JSON: %q: %v", line, err)
		}
		out = append(out, obj)
	}
	return out
}

// TestACPAgentJobWritesStdoutAndEvents is the end-to-end S0 proof: an acp-agent job
// against the fake ACP server lands pure agent text on stdout.log, the structured
// events in acp.jsonl (tool call through its three statuses + plan + thought), the
// session id on the job row, and job.tool_call events in the job event log.
func TestACPAgentJobWritesStdoutAndEvents(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.SessionID != acptest.SessionID {
		t.Fatalf("session_id = %q, want %q", final.SessionID, acptest.SessionID)
	}
	if final.StopReason != acp.StopEndTurn {
		t.Fatalf("stop_reason = %q, want %q", final.StopReason, acp.StopEndTurn)
	}

	// stdout.log carries the agent's text and NOTHING of the protocol.
	out, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(final.ID, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("read stdout.log: %v", err)
	}
	if got, want := string(out), acptest.TextHello+acptest.TextWorld+acptest.TextDone; got != want {
		t.Fatalf("stdout.log = %q, want the merged agent text %q", got, want)
	}
	if strings.Contains(string(out), "sessionUpdate") || strings.Contains(string(out), "jsonrpc") {
		t.Fatalf("protocol frames leaked into stdout.log: %q", out)
	}

	lines := readACPJSONL(t, final.ResultDir)
	var toolStatuses []string
	var plans, thoughts int
	for _, l := range lines {
		switch l["t"] {
		case "tool_call":
			status, _ := l["status"].(string)
			toolStatuses = append(toolStatuses, status)
		case "plan":
			plans++
		case "thought":
			thoughts++
		}
	}
	if got, want := strings.Join(toolStatuses, ","), "pending,in_progress,completed"; got != want {
		t.Fatalf("acp.jsonl tool_call statuses = %q, want %q (lines=%v)", got, want, lines)
	}
	if plans != 1 {
		t.Fatalf("acp.jsonl plan lines = %d, want 1", plans)
	}
	if thoughts != 1 {
		t.Fatalf("acp.jsonl thought lines = %d, want 1", thoughts)
	}

	// acp.jsonl is an artifact of the job.
	if !strings.Contains(final.ArtifactsJSON, `"name":"acp.jsonl"`) {
		t.Fatalf("artifacts manifest does not list acp.jsonl: %s", final.ArtifactsJSON)
	}
	manifest, ok := s.GetArtifactManifest(final.ID)
	if !ok || !strings.Contains(manifestJSON(t, manifest), "acp.jsonl") {
		t.Fatalf("GetArtifactManifest does not list acp.jsonl: %s", manifestJSON(t, manifest))
	}
	// Status changes surface as job events (job.tool_call), one per change.
	events, err := s.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var transitions []string
	for _, e := range events {
		if e.Type != EventJobToolCall {
			continue
		}
		var d struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal([]byte(e.Detail), &d)
		transitions = append(transitions, d.Status)
	}
	if got, want := strings.Join(transitions, ","), "pending,in_progress,completed"; got != want {
		t.Fatalf("job.tool_call statuses = %q, want %q (events=%v)", got, want, events)
	}
}

// manifestJSON marshals an artifact manifest for assertion messages.
func manifestJSON(t *testing.T, m ArtifactManifest) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(b)
}

// TestACPAgentRefusalFails proves the refusal stopReason maps to a failed job that
// still records the reason.
func TestACPAgentRefusalFails(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{StopReason: acp.StopRefusal})

	final := acpSubmit(t, s, 30)
	if final.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	if final.StopReason != acp.StopRefusal {
		t.Fatalf("stop_reason = %q, want %q", final.StopReason, acp.StopRefusal)
	}
	if !strings.Contains(final.Error, "refus") {
		t.Fatalf("error = %q, want it to mention the refusal", final.Error)
	}
	// The session id is still recorded: the agent answered, it just refused.
	if final.SessionID != acptest.SessionID {
		t.Fatalf("session_id = %q, want %q", final.SessionID, acptest.SessionID)
	}
}

// TestACPAgentTimeoutCancels proves the timeout path: the job's deadline cancels
// the turn (session/cancel), the agent answers cancelled, and the job lands in
// timeout with the cancelled stopReason recorded.
func TestACPAgentTimeoutCancels(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{Slow: true})

	final := acpSubmit(t, s, 1)
	if final.Status != StatusTimeout {
		t.Fatalf("status = %s (err=%s), want timeout", final.Status, final.Error)
	}
	if final.StopReason != acp.StopCancelled {
		t.Fatalf("stop_reason = %q, want %q", final.StopReason, acp.StopCancelled)
	}
	if final.SessionID != acptest.SessionID {
		t.Fatalf("session_id = %q, want %q", final.SessionID, acptest.SessionID)
	}
}
