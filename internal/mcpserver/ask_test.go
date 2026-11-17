package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

func toolNamesOf(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(tools.Tools))
	for _, tl := range tools.Tools {
		got = append(got, tl.Name)
	}
	sort.Strings(got)
	return got
}

func hasTool(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// A resident ACP session's gofer MCP (GOFER_ASK) exposes gofer_ask and its own outcome
// report, nothing else.
func TestGoferAskSurfaceIsOnlyGoferAsk(t *testing.T) {
	t.Setenv(envGoferAsk, "1")
	jobs, projects, agents := testCore(t)
	got := toolNamesOf(t, connectTo(t, NewLocal(jobs, projects, agents)))
	if strings.Join(got, ",") != "gofer_ask,gofer_submit_result" {
		t.Fatalf("GOFER_ASK surface = %v, want exactly [gofer_ask gofer_submit_result]", got)
	}
}

// The resident mailbox session keeps its full surface and gains gofer_ask; the ordinary
// operator surface and a leader round (unattended) do not carry it.
func TestGoferAskOnMailboxFullSurfaceNotOnOperatorOrLeader(t *testing.T) {
	jobs, projects, agents := testCore(t)

	t.Setenv(envMailResident, "1")
	mail := toolNamesOf(t, connectTo(t, NewLocal(jobs, projects, agents)))
	if !hasTool(mail, "gofer_ask") || !hasTool(mail, "gofer_run_job") {
		t.Fatalf("mailbox surface must be the full one plus gofer_ask: %v", mail)
	}

	t.Setenv(envMailResident, "")
	if op := toolNamesOf(t, connectTo(t, NewLocal(jobs, projects, agents))); hasTool(op, "gofer_ask") {
		t.Fatalf("the operator surface must not carry gofer_ask: %v", op)
	}

	t.Setenv(envLeaderPlan, "plan-1")
	if ld := toolNamesOf(t, connectTo(t, NewLocal(jobs, projects, agents))); hasTool(ld, "gofer_ask") {
		t.Fatalf("a leader round is unattended and must not carry gofer_ask: %v", ld)
	}
}

// askSession wires a gofer_ask MCP the way a resident session's child runs it: GOFER_ASK +
// the job id, talking to the hub over HTTP with the job's own member credential.
func askSession(t *testing.T) (session *mcp.ClientSession, jobs *job.Service, jobID string) {
	t.Helper()
	jobs, projects, agents := testCore(t)
	res, err := jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "sleep", "30s"), Cwd: ".", TimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Cleanup(func() { _ = jobs.Cancel(res.ID); jobs.Wait(res.ID) })
	wait.Until(t, 10*time.Second, "job running", func() bool {
		r, ok := jobs.Get(res.ID)
		return ok && r.Status == job.StatusRunning
	})
	tok := "gjt_" + res.ID + "_00112233445566778899aabbccddeeff"
	sum := sha256.Sum256([]byte(tok))
	if err := jobs.Meta().UpsertJobToken(jobstore.JobTokenRecord{JobID: res.ID, TokenHash: hex.EncodeToString(sum[:]),
		Kind: jobstore.JobCredentialMember, ExpiresAt: time.Now().Unix() + 3600, CreatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	hs := httpapi.New(&config.ServerConfig{Token: "op-secret"}, "op-secret", false, jobs, nil, projects, agents, nil, nil, nil, nil)
	srv := httptest.NewServer(hs.Handler())
	t.Cleanup(srv.Close)
	t.Setenv(envGoferAsk, "1")
	t.Setenv(envJobID, res.ID)
	old := goferAskPollInterval
	goferAskPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { goferAskPollInterval = old })
	return connectTo(t, newServer(NewClientBackend(client.New(srv.URL, tok)), "")), jobs, res.ID
}

type askCall struct {
	out goferAskOutput
	err error
	isE bool
}

func callAsk(t *testing.T, session *mcp.ClientSession, args map[string]any) <-chan askCall {
	t.Helper()
	ch := make(chan askCall, 1)
	go func() {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_ask", Arguments: args})
		if err != nil {
			ch <- askCall{err: err}
			return
		}
		var out goferAskOutput
		if !res.IsError {
			structured(t, res, &out)
		}
		ch <- askCall{out: out, isE: res.IsError}
	}()
	return ch
}

func pendingInteraction(t *testing.T, jobs *job.Service, jobID string) job.Interaction {
	t.Helper()
	var got job.Interaction
	wait.Until(t, 10*time.Second, "a pending interaction", func() bool {
		list, _ := jobs.GetInteractions(jobID)
		for _, it := range list {
			if it.Status == job.InteractionPending {
				got = it
				return true
			}
		}
		return false
	})
	return got
}

func awaitAsk(t *testing.T, ch <-chan askCall) askCall {
	t.Helper()
	select {
	case c := <-ch:
		if c.err != nil {
			t.Fatalf("gofer_ask transport: %v", c.err)
		}
		return c
	case <-time.After(wait.Timeout(t, 15*time.Second)):
		t.Fatal("gofer_ask did not return")
		return askCall{}
	}
}

func TestGoferAskSingleChoiceAnswered(t *testing.T) {
	session, jobs, jobID := askSession(t)
	ch := callAsk(t, session, map[string]any{
		"question": "which db?", "recommended": "pg",
		"options": []map[string]any{{"label": "pg", "description": "postgres"}, {"label": "sqlite"}},
	})
	it := pendingInteraction(t, jobs, jobID)
	if it.Type != job.InteractionTypeChoice || it.Recommended != "pg" || len(it.Options) != 2 || it.Options[0].Description != "postgres" {
		t.Fatalf("raised interaction = %+v", it)
	}
	if _, err := jobs.AnswerInteractionByHuman(jobID, it.ID, "sqlite", "alice"); err != nil {
		t.Fatal(err)
	}
	c := awaitAsk(t, ch)
	if c.out.State != "answered" || c.out.Answer != "sqlite" || c.out.AnsweredBy != "alice" {
		t.Fatalf("output = %+v, want answered/sqlite", c.out)
	}
}

func TestGoferAskMultiChoiceAnswered(t *testing.T) {
	session, jobs, jobID := askSession(t)
	ch := callAsk(t, session, map[string]any{
		"question": "which checks?", "multi": true,
		"options": []map[string]any{{"label": "lint"}, {"label": "vet"}, {"label": "race"}},
	})
	it := pendingInteraction(t, jobs, jobID)
	if !it.Multi {
		t.Fatalf("interaction must be multi: %+v", it)
	}
	if _, err := jobs.AnswerInteractionByHuman(jobID, it.ID, `["lint","race"]`, ""); err != nil {
		t.Fatal(err)
	}
	c := awaitAsk(t, ch)
	if c.out.State != "answered" || strings.Join(c.out.Answers, ",") != "lint,race" {
		t.Fatalf("output = %+v, want answered with answers [lint race]", c.out)
	}
}

func TestGoferAskOtherTextAndFreeText(t *testing.T) {
	session, jobs, jobID := askSession(t)
	ch := callAsk(t, session, map[string]any{
		"question": "which db?", "allow_other": true,
		"options": []map[string]any{{"label": "pg"}},
	})
	it := pendingInteraction(t, jobs, jobID)
	if _, err := jobs.AnswerInteractionByHuman(jobID, it.ID, "duckdb", ""); err != nil {
		t.Fatalf("allow_other must accept free text: %v", err)
	}
	if c := awaitAsk(t, ch); c.out.State != "answered" || c.out.Answer != "duckdb" {
		t.Fatalf("output = %+v, want answered/duckdb", c.out)
	}

	ch = callAsk(t, session, map[string]any{"question": "what should the file be called?"})
	it = pendingInteraction(t, jobs, jobID)
	if it.Type != job.InteractionTypeQuestion || len(it.Options) != 0 {
		t.Fatalf("a free-text question must have no options: %+v", it)
	}
	if _, err := jobs.AnswerInteractionByHuman(jobID, it.ID, "notes.md", ""); err != nil {
		t.Fatal(err)
	}
	if c := awaitAsk(t, ch); c.out.State != "answered" || c.out.Answer != "notes.md" {
		t.Fatalf("output = %+v, want answered/notes.md", c.out)
	}
}

// Nobody answers within timeout_sec: the server cancels the interaction at the deadline and
// the tool reports expired together with the recommended option to fall back to.
func TestGoferAskExpires(t *testing.T) {
	session, jobs, jobID := askSession(t)
	ch := callAsk(t, session, map[string]any{
		"question": "ok to proceed?", "timeout_sec": 1, "recommended": "yes",
		"options": []map[string]any{{"label": "yes"}, {"label": "no"}},
	})
	c := awaitAsk(t, ch)
	if c.out.State != "expired" || c.out.Recommended != "yes" || c.out.Answer != "" {
		t.Fatalf("output = %+v, want expired with the recommended option", c.out)
	}
	list, _ := jobs.GetInteractions(jobID)
	if len(list) != 1 || list[0].Status != job.InteractionCancelled {
		t.Fatalf("the expired interaction must be cancelled, not left pending: %+v", list)
	}
	if r, _ := jobs.Get(jobID); r.Status != job.StatusRunning {
		t.Fatalf("job status after expiry = %s, want running", r.Status)
	}
}

// stubInteractions serves a fixed interaction list (the rest of Backend is nil).
type stubInteractions struct {
	Backend
	list []job.Interaction
}

func (s stubInteractions) GetInteractions(string) ([]job.Interaction, error) { return s.list, nil }

// A question cancelled before its deadline was withdrawn (not expired); one cancelled at the
// deadline is an expiry. (Through HTTP a job that ends also revokes the credential, so the
// tool then fails with the transport error instead — the agent is gone either way.)
func TestGoferAskCancelledVersusExpired(t *testing.T) {
	old := goferAskPollInterval
	goferAskPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { goferAskPollInterval = old })
	it := job.Interaction{ID: "i1", Status: job.InteractionCancelled}
	b := stubInteractions{list: []job.Interaction{it}}

	out, err := waitAskAnswer(context.Background(), b, "j1", it, time.Now().Add(time.Hour), false)
	if err != nil || out.State != "cancelled" {
		t.Fatalf("cancelled before the deadline = %+v, %v, want cancelled", out, err)
	}
	out, err = waitAskAnswer(context.Background(), b, "j1", it, time.Now(), false)
	if err != nil || out.State != "expired" {
		t.Fatalf("cancelled at the deadline = %+v, %v, want expired", out, err)
	}
}

func TestGoferAskRejectsBadInput(t *testing.T) {
	session, _, _ := askSession(t)
	for name, args := range map[string]map[string]any{
		"empty question":        {"question": " "},
		"multi without options": {"question": "q", "multi": true},
		"unknown recommended":   {"question": "q", "recommended": "x", "options": []map[string]any{{"label": "a"}}},
		"duplicate label":       {"question": "q", "options": []map[string]any{{"label": "a"}, {"label": "a"}}},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_ask", Arguments: args})
		if err != nil {
			t.Fatalf("%s: transport %v", name, err)
		}
		if !res.IsError {
			t.Errorf("%s: want a tool error, got %+v", name, res.StructuredContent)
		}
	}
}

// gofer_run_job carries permission_tier through to the submitted request.
func TestRunJobInputCarriesPermissionTier(t *testing.T) {
	jobs, projects, agents := testCore(t)
	session := connectTo(t, newServer(newLocalBackend(jobs, projects, agents), ""))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": testcmd.Cmd(t, "sleep", "1s"), "cwd": ".", "permission_tier": "ask",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The exec agent is no acp-agent, so the server's admission names the field it refused:
	// proof the tier reached the request.
	if !res.IsError || !strings.Contains(fmt.Sprint(res.Content[0].(*mcp.TextContent).Text), "permission_tier") {
		t.Fatalf("result = %+v, want an admission error naming permission_tier", res)
	}
}
