package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// InHandler 反向用例与调用断言（设计「架构」的「测试兜底」2 与 3，计划 T1.5a）。
//
// 每条 InHandler 路由在 jobAuthzCases 里登记一个用例，用例把一个请求跑两遍：
//
//   - 「他人的资源」：调用方 alice 既没有声明的权限，也不是资源的 owner —— 期望 403
//     （deny），或者 200 但结果被收窄到自己的资源（filter）；
//   - 「自己的资源」：alice 操作自己的 job —— 期望不被拒（自己的资源不需要权限）；
//
// 两遍都断言「调用断言」：s.can 记下的判定不能超出声明（handler 不得自己发明权限），
// 而且两遍合起来的判定集合必须**等于**声明里的权限集合（声明的每个权限都被判定过）。
//
// 权限场最基础的「alice 什么也没有」也由这两个配置表达：本次测试的部署没有根管理员
// （没有一个调用方带 can_admin）、四个开关全打开（配置推出的 `users` 授权因此为空），
// 再把数据库的 `users` 授权换成用例自己写的两套（空 / 声明的权限）—— 这正是解析器读的那
// 一份数据（authzGrants），所以判定走的完全是生产路径。

// authzCaseEnv is the resource set one case runs against: the caller under test, the caller
// that OWNS the resource, and the resource itself.
type authzCaseEnv struct {
	t      *testing.T
	s      *Server
	caller string
	owner  string
	job    job.JobResult

	wakeupID   string
	retryID    string
	workflowID string
	// pendingID is the pending interaction of a LIVE job owned by owner (the filter case of
	// GET /v1/interactions); relayDecisionID / plainDecisionID are the two decisions a
	// decision-answer case drives.
	pendingID       string
	relayDecisionID string
	plainDecisionID string
}

// caseReq is one request of a case.
type caseReq struct {
	method string
	path   string
	body   any
}

// authzCase is one registered reverse case.
type authzCase struct {
	// key is the canonical route key of the declaration this case belongs to.
	key string
	// req builds the request.
	req func(e *authzCaseEnv) caseReq
	// reqOwn builds the request of the own half when it differs from req. It exists for the
	// routes whose two halves stand for two PERMISSION branches rather than for two resources:
	// POST /v1/decisions/{id}/answer answers a relay turn (session.reply.others) and a plain
	// decision (interaction.answer) through one route, so the two halves must drive a row of
	// each kind for the union assertion below to see both permissions decided. nil = req.
	reqOwn func(e *authzCaseEnv) caseReq
	// wantOthers asserts the answer of the request against SOMEONE ELSE's resource. nil
	// means the default: a 403 that names a declared permission.
	wantOthers func(t *testing.T, resp *http.Response, e *authzCaseEnv)
	// wantOwn asserts the answer of the request against the caller's OWN resource. nil
	// means the default: anything but a 403 (the resource rule must not refuse the owner).
	wantOwn func(t *testing.T, resp *http.Response, e *authzCaseEnv)
	// skipOwn leaves out the own-resource half: the route's own-resource answer is a live
	// SSE stream (it stays open until the job ends), and the permission it decides is the
	// same one the denied half decides — the union check below still sees it.
	skipOwn bool
}

// jobAuthzCases is the reverse-case registry: one entry per InHandler route of the job
// domain (see jobDomainInHandler, which the completeness test derives from the declaration
// table — a job route without a case fails the test).
var jobAuthzCases = []authzCase{
	// reads
	{key: "GET /v1/jobs", req: func(*authzCaseEnv) caseReq { return caseReq{http.MethodGet, "/v1/jobs", nil} },
		wantOthers: filteredList("jobs")},
	{key: "GET /v1/jobs/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID, nil}
	}},
	{key: "GET /v1/jobs/{id}/request", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/request", nil}
	}},
	{key: "GET /v1/jobs/{id}/logs/stdout", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/logs/stdout", nil}
	}},
	{key: "GET /v1/jobs/{id}/logs/stderr", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/logs/stderr", nil}
	}},
	{key: "GET /v1/jobs/{id}/stream", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/stream", nil}
	}, skipOwn: true},
	{key: "GET /v1/jobs/{id}/acp/stream", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/acp/stream", nil}
	}, skipOwn: true},
	{key: "GET /v1/jobs/{id}/events", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/events", nil}
	}},
	{key: "GET /v1/jobs/{id}/deliveries", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/deliveries", nil}
	}},
	{key: "GET /v1/jobs/{id}/comments", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/comments", nil}
	}},
	{key: "GET /v1/jobs/{id}/artifacts", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/artifacts", nil}
	}},
	{key: "GET /v1/jobs/{id}/artifacts/{name}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/artifacts/x.bin", nil}
	}},
	{key: "GET /v1/jobs/{id}/diff", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/diff", nil}
	}},
	{key: "GET /v1/jobs/{id}/worktree", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/worktree", nil}
	}},
	{key: "GET /v1/jobs/{id}/wakeups", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/wakeups", nil}
	}},
	{key: "GET /v1/jobs/{id}/retries", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/retries", nil}
	}},
	{key: "GET /v1/wakeups/{wid}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/wakeups/" + e.wakeup(), nil}
	}},
	{key: "GET /v1/workflows", req: func(*authzCaseEnv) caseReq { return caseReq{http.MethodGet, "/v1/workflows", nil} },
		wantOthers: filteredList("workflows")},
	{key: "GET /v1/workflows/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/workflows/" + e.workflow(), nil}
	}},
	{key: "GET /v1/workflows/{id}/events", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/workflows/" + e.workflow() + "/events", nil}
	}},
	{key: "GET /v1/workflows/{id}/export", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/workflows/" + e.workflow() + "/export", nil}
	}},

	// attach (the pty recording / sessions and the attach ticket)
	{key: "GET /v1/jobs/{id}/pty/recording", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/pty/recording", nil}
	}},
	{key: "GET /v1/jobs/{id}/pty/sessions", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/pty/sessions", nil}
	}},
	{key: "GET /v1/pty/sessions", req: func(*authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/pty/sessions", nil}
	}, wantOthers: filteredList("sessions")},
	{key: "POST /v1/jobs/{id}/attach-ticket", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/attach-ticket", nil}
	}},

	// cancel / delete
	{key: "POST /v1/jobs/{id}/cancel", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/cancel", nil}
	}},
	{key: "POST /v1/workflows/{id}/cancel", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/workflows/" + e.workflow() + "/cancel", nil}
	}},
	{key: "DELETE /v1/retries/{rid}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/retries/" + e.retry(), nil}
	}},
	{key: "DELETE /v1/jobs/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/jobs/" + e.job.ID, nil}
	}},
	{key: "POST /v1/jobs/{id}/redact", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/redact", jobRedactRequest{Literals: []string{"x"}}}
	}},

	// manage (+ job.submit on the entries that always start a job)
	{key: "PATCH /v1/jobs/{id}", req: func(e *authzCaseEnv) caseReq {
		title := "t"
		return caseReq{http.MethodPatch, "/v1/jobs/" + e.job.ID, patchJobBody{Title: &title}}
	}},
	{key: "POST /v1/jobs/{id}/say", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/say", map[string]string{"message": "hi"}}
	}},
	{key: "POST /v1/jobs/{id}/end", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/end", nil}
	}},
	{key: "POST /v1/jobs/{id}/deliver", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/deliver", map[string]any{}}
	}},
	{key: "DELETE /v1/jobs/{id}/worktree", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/jobs/" + e.job.ID + "/worktree", nil}
	}},
	{key: "POST /v1/jobs/{id}/worktree/merge", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/worktree/merge", map[string]any{}}
	}},
	{key: "POST /v1/jobs/{id}/resume", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/resume", map[string]string{"prompt": "again"}}
	}},
	{key: "POST /v1/jobs/{id}/rebuild", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/rebuild", map[string]any{}}
	}},
	{key: "POST /v1/workflows/{id}/pick", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/workflows/" + e.workflow() + "/pick", map[string]int{"step": 1, "fan": 0}}
	}},

	// wakeups
	{key: "POST /v1/jobs/{id}/wakeups", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/wakeups", job.WakeupSpec{
			Kind: jobstore.WakeupKindAt, At: time.Now().Add(time.Hour).Unix(),
		}}
	}},
	{key: "PATCH /v1/wakeups/{wid}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPatch, "/v1/wakeups/" + e.wakeup(), map[string]bool{"enabled": false}}
	}},
	{key: "DELETE /v1/wakeups/{wid}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/wakeups/" + e.wakeup(), nil}
	}},

	// the filters outside the declarative gate
	{key: "POST /v1/jobs/secret-scan", req: func(*authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/secret-scan", jobSecretScanRequest{Literals: []string{"zzz-not-a-secret"}}}
	}, wantOthers: wantOK},

	// interactions (plan T1.5b): injecting one into someone else's job is a manage action,
	// reading someone else's job's interactions a read one. The two carriers of
	// interaction.answer are declared Require and are covered by the route gate
	// (TestAuthzGateCategories), not by a reverse case.
	{key: "POST /v1/jobs/{id}/interactions", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/interactions",
			createInteractionReq{Type: job.InteractionTypeQuestion, Prompt: "continue?"}}
	}},
	{key: "GET /v1/jobs/{id}/interactions", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/jobs/" + e.job.ID + "/interactions", nil}
	}},
	// The cross-job pending list is the FILTER form: without job.read.others the answer is
	// narrowed to the caller's OWN jobs instead of being refused.
	{key: "GET /v1/interactions",
		req: func(e *authzCaseEnv) caseReq {
			e.pendingOwnerInteraction()
			return caseReq{http.MethodGet, "/v1/interactions?status=pending", nil}
		},
		wantOthers: func(t *testing.T, resp *http.Response, e *authzCaseEnv) {
			t.Helper()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("the filtered pending list answered %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
			}
			if body := readBody(t, resp); strings.Contains(body, e.pendingID) {
				t.Fatalf("the filter leaked another caller's pending interaction %s: %s", e.pendingID, body)
			}
		},
		wantOwn: func(t *testing.T, resp *http.Response, e *authzCaseEnv) {
			t.Helper()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("the caller's own pending list answered %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
			}
			if body := readBody(t, resp); !strings.Contains(body, e.pendingID) {
				t.Fatalf("the caller's own pending interaction %s is missing: %s", e.pendingID, body)
			}
		}},
}

// interactionAuthzCases is the reverse-case registry of plan T1.5b's InHandler routes OUTSIDE
// the job domain: answering a decision, whose permission depends on the row's KIND.
var interactionAuthzCases = []authzCase{
	{
		key: "POST /v1/decisions/{id}/answer",
		// The two halves stand for the route's two permission branches, not for two resources:
		// answering a decision carries no ownership at all (any holder of interaction.answer
		// answers any decision). The "someone else's resource" half is a RELAY turn — speech
		// into a terminal session owned by bob — so alice needs session.reply.others and is
		// refused; the own half is a plain decision, which only needs interaction.answer and is
		// accepted. Together they decide both declared permissions (the union assertion below).
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/decisions/" + e.relayDecision() + "/answer",
				answerDecisionReq{Answer: "plan B"}}
		},
		reqOwn: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/decisions/" + e.plainDecision() + "/answer",
				answerDecisionReq{Answer: "plan B"}}
		},
	},
}

// inHandlerDomain pairs a reverse-case registry with the routes it must cover. The routes are
// DERIVED from the declaration table (see the two derivations), so a new route of one of these
// shapes cannot be declared without a reverse case.
type inHandlerDomain struct {
	name    string
	derived func() []string
	cases   []authzCase
}

var inHandlerDomains = []inHandlerDomain{
	{name: "the job domain", derived: jobDomainInHandler, cases: jobAuthzCases},
	{name: "interactions and decisions", derived: interactionDomainInHandler, cases: interactionAuthzCases},
}

// pendingOwnerInteraction seeds a RUNNING job owned by e.owner carrying one pending
// interaction, and returns the interaction id. The cross-job pending list only carries
// interactions of non-terminal jobs (jobstore JOINs jobs), so the filter case of
// GET /v1/interactions cannot use the finished case job.
func (e *authzCaseEnv) pendingOwnerInteraction() string {
	if e.pendingID != "" {
		return e.pendingID
	}
	jobID, itID := "job-live-"+e.owner, "int-live-"+e.owner
	now := time.Now().Unix()
	if err := e.s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: jobID, ProjectKey: "self", Agent: "exec", Runner: "worker",
		Status: job.StatusRunning, Cwd: ".", StartedAt: now, UpdatedAt: now, CallerID: e.owner,
	}); err != nil {
		e.t.Fatalf("upsert live job for %s: %v", e.owner, err)
	}
	if err := e.s.jobs.Meta().UpsertInteraction(jobstore.InteractionRecord{
		ID: itID, JobID: jobID, Type: job.InteractionTypeQuestion, Prompt: "continue?",
		Status: job.InteractionPending, CreatedAt: now,
	}); err != nil {
		e.t.Fatalf("upsert pending interaction for %s: %v", e.owner, err)
	}
	e.pendingID = itID
	return itID
}

// relayDecision seeds an OPEN relay turn — a decision of kind relay belonging to a terminal
// session owned by e.owner — and returns its id: answering it is 向会话说话, so it follows
// session.reply.others of that session.
func (e *authzCaseEnv) relayDecision() string {
	if e.relayDecisionID != "" {
		return e.relayDecisionID
	}
	sid := "sess-live-" + e.owner
	if _, err := e.s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: sid, Agent: "claude", CallerID: e.owner,
	}); err != nil {
		e.t.Fatalf("upsert session for %s: %v", e.owner, err)
	}
	d := jobstore.PlanDecision{
		Title: "turn", Question: "continue?",
		Kind: jobstore.DecisionKindRelay, SessionID: sid,
	}
	if err := e.s.jobs.Meta().InsertDecision(&d); err != nil {
		e.t.Fatalf("insert relay decision of %s: %v", e.owner, err)
	}
	e.relayDecisionID = d.ID
	return d.ID
}

// plainDecision seeds an OPEN gofer_ask_human decision (no session, no kind) and returns its
// id: answering it is the interaction.answer branch of the same route.
func (e *authzCaseEnv) plainDecision() string {
	if e.plainDecisionID != "" {
		return e.plainDecisionID
	}
	d := jobstore.PlanDecision{Title: "pick", Question: "which?"}
	if err := e.s.jobs.Meta().InsertDecision(&d); err != nil {
		e.t.Fatalf("insert decision: %v", err)
	}
	e.plainDecisionID = d.ID
	return d.ID
}

// Wakeup / retry / workflow rows are created on demand, so the cases that need one pay for
// it and the rest do not.
func (e *authzCaseEnv) wakeup() string {
	if e.wakeupID == "" {
		w, err := e.s.jobs.CreateWakeup(job.WakeupSpec{
			JobID: e.job.ID, Kind: jobstore.WakeupKindAt,
			At: time.Now().Add(time.Hour).Unix(), Instruction: "test",
		}, e.owner, job.AllowAllWakeups)
		if err != nil {
			e.t.Fatalf("create wakeup for %s: %v", e.owner, err)
		}
		e.wakeupID = w.ID
	}
	return e.wakeupID
}

func (e *authzCaseEnv) retry() string {
	if e.retryID == "" {
		now := time.Now().Unix()
		id := "rt-" + e.owner + "-" + e.job.ID
		if err := e.s.jobs.Meta().InsertRetry(jobstore.RetryRecord{
			ID: id, SourceJobID: e.job.ID, Attempt: 1, Reason: "test", RequestJSON: "{}",
			NextRunAt: now + 3600, State: jobstore.RetryPending, CreatedAt: now,
		}); err != nil {
			e.t.Fatalf("insert retry for %s: %v", e.owner, err)
		}
		e.retryID = id
	}
	return e.retryID
}

func (e *authzCaseEnv) workflow() string {
	if e.workflowID == "" {
		id := "wf-" + e.owner + "-" + e.job.ID
		now := time.Now().Unix()
		if err := e.s.jobs.Meta().InsertWorkflow(jobstore.Workflow{
			ID: id, Status: jobstore.WorkflowRunning, CallerID: e.owner,
			CurrentStep: 1, TotalSteps: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			e.t.Fatalf("insert workflow for %s: %v", e.owner, err)
		}
		e.workflowID = id
	}
	return e.workflowID
}

// filteredList asserts the FILTER answer: 200, and the other caller's resource is not in
// the body (or in the named JSON array of it).
func filteredList(field string) func(*testing.T, *http.Response, *authzCaseEnv) {
	return func(t *testing.T, resp *http.Response, e *authzCaseEnv) {
		t.Helper()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("filtered route answered %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
		}
		body := readBody(t, resp)
		if field != "" {
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &m); err != nil {
				t.Fatalf("filtered route body is not an object: %v (%s)", err, body)
			}
			body = string(m[field])
		}
		if strings.Contains(body, e.job.ID) || (e.workflowID != "" && strings.Contains(body, e.workflowID)) {
			t.Fatalf("the filter leaked another caller's resource: %s", body)
		}
	}
}

// wantOK asserts a plain 200 (the filter of a route whose answer carries no resource id).
func wantOK(t *testing.T, resp *http.Response, _ *authzCaseEnv) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}

// jobDomainInHandler derives the InHandler routes of plan T1.5a from the declaration table:
// the ones whose declared permissions are all job permissions (job.*, the attach pair and
// the job secret scan). Deriving them means a new job route cannot be declared without a
// reverse case — the completeness test fails.
func jobDomainInHandler() []string {
	var out []string
	for key, decl := range routeDecls {
		if decl.user.kind != userInHandler || len(decl.user.perms) == 0 {
			continue
		}
		allJob := true
		for _, p := range decl.user.perms {
			if !strings.HasPrefix(p, "job.") && p != authz.PermSecretScanAll {
				allJob = false
				break
			}
		}
		if allJob {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// interactionDomainInHandler derives plan T1.5b's InHandler routes that are NOT part of the
// job domain: every route whose decision is the `interaction.answer` permission (answering a
// decision, whose relay kind additionally follows session.reply.others). Like the job
// derivation it reads the declaration table, so such a route cannot be declared without a
// reverse case.
func interactionDomainInHandler() []string {
	var out []string
	for key, decl := range routeDecls {
		if decl.user.kind != userInHandler {
			continue
		}
		if slices.Contains(decl.user.perms, authz.PermInteractionAnswer) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// TestInHandlerCasesMatchDeclarations: every registered case names a declared InHandler
// route, and every InHandler route of a domain has a case (design 「没登记就失败」).
func TestInHandlerCasesMatchDeclarations(t *testing.T) {
	t.Parallel()
	registered := map[string]bool{}
	for _, domain := range inHandlerDomains {
		for _, tc := range domain.cases {
			if registered[tc.key] {
				t.Errorf("case %s is registered twice", tc.key)
			}
			registered[tc.key] = true
			decl, ok := routeDecls[tc.key]
			if !ok {
				t.Errorf("case %s has no route declaration", tc.key)
				continue
			}
			if decl.user.kind != userInHandler {
				t.Errorf("%s is declared as category %d, but a reverse case was registered for it", tc.key, decl.user.kind)
			}
		}
		for _, key := range domain.derived() {
			if !registered[key] {
				t.Errorf("InHandler route %s of %s has no reverse case", key, domain.name)
			}
		}
	}
}

// TestInHandlerReverseCases drives every case of every domain twice: against someone else's
// resource (the caller holds nothing) and against its own (the caller holds exactly the
// declared permissions). It asserts the answer, that no handler decided a permission its route
// does not declare, and that together the two requests decided every declared permission.
func TestInHandlerReverseCases(t *testing.T) {
	// Not parallel: the decision collector is a package-level seam (see installCaseRecorder).
	restore := installCaseRecorder()
	defer restore()

	// No root administrator and every config gate OPEN: the `users` subject derives no
	// permission at all, so the database grants (which each half of a case sets) are the
	// whole of the caller's authority.
	s := recordingServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	})
	if s.rootAdmin("alice") || s.rootAdmin("bob") {
		t.Fatal("the harness deployment must have no root administrator")
	}

	// One running index across the domains, so every half gets its own case job.
	caseIndex := 0

	for _, domain := range inHandlerDomains {
		for _, tc := range domain.cases {
			caseIndex++
			i := caseIndex
			t.Run(tc.key, func(t *testing.T) {
				decl := routeDecls[tc.key]

				// --- someone else's resource: alice holds nothing -------------------
				other := newAuthzCaseEnv(t, s, "alice", "bob", i)
				s.authzGrants.Store(&userGrants{grants: map[string]string{}})
				resp, decided := serveAuthzCase(t, s, "tok-alice", tc.req(other))
				if tc.wantOthers != nil {
					tc.wantOthers(t, resp, other)
				} else {
					if resp.StatusCode != http.StatusForbidden {
						t.Fatalf("someone else's resource answered %d, want 403 (body %s)",
							resp.StatusCode, readBody(t, resp))
					}
					assertNamesDeclaredPermission(t, readBody(t, resp), decl.user.perms)
				}
				assertDecidedOnly(t, decided, decl.user.perms, "someone else's resource")

				// --- its own resource: alice holds exactly the declared permissions --
				ownReq := tc.req
				if tc.reqOwn != nil {
					ownReq = tc.reqOwn
				}
				var ownDecided []decidedPerm
				if !tc.skipOwn {
					own := newAuthzCaseEnv(t, s, "alice", "alice", i)
					s.authzGrants.Store(grantsFor(decl.user.perms))
					var resp *http.Response
					resp, ownDecided = serveAuthzCase(t, s, "tok-alice", ownReq(own))
					if tc.wantOwn != nil {
						tc.wantOwn(t, resp, own)
					} else if resp.StatusCode == http.StatusForbidden {
						t.Fatalf("the caller's OWN resource was refused: %d (body %s)", resp.StatusCode, readBody(t, resp))
					}
					assertDecidedOnly(t, ownDecided, decl.user.perms, "own resource")
				}

				// The union of the two requests must be exactly the declaration: every declared
				// permission is decided by one of them (design 「每个声明的权限都被判定过」).
				union := map[string]bool{}
				for _, d := range append(append([]decidedPerm{}, decided...), ownDecided...) {
					union[d.perm] = true
				}
				for _, p := range decl.user.perms {
					if !union[p] {
						t.Errorf("declared permission %s was never decided by either request", p)
					}
				}
			})
		}
	}
}

// newAuthzCaseEnv creates the resource set of one case half: a FINISHED interactive job
// owned by owner (plus its pty session row, which the pty routes read). A terminal job is
// what the job routes are routinely asked about, and it keeps a live answer (a stream, a
// long poll) from outliving the test.
func newAuthzCaseEnv(t *testing.T, s *Server, caller, owner string, i int) *authzCaseEnv {
	t.Helper()
	id := fmt.Sprintf("job-%s-%d", owner, i)
	dir := t.TempDir()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "worker", Interactive: true,
		Status: job.StatusDone, Cwd: ".", ResultDir: dir, StartedAt: now, EndedAt: now,
		UpdatedAt: now, CallerID: owner,
	}); err != nil {
		t.Fatalf("upsert case job: %v", err)
	}
	cast := filepath.Join(dir, "pty.cast")
	if err := os.WriteFile(cast, []byte("cast"), 0o600); err != nil {
		t.Fatalf("write cast: %v", err)
	}
	addPtyRow(t, s, id, owner, cast, 2)
	res, ok := s.jobs.Get(id)
	if !ok {
		t.Fatalf("the case job %s is not resolvable", id)
	}
	return &authzCaseEnv{t: t, s: s, caller: caller, owner: owner, job: res}
}

// grantsFor is the database half of a decision that holds exactly perms.
func grantsFor(perms []string) *userGrants {
	g := &userGrants{grants: map[string]string{}}
	for _, p := range perms {
		g.grants[p] = "case:" + p
	}
	g.perms = append([]string(nil), perms...)
	return g
}

// serveAuthzCase sends one request through the real middleware chain and returns its
// response plus the decisions the request made.
func serveAuthzCase(t *testing.T, s *Server, token string, req caseReq) (*http.Response, []decidedPerm) {
	t.Helper()
	caseDecisionMu.Lock()
	caseDecisions = nil
	caseDecisionMu.Unlock()

	resp := do(t, s, req.method, req.path, token, req.body)

	caseDecisionMu.Lock()
	decided := append([]decidedPerm(nil), caseDecisions...)
	caseDecisionMu.Unlock()
	return resp, decided
}

// assertNamesDeclaredPermission: a 403 of an InHandler route names the permission the caller
// is missing (design「403 响应写明缺哪个权限」).
func assertNamesDeclaredPermission(t *testing.T, body string, perms []string) {
	t.Helper()
	for _, p := range perms {
		if strings.Contains(body, p) {
			return
		}
	}
	t.Errorf("403 body %q names none of the declared permissions %v", body, perms)
}

// assertDecidedOnly: a handler must not decide a permission its route does not declare
// (the design's calling assertion, in the only direction a single request can check).
func assertDecidedOnly(t *testing.T, decided []decidedPerm, declared []string, side string) {
	t.Helper()
	for _, d := range decided {
		found := false
		for _, p := range declared {
			if p == d.perm {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: the handler decided %s, which the route does not declare (%v)", side, d.perm, declared)
		}
	}
}

// The decision collector: the harness runs its requests serially (never t.Parallel), so a
// package-level slice plus the mutex is enough to attribute the decisions of ONE request.
var (
	caseDecisionMu sync.Mutex
	caseDecisions  []decidedPerm
)

// installCaseRecorder points the decision hook at the collector and returns the restore.
func installCaseRecorder() func() {
	prev := decisionHook
	decisionHook = func(_ *rux.Context, perm string, granted bool) {
		caseDecisionMu.Lock()
		caseDecisions = append(caseDecisions, decidedPerm{perm: perm, granted: granted})
		caseDecisionMu.Unlock()
	}
	return func() { decisionHook = prev }
}
