package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// testCore builds the registries + job.Service over a temp result root with a
// single project "self" that allows the built-in exec agent and the local
// runner. Mirrors the httpapi/job test fixtures.
func testCore(t *testing.T) (*job.Service, *project.Registry, *agent.Registry, *presence.Service) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root, // any existing dir; cwd "." resolves here
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	jobs := job.NewService(cfg, projects, agents, runners, meta, nil)
	pres := presence.NewService(meta)
	return jobs, projects, agents, pres
}

// connect wires an in-memory client<->server session over the mcpserver. The
// returned session and jobs handle let tests drive tools and wait on jobs.
func connect(t *testing.T) (*mcp.ClientSession, *job.Service) {
	t.Helper()
	jobs, projects, agents, pres := testCore(t)
	return connectTo(t, NewLocal(jobs, projects, agents, pres)), jobs
}

// connectTo wires an in-memory client<->server session over the given mcpserver
// (used directly when a test needs a server built with a non-default originAgent).
func connectTo(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	c2s, s2c := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, s2c, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := cli.Connect(ctx, c2s, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// structured decodes the structured output of a CallToolResult into out.
func structured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned error result: %+v", res.Content)
	}
	if res.StructuredContent == nil {
		t.Fatalf("tool result has no structured content")
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
}

func TestListToolsAllPresent(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"gofer_list_projects":      false,
		"gofer_list_agents":        false,
		"gofer_run_job":            false,
		"gofer_get_job":            false,
		"gofer_tail_log":           false,
		"gofer_cancel_job":         false,
		"gofer_get_interactions":   false,
		"gofer_answer_interaction": false,
		"gofer_get_artifacts":      false,
		"gofer_get_result":         false,
		// E36 presence/mailbox (4 tools).
		"gofer_register":      false,
		"gofer_poll_inbox":    false,
		"gofer_post_message":  false,
		"gofer_list_presence": false,
		// E25 supervisor discovery (1 tool).
		"gofer_list_pending_interactions": false,
	}
	for _, tl := range res.Tools {
		if _, ok := want[tl.Name]; ok {
			want[tl.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("tool %q missing from ListTools (got %d tools)", name, len(res.Tools))
		}
	}
	// All gofer_* tools must be registered (no more, no fewer).
	if len(res.Tools) != len(want) {
		t.Fatalf("expected %d tools, got %d: %+v", len(want), len(res.Tools), res.Tools)
	}
}

// TestRunJobInputSchemaSnakeCase asserts the SDK-derived input schema for
// gofer_run_job uses snake_case property names (matching the HTTP API).
func TestRunJobInputSchemaSnakeCase(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var runJob *mcp.Tool
	for _, tl := range res.Tools {
		if tl.Name == "gofer_run_job" {
			runJob = tl
			break
		}
	}
	if runJob == nil {
		t.Fatalf("gofer_run_job not found")
	}
	// InputSchema arrives as a map[string]any on the client; its properties keys
	// are the field names. Assert snake_case keys are present.
	b, err := json.Marshal(runJob.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("unmarshal input schema: %v", err)
	}
	for _, key := range []string{"project_key", "timeout_sec", "role", "system_prompt", "origin_agent", "escalate_to"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("input schema missing snake_case property %q; properties=%v", key, schema.Properties)
		}
	}
}

func TestListProjectsTool(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_list_projects"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out listProjectsOutput
	structured(t, res, &out)
	if len(out.Projects) != 1 || out.Projects[0].Key != "self" {
		t.Fatalf("unexpected projects: %+v", out.Projects)
	}
	if !out.Projects[0].AllowExec {
		t.Fatalf("expected allow_exec=true for self: %+v", out.Projects[0])
	}
}

func TestListAgentsTool(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_list_agents"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out listAgentsOutput
	structured(t, res, &out)
	if len(out.Agents) == 0 {
		t.Fatalf("expected non-empty agents")
	}
	var foundExec bool
	for _, a := range out.Agents {
		if a.Name == "exec" {
			foundExec = true
			if !a.Available {
				t.Fatalf("exec agent should be available: %+v", a)
			}
		}
	}
	if !foundExec {
		t.Fatalf("exec agent missing: %+v", out.Agents)
	}
}

func TestRunJobAndGet(t *testing.T) {
	session, jobs := connect(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         []string{"go", "version"},
			"cwd":         ".",
			"timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.ID == "" {
		t.Fatalf("run_job returned no id: %+v", created)
	}
	if created.ProjectKey != "self" || created.Agent != "exec" {
		t.Fatalf("unexpected job view: %+v", created)
	}

	// Wait for terminal state via the service, then re-query through the tool.
	final, ok := jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}

	getRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_job",
		Arguments: map[string]any{"id": created.ID},
	})
	if err != nil {
		t.Fatalf("CallTool get_job: %v", err)
	}
	var got jobView
	structured(t, getRes, &got)
	if got.Status != job.StatusDone || got.ExitCode != 0 {
		t.Fatalf("expected done/exit 0, got status=%s exit=%d", got.Status, got.ExitCode)
	}
}

// TestRunJobOriginAgentRoundTrip proves the supervisor-routing owner fields
// (origin_agent / escalate_to, P1.1) flow from the MCP run_job input through
// JobRequest → submit → persist and read back via get_job (jobView). P1.1 only
// 透传 the explicit input; the routing改写 is P1.2.
func TestRunJobOriginAgentRoundTrip(t *testing.T) {
	session, jobs := connect(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key":  "self",
			"agent":        "exec",
			"runner":       "local",
			"cmd":          []string{"go", "version"},
			"cwd":          ".",
			"timeout_sec":  30,
			"origin_agent": "agt_owner_1",
			"escalate_to":  "role-one:supervisor",
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.ID == "" {
		t.Fatalf("run_job returned no id: %+v", created)
	}
	if created.OriginAgent != "agt_owner_1" || created.EscalateTo != "role-one:supervisor" {
		t.Fatalf("run_job did not echo owner routing: %+v", created)
	}

	// Drive to terminal, then re-query through get_job (DB read path) to confirm
	// the values round-tripped through persistence (fromRecord), not just memory.
	if _, ok := jobs.Wait(created.ID); !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}
	getRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_job",
		Arguments: map[string]any{"id": created.ID},
	})
	if err != nil {
		t.Fatalf("CallTool get_job: %v", err)
	}
	var got jobView
	structured(t, getRes, &got)
	if got.OriginAgent != "agt_owner_1" {
		t.Fatalf("get_job origin_agent = %q, want agt_owner_1", got.OriginAgent)
	}
	if got.EscalateTo != "role-one:supervisor" {
		t.Fatalf("get_job escalate_to = %q, want role-one:supervisor", got.EscalateTo)
	}
}

// TestSelfRegisterInjectsOriginAgent proves P1.0: a self-registered mcp process
// stamps run_job submissions with its driver agent_id when the caller passes no
// explicit origin_agent, and that an explicit origin_agent still wins (不覆盖).
func TestSelfRegisterInjectsOriginAgent(t *testing.T) {
	jobs, projects, agents, pres := testCore(t)
	b := newLocalBackend(jobs, projects, agents, pres)

	// Self-register exactly as Serve does, then build the server over that owner.
	originAgent, originToken := selfRegister(b)
	if originAgent == "" {
		t.Fatalf("selfRegister returned empty agent_id")
	}
	session := connectTo(t, newServer(b, originAgent, originToken))

	// No explicit origin_agent -> auto-injected self-registered agent_id.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.OriginAgent != originAgent {
		t.Fatalf("auto-injected origin_agent = %q, want %q", created.OriginAgent, originAgent)
	}

	// Explicit origin_agent wins over the self-registered default.
	res2, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 30,
			"origin_agent": "agt_explicit",
		},
	})
	if err != nil {
		t.Fatalf("run_job(explicit): %v", err)
	}
	var created2 jobView
	structured(t, res2, &created2)
	if created2.OriginAgent != "agt_explicit" {
		t.Fatalf("explicit origin_agent = %q, want agt_explicit", created2.OriginAgent)
	}

	// Drain both background jobs before the temp store closes (cleanup ordering).
	jobs.Wait(created.ID)
	jobs.Wait(created2.ID)
}

// TestPollInboxDefaultsToSelfIdentity proves the P1.0 self-register identity is reused
// for inbox polls: gofer_poll_inbox with NO creds polls THIS mcp process's OWN inbox.
// This is what lets a claude/codex supervisor poll + answer + receive role-one routing
// under ONE consistent identity (mcp-<hash>) without the explicit gofer_register dance.
func TestPollInboxDefaultsToSelfIdentity(t *testing.T) {
	jobs, projects, agents, pres := testCore(t)
	b := newLocalBackend(jobs, projects, agents, pres)

	selfID, selfToken := selfRegister(b)
	if selfID == "" || selfToken == "" {
		t.Fatalf("selfRegister returned empty id/token: %q/%q", selfID, selfToken)
	}
	session := connectTo(t, newServer(b, selfID, selfToken))

	// Deliver an escalation to the self-registered agent's own inbox.
	if n, err := pres.Post("system", selfID, "escalation", "是否继续?", "job:demo"); err != nil || n != 1 {
		t.Fatalf("post to self: delivered=%d err=%v", n, err)
	}

	// Direct backend probe to isolate identity vs MCP layer.
	if msgs, derr := b.PollInbox(selfID, selfToken, false); derr != nil {
		t.Fatalf("direct PollInbox(self) err=%v (id=%q tok=%q)", derr, selfID, selfToken)
	} else if len(msgs) != 1 {
		t.Fatalf("direct PollInbox(self) got %d msgs, want 1", len(msgs))
	}

	// poll_inbox with NO creds -> uses the self identity -> sees the message.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_poll_inbox",
		Arguments: map[string]any{"ack": false},
	})
	if err != nil {
		t.Fatalf("poll_inbox(self): %v", err)
	}
	if res.IsError {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				t.Fatalf("poll_inbox(self) error result: %s", tc.Text)
			}
		}
		t.Fatalf("poll_inbox(self) error result: %+v", res.Content)
	}
	var out pollInboxOutput
	structured(t, res, &out)
	if len(out.Messages) != 1 || out.Messages[0].Body != "是否继续?" {
		t.Fatalf("self-poll got %+v, want 1 escalation '是否继续?'", out.Messages)
	}

	// Explicit (invalid) creds still take precedence — they do NOT fall back to self
	// (so the fallback can't be abused to read another identity's inbox).
	res2, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_poll_inbox",
		Arguments: map[string]any{"agent_id": "agt_other", "agent_token": "nope"},
	})
	// Correct outcomes: rejected (Go error OR IsError result), else an empty inbox —
	// NEVER a silent fall back to the self inbox.
	if err == nil && !res2.IsError {
		var out2 pollInboxOutput
		structured(t, res2, &out2)
		if len(out2.Messages) != 0 {
			t.Fatalf("explicit creds leaked self inbox: %+v", out2.Messages)
		}
	}
}

// TestSelfRegisterNameFormat asserts the per-process driver-agent name shape
// mcp-<hostHash>-<pid>: an 8-hex-char host hash and the live pid (pid embedding is
// what keeps two processes on one host from aliasing onto a single agent_id).
func TestSelfRegisterNameFormat(t *testing.T) {
	name := selfRegisterName()
	prefix := fmt.Sprintf("mcp-%s-", hostHash(mcpHostname()))
	if !strings.HasPrefix(name, prefix) {
		t.Fatalf("name %q missing prefix %q", name, prefix)
	}
	if pidPart := strings.TrimPrefix(name, prefix); pidPart != strconv.Itoa(os.Getpid()) {
		t.Fatalf("name pid part = %q, want %d", pidPart, os.Getpid())
	}
	// hostHash is 8 lowercase hex chars for any input.
	hh := hostHash("any-host.example")
	if len(hh) != 8 {
		t.Fatalf("hostHash len = %d, want 8 (%q)", len(hh), hh)
	}
	for _, c := range hh {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("hostHash %q has non-hex char %q", hh, c)
		}
	}
}

func TestRunJobRejectedUnknownProject(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "ghost",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         []string{"go", "version"},
		},
	})
	// A tool error surfaces as IsError=true (not a transport error).
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown project, got success: %+v", res.StructuredContent)
	}
}

func TestTailLogTool(t *testing.T) {
	session, jobs := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self",
			"agent":       "exec",
			"runner":      "local",
			"cmd":         []string{"go", "version"},
			"cwd":         ".",
			"timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if _, ok := jobs.Wait(created.ID); !ok {
		t.Fatalf("Wait: job not found")
	}

	logRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_tail_log",
		Arguments: map[string]any{"id": created.ID, "stream": "stdout"},
	})
	if err != nil {
		t.Fatalf("tail_log: %v", err)
	}
	var out tailLogOutput
	structured(t, logRes, &out)
	if out.Text == "" {
		t.Fatalf("expected non-empty stdout tail")
	}
}

func TestTailLogInvalidStream(t *testing.T) {
	session, jobs := connect(t)
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	var created jobView
	structured(t, res, &created)
	jobs.Wait(created.ID)

	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_tail_log",
		Arguments: map[string]any{"id": created.ID, "stream": "bogus"},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !bad.IsError {
		t.Fatalf("expected IsError for invalid stream")
	}
}

func TestCancelJobTool(t *testing.T) {
	session, jobs := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"sleep", "5"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)

	// Wait until the job is actually running before cancelling.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := jobs.Get(created.ID); ok && r.Status == job.StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancelRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_cancel_job",
		Arguments: map[string]any{"id": created.ID},
	})
	if err != nil {
		t.Fatalf("cancel_job: %v", err)
	}
	var cancelled jobView
	structured(t, cancelRes, &cancelled)

	final, _ := jobs.Wait(created.ID)
	if final.Status != job.StatusCancelled {
		t.Fatalf("expected cancelled, got %s", final.Status)
	}
}

func TestCancelUnknownJobTool(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_cancel_job",
		Arguments: map[string]any{"id": "does-not-exist"},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown job id")
	}
}

// startRunningJob submits a long-lived exec job over the tool and polls (via the
// service handle) until it reports running, so interactions can be raised while
// the job is genuinely live. It cancels the job on cleanup.
func startRunningJob(t *testing.T, session *mcp.ClientSession, jobs *job.Service) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"sleep", "30"}, "cwd": ".", "timeout_sec": 60,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.ID == "" {
		t.Fatalf("run_job returned no id: %+v", created)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := jobs.Get(created.ID); ok &&
			(r.Status == job.StatusRunning || r.Status == job.StatusPendingInteraction) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = jobs.Cancel(created.ID) })
	return created.ID
}

// TestInteractionToolsRoundTrip drives the two new interaction tools end to end:
// a pending interaction is raised on a live job via the service (MCP has no
// create tool), then read with gofer_get_interactions, answered with
// gofer_answer_interaction, and re-read to confirm the answered state.
func TestInteractionToolsRoundTrip(t *testing.T) {
	session, jobs := connect(t)
	jobID := startRunningJob(t, session, jobs)

	created, err := jobs.CreateInteraction(jobID, job.InteractionInput{
		Type:   job.InteractionTypeQuestion,
		Prompt: "continue?",
	})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}
	if created.Status != job.InteractionPending {
		t.Fatalf("created interaction status=%s, want pending", created.Status)
	}

	// gofer_get_interactions must surface the pending interaction.
	getRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_interactions",
		Arguments: map[string]any{"id": jobID},
	})
	if err != nil {
		t.Fatalf("get_interactions: %v", err)
	}
	var got getInteractionsOutput
	structured(t, getRes, &got)
	if len(got.Interactions) != 1 || got.Interactions[0].ID != created.ID {
		t.Fatalf("unexpected interactions: %+v", got.Interactions)
	}
	if got.Interactions[0].Status != job.InteractionPending || got.Interactions[0].JobID != jobID {
		t.Fatalf("expected pending interaction for job %s: %+v", jobID, got.Interactions[0])
	}

	// gofer_answer_interaction answers it and returns the answered view.
	ansRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_answer_interaction",
		Arguments: map[string]any{
			"id": jobID, "interaction_id": created.ID, "answer": "yes-go",
		},
	})
	if err != nil {
		t.Fatalf("answer_interaction: %v", err)
	}
	var answered interactionView
	structured(t, ansRes, &answered)
	if answered.Status != job.InteractionAnswered || answered.Answer != "yes-go" {
		t.Fatalf("unexpected answered interaction: %+v", answered)
	}

	// Re-read confirms the answered state persisted.
	getRes2, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_interactions",
		Arguments: map[string]any{"id": jobID},
	})
	if err != nil {
		t.Fatalf("get_interactions(2): %v", err)
	}
	var got2 getInteractionsOutput
	structured(t, getRes2, &got2)
	if len(got2.Interactions) != 1 || got2.Interactions[0].Status != job.InteractionAnswered {
		t.Fatalf("expected answered after re-read, got %+v", got2.Interactions)
	}
	if got2.Interactions[0].Answer != "yes-go" {
		t.Fatalf("expected answer 'yes-go', got %+v", got2.Interactions[0])
	}
}

// TestGetInteractionsEmptyIsArray asserts an unknown job yields a non-nil empty
// array (Out.interactions == []), not null.
func TestGetInteractionsEmptyIsArray(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_interactions",
		Arguments: map[string]any{"id": "does-not-exist"},
	})
	if err != nil {
		t.Fatalf("get_interactions: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error for unknown job: %+v", res.Content)
	}
	var out getInteractionsOutput
	structured(t, res, &out)
	if out.Interactions == nil || len(out.Interactions) != 0 {
		t.Fatalf("expected empty non-nil interactions array, got %v", out.Interactions)
	}
}

// TestAnswerInteractionSchemaSnakeCase asserts the SDK-derived input schema for
// gofer_answer_interaction uses snake_case property names (incl. interaction_id).
func TestAnswerInteractionSchemaSnakeCase(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var tool *mcp.Tool
	for _, tl := range res.Tools {
		if tl.Name == "gofer_answer_interaction" {
			tool = tl
			break
		}
	}
	if tool == nil {
		t.Fatalf("gofer_answer_interaction not found")
	}
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("unmarshal input schema: %v", err)
	}
	for _, key := range []string{"id", "interaction_id", "answer"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("input schema missing snake_case property %q; properties=%v", key, schema.Properties)
		}
	}
}

// TestAnswerInteractionUnknownJobToolError asserts answering on an unknown job
// surfaces a tool error (the service error is returned directly).
func TestAnswerInteractionUnknownJobToolError(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_answer_interaction",
		Arguments: map[string]any{
			"id": "ghost", "interaction_id": "x", "answer": "a",
		},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown job")
	}
}

// runDoneJob submits a trivial exec job over the tool and waits for terminal
// state via the service, returning its id (for the empty-result/empty-artifact
// cases).
func runDoneJob(t *testing.T, session *mcp.ClientSession, jobs *job.Service) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.ID == "" {
		t.Fatalf("run_job returned no id")
	}
	if _, ok := jobs.Wait(created.ID); !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}
	return created.ID
}

// startSleepJob submits a brief sleep job over the tool and returns its created
// snapshot (id + result_dir) WITHOUT waiting — so the test can drop
// artifacts/result.json into the result dir before captureOutcomes runs at
// finish (mirrors httpapi.createSleepJob).
func startSleepJob(t *testing.T, session *mcp.ClientSession) jobView {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"sleep", "0.4"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.ID == "" || created.ResultDir == "" {
		t.Fatalf("created job missing id/result_dir: %+v", created)
	}
	return created
}

// TestGetArtifactsTool covers a job that captured artifacts at finish: both
// files are listed with slash-relative names. Files are dropped while the job
// is still running so captureOutcomes scans them into ArtifactsJSON.
func TestGetArtifactsTool(t *testing.T) {
	session, jobs := connect(t)
	created := startSleepJob(t, session)

	artDir := filepath.Join(created.ResultDir, "artifacts", "sub")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(created.ResultDir, "artifacts", "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "b.bin"), []byte("abcdefgh"), 0o600); err != nil {
		t.Fatalf("write b.bin: %v", err)
	}

	final, ok := jobs.Wait(created.ID)
	if !ok || final.Status != job.StatusDone {
		t.Fatalf("job not done: %+v", final)
	}
	// The captured manifest must be populated (not just the live-scan fallback).
	if final.ArtifactsJSON == "" {
		t.Fatalf("ArtifactsJSON should be captured at finish")
	}

	out := callGetArtifacts(t, session, created.ID)
	if len(out.Artifacts) != 2 {
		t.Fatalf("expected 2 artifacts, got %d: %+v", len(out.Artifacts), out.Artifacts)
	}
	sizes := map[string]int64{}
	for _, a := range out.Artifacts {
		sizes[a.Name] = a.Size
	}
	if sizes["a.txt"] != 5 || sizes["sub/b.bin"] != 8 {
		t.Fatalf("unexpected artifacts: %+v", out.Artifacts)
	}
}

// TestGetArtifactsEmptyIsArray asserts a job with no artifacts yields a non-nil
// empty array.
func TestGetArtifactsEmptyIsArray(t *testing.T) {
	session, jobs := connect(t)
	id := runDoneJob(t, session, jobs)
	out := callGetArtifacts(t, session, id)
	if out.Artifacts == nil || len(out.Artifacts) != 0 {
		t.Fatalf("expected empty non-nil artifacts, got %+v", out.Artifacts)
	}
}

func TestGetArtifactsUnknownJob(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_artifacts",
		Arguments: map[string]any{"id": "ghost"},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown job")
	}
}

// callGetArtifacts invokes gofer_get_artifacts and decodes the output.
func callGetArtifacts(t *testing.T, session *mcp.ClientSession, id string) getArtifactsOutput {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_artifacts",
		Arguments: map[string]any{"id": id},
	})
	if err != nil {
		t.Fatalf("get_artifacts: %v", err)
	}
	var out getArtifactsOutput
	structured(t, res, &out)
	return out
}

// TestGetResultTool covers a job that has a result.json captured at finish: the
// tool returns the JSON verbatim. result.json is dropped while the job is still
// running so captureOutcomes inlines it into ResultJSON.
func TestGetResultTool(t *testing.T) {
	session, jobs := connect(t)
	created := startSleepJob(t, session)

	want := `{"ok":true,"n":3}`
	if err := os.WriteFile(filepath.Join(created.ResultDir, "result.json"), []byte(want), 0o600); err != nil {
		t.Fatalf("write result.json: %v", err)
	}

	final, ok := jobs.Wait(created.ID)
	if !ok || final.Status != job.StatusDone {
		t.Fatalf("job not done: %+v", final)
	}

	got := callGetResult(t, session, created.ID)
	if got.ResultJSON != want {
		t.Fatalf("result_json=%q, want %q", got.ResultJSON, want)
	}
}

// TestGetResultEmpty asserts a job with no result.json yields an empty string.
func TestGetResultEmpty(t *testing.T) {
	session, jobs := connect(t)
	id := runDoneJob(t, session, jobs)
	got := callGetResult(t, session, id)
	if got.ResultJSON != "" {
		t.Fatalf("expected empty result_json, got %q", got.ResultJSON)
	}
}

func TestGetResultUnknownJob(t *testing.T) {
	session, _ := connect(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_result",
		Arguments: map[string]any{"id": "ghost"},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown job")
	}
}

// callGetResult invokes gofer_get_result and decodes the output.
func callGetResult(t *testing.T, session *mcp.ClientSession, id string) getResultOutput {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_get_result",
		Arguments: map[string]any{"id": id},
	})
	if err != nil {
		t.Fatalf("get_result: %v", err)
	}
	var out getResultOutput
	structured(t, res, &out)
	return out
}
