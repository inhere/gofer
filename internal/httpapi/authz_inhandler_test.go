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

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/tunnel"
	"github.com/inhere/gofer/internal/xfer"
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
// 再把数据库那一半换成用例自己写的两套（空 / 声明的权限）—— 这正是解析器读的那一份数据
// （setDBGrants 装进解析器的授权缓存），所以判定走的完全是生产路径。

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

	// The session-domain resources (plan T1.5c): the registered terminal session of owner,
	// a nudge on it, and the work item whose current session it is.
	sessionID       string
	nudgeID         string
	workItemID      string
	capturedID      string
	foreignThreadID string

	// The plan-domain resources (plan T1.5d): the plan of owner with one checklist item
	// in it (a todo has no owner of its own — its plan's owner is its resource owner).
	planID string
	todoID string

	// The schedule-domain resource (plan T1.5e): the schedule of owner. Its creator is
	// recorded inside the stored request rather than in a column.
	scheduleID string

	// The tunnel-domain resources (plan T1.5f): the external forwarder registration of owner
	// and the hosted forwarder owner started.
	forwarderID string
	hostedName  string

	// The transfer-domain resource (plan T1.5g): the transfer of owner.
	transferID string
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
	// extraOthers are further requests of the "someone else's resource" half, each refused
	// with a 403 that names a declared permission. They exist for the routes whose body picks
	// WHICH resource is checked (POST /v1/work-items/{id}/dispatch: a session, a job or a
	// plan), so one request can decide only one of the declared permissions.
	extraOthers []func(e *authzCaseEnv) caseReq
}

// authzCaseMentionAgent is the cli-agent the reverse-case harness configures (and allows in the
// fixture project): a comment can only dispatch an agent its project allows, so the comment case of
// the job domain needs one that really resolves.
const authzCaseMentionAgent = "mention-ok"

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
	// reject is review always, plus job.submit when it asks to resume (a resume starts a new
	// job): the resume body makes the own half decide both permissions.
	{key: "POST /v1/jobs/{id}/reject", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/reject", map[string]any{"note": "again", "resume": true}}
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

	// comments (plan T1.5a): a comment whose @-mention really dispatches a job needs
	// job.submit (design §1's job.submit row: 「评论 @ 派发」), and one that dispatches nothing
	// asks nothing — the body below mentions the agent the harness project allows, so this
	// request WOULD start a job. The 「someone else's resource」 half holds no permission and is
	// refused with job.submit named, BEFORE the comment is written; the own half holds
	// job.submit and the comment is recorded (the dispatch it asks for is left to the harness's
	// worker, so the answer is the comment view either way).
	{key: "POST /v1/jobs/{id}/comments", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/jobs/" + e.job.ID + "/comments",
			commentReq{Body: "@" + authzCaseMentionAgent + " please look at this"}}
	}},
	// The plan and todo threads dispatch through the same job-layer path (design 附录 A
	// 「@ 派发规则同 job comments」), so their declarations carry the same rule and each gets its
	// own case: the plan thread, the id-addressed todo thread, and the nested plan-todo one (whose
	// handler checks the todo belongs to that plan BEFORE the dispatch question).
	{key: "POST /v1/plans/{id}/comments", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/plans/" + e.plan() + "/comments",
			commentReq{Body: "@" + authzCaseMentionAgent + " please look at this"}}
	}},
	{key: "POST /v1/todos/{todo_id}/comments", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/todos/" + e.todo() + "/comments",
			commentReq{Body: "@" + authzCaseMentionAgent + " please look at this"}}
	}},
	{key: "POST /v1/plans/{id}/todos/{todo_id}/comments", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/plans/" + e.plan() + "/todos/" + e.todo() + "/comments",
			commentReq{Body: "@" + authzCaseMentionAgent + " please look at this"}}
	}},

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

	// The workbench routes of plan T1.5c whose declaration is a job permission (the boundary
	// of this registry is the permission, not the task): the thread list is the FILTER form of
	// job.read.others, the diff its deny form, and review continues the thread with a new job
	// over the agent session (job.manage.others).
	{key: "GET /v1/workbench/threads",
		req:        func(*authzCaseEnv) caseReq { return caseReq{http.MethodGet, "/v1/workbench/threads", nil} },
		wantOthers: workbenchThreadHides,
		wantOwn:    workbenchThreadShows},
	{key: "GET /v1/workbench/threads/{id}/diff", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/workbench/threads/j:" + e.job.ID + "/diff", nil}
	}},
	{key: "POST /v1/workbench/threads/{id}/review", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/workbench/threads/j:" + e.job.ID + "/review", map[string]any{}}
	}},
}

// planAuthzCases is the reverse-case registry of plan T1.5d: the plan container, its
// checklist and the work item that may become one.
var planAuthzCases = []authzCase{
	// ---- the plan container ----
	// PATCH /v1/plans/{id} carries three permissions, so its two halves stand for two
	// BRANCHES rather than for two resources (like POST /v1/decisions/{id}/answer does for
	// T1.5b): the "someone else's plan" half changes a stranger's plan and is refused
	// (plan.manage.others), the own half flips the leader switch on (job.submit) AND re-binds
	// the supervisor session (plan.supervise.others) — which is what decides both of them.
	{key: "PATCH /v1/plans/{id}",
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPatch, "/v1/plans/" + e.plan(), map[string]any{"status": jobstore.PlanDone}}
		},
		reqOwn: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPatch, "/v1/plans/" + e.plan(), map[string]any{
				"leader": jobstore.PlanLeaderOn, "supervisor_session_id": e.session(),
			}}
		}},
	{key: "PUT /v1/plans/{id}/handoff", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPut, "/v1/plans/" + e.plan() + "/handoff",
			planHandoffPutReq{Body: "hand the plan over", ExpectedVersion: 0}}
	}},
	{key: "POST /v1/plans/{id}/jobs", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/plans/" + e.plan() + "/jobs", attachJobReq{JobID: e.job.ID}}
	}},
	{key: "POST /v1/plans/{id}/todos", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/plans/" + e.plan() + "/todos", addTodoReq{Title: "one more item"}}
	}},
	{key: "POST /v1/plans/{id}/pause", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/plans/" + e.plan() + "/pause", nil}
	}},
	{key: "POST /v1/plans/{id}/resume", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/plans/" + e.plan() + "/resume", nil}
	}},
	{key: "POST /v1/plans/{id}/run", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/plans/" + e.plan() + "/run", nil}
	}},

	// ---- the checklist ----
	// A move to ready may DISPATCH the item (job.submit) on top of the item's plan rule
	// (plan.manage.others). The own half holds every declared permission, so it passes both
	// and the dispatch itself finds no assignee and starts nothing.
	{key: "PATCH /v1/todos/{todo_id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPatch, "/v1/todos/" + e.todo(), map[string]any{"status": jobstore.TodoReady}}
	}},
	{key: "POST /v1/todos/{todo_id}/dispatch", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/todos/" + e.todo() + "/dispatch", nil}
	}},

	// ---- the work item that becomes an item of a plan ----
	// The TARGET plan decides: the other half converts into the owner's plan, the own half
	// into the caller's own plan (whose id the case env seeds per half).
	{key: "POST /v1/work-items/{id}/to-todo", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/work-items/" + e.workItem() + "/to-todo", map[string]any{"plan_id": e.plan()}}
	}},
}

// scheduleAuthzCases is the reverse-case registry of plan T1.5e: the write entries of the
// schedule container. The domain's other three routes need no case here — the two reads are
// Public and POST /v1/schedules is a Require(job.submit) — so the derivation below (which reads
// the declaration table) claims exactly these five.
var scheduleAuthzCases = []authzCase{
	{key: "DELETE /v1/schedules/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/schedules/" + e.schedule(), nil}
	}},
	{key: "POST /v1/schedules/{id}/disable", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/schedules/" + e.schedule() + "/disable", nil}
	}},
	// Enabling switches the schedule's sweep on and the jobs it will start have no later
	// checkpoint, so this entry asks job.submit on top of the manage rule — exactly like
	// run-now. The own half holds every declared permission and really flips the switch.
	{key: "POST /v1/schedules/{id}/enable", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/schedules/" + e.schedule() + "/enable", nil}
	}},
	// run-now submits the stored request in this very request. The case schedule's stored request
	// names a project that does not exist, so the own half passes both permissions and then FAILS
	// to submit (400) — the harness never starts a real job, and a 403 would have been the
	// permission rule, which is what the other half must see.
	{key: "POST /v1/schedules/{id}/run-now",
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/schedules/" + e.schedule() + "/run-now", nil}
		},
		wantOwn: func(t *testing.T, resp *http.Response, _ *authzCaseEnv) {
			t.Helper()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("run-now on the caller's OWN schedule = %d, want 400 (the stored request names an unknown project): "+
					"the permission rules must let it through, the submit is what fails (body %s)",
					resp.StatusCode, readBody(t, resp))
			}
		}},
	{key: "POST /v1/schedules/{id}/rotate-token", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/schedules/" + e.schedule() + "/rotate-token", nil}
	}},
}

// tunnelAuthzCases is the reverse-case registry of plan T1.5f: the tunnel endpoint (which
// authenticates itself and therefore decides tunnel.open inside its handler), the external
// forwarder stop route and the hosted stop route.
var tunnelAuthzCases = []authzCase{
	// The endpoint runs OUTSIDE the /v1 group, so its handler resolves the caller and asks
	// tunnel.open itself: a caller without it is refused before the worker is even looked up,
	// while the own half reaches the worker lookup — which the case server answers "offline"
	// (404), proving the permission rule let the request through.
	{key: "GET /v1/tunnels/connect", req: func(*authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/tunnels/connect?worker=w-case&target=127.0.0.1:80", nil}
	}},
	// The external forwarder registry: the caller that registered a forwarder may stop it, anyone
	// else needs tunnel.manage.others (仅根 — the successor of the owner-or-can_admin test).
	{key: "POST /v1/tunnels/forwarders/{id}/stop", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/tunnels/forwarders/" + e.forwarder() + "/stop", nil}
	}},
	// The hosted stop route carries all three tunnel permissions: stopping someone else's hosted
	// forwarder is tunnel.manage.others (仅根), the starter's own needs tunnel.hosted + tunnel.open.
	{key: "DELETE /v1/tunnels/hosted/{name}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/tunnels/hosted/" + e.hostedForwarder(), nil}
	}},
}

// transferAuthzCases is the reverse-case registry of plan T1.5g: the transfer reads and the
// delete (one permission, xfer.manage.others, decided by the resource owner), plus
// GET /v1/sessions/{sid}/tail — declared by T1.4 with a session permission although its
// handler is steward_handler.go, so it belongs to this task (the session domain's derivation
// excludes it by key).
var transferAuthzCases = []authzCase{
	// The list is the FILTER form: without xfer.manage.others the answer is narrowed to the
	// caller's own transfers instead of being refused.
	{key: "GET /v1/xfer",
		req:        func(e *authzCaseEnv) caseReq { e.transfer(); return caseReq{http.MethodGet, "/v1/xfer", nil} },
		wantOthers: transferListHides,
		wantOwn:    transferListShows},
	{key: "GET /v1/xfer/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/xfer/" + e.transfer(), nil}
	}},
	{key: "DELETE /v1/xfer/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/xfer/" + e.transfer(), nil}
	}},
	// The content route's user arm: the case transfer is a PUT, so the caller's OWN download
	// answers 409 (there is nothing to fetch) — anything but the 403 the permission rule would
	// write, which is what the assertion is about. The worker arm is its own rule and is pinned
	// by xfer_handler_test.go.
	{key: "GET /v1/xfer/{id}/content",
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodGet, "/v1/xfer/" + e.transfer() + "/content", nil}
		},
		wantOwn: func(t *testing.T, resp *http.Response, _ *authzCaseEnv) {
			t.Helper()
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("the caller's OWN put download = %d, want 409 (nothing to download): "+
					"the permission rule must let the owner through (body %s)", resp.StatusCode, readBody(t, resp))
			}
		}},
	// The transcript tail is a session READ: someone else's session needs session.read.others,
	// the caller's own session needs nothing. The own half really reads the tail (the case
	// session exists, so the fallback answer is a 200).
	{key: "GET /v1/sessions/{sid}/tail", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/sessions/" + e.session() + "/tail", nil}
	}},
}

// sessionAuthzCases is the reverse-case registry of plan T1.5c: the terminal sessions, the
// nudges and the workbench turn (whose permission the TARGET of the turn decides).
var sessionAuthzCases = []authzCase{
	// ---- reads (`session.read.others`, deny) ----
	{key: "GET /v1/sessions",
		req:        func(e *authzCaseEnv) caseReq { e.session(); return caseReq{http.MethodGet, "/v1/sessions", nil} },
		wantOthers: sessionListHides,
		wantOwn:    sessionListShows},
	{key: "GET /v1/sessions/{sid}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/sessions/" + e.session(), nil}
	}},
	{key: "GET /v1/sessions/{sid}/messages", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/sessions/" + e.session() + "/messages", nil}
	}},
	{key: "GET /v1/sessions/{sid}/nudges", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/sessions/" + e.session() + "/nudges", nil}
	}},
	{key: "GET /v1/sessions/{sid}/takeover-plan", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/sessions/" + e.session() + "/takeover-plan", nil}
	}},
	{key: "GET /v1/sessions/{sid}/turns/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/sessions/" + e.session() + "/turns/turn-1", nil}
	}},
	{key: "GET /v1/sessions/{sid}/watches", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/sessions/" + e.session() + "/watches", nil}
	}},
	// The cross-session nudge list is the FILTER form: the answer stays 200 and only loses the
	// other caller's rows.
	{key: "GET /v1/nudges",
		req:        func(e *authzCaseEnv) caseReq { e.nudge(); return caseReq{http.MethodGet, "/v1/nudges", nil} },
		wantOthers: nudgeListHides,
		wantOwn:    nudgeListShows},

	// ---- 说话与待发内容 (`session.reply.others`, deny) ----
	{key: "GET /v1/sessions/{sid}/outbox", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodGet, "/v1/sessions/" + e.session() + "/outbox", nil}
	}},
	{key: "POST /v1/sessions/{sid}/messages", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/messages", map[string]string{"message": "hi"}}
	}},
	{key: "POST /v1/sessions/{sid}/say", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/say", map[string]string{"answer": "hi"}}
	}},
	// A deliver with allow_takeover is ALSO a job.submit entry (design §1: deliver，允许接管时起 pty
	// job): the own half passes the session rule and then decides job.submit, while the other half
	// is refused by the session rule before it gets that far.
	{key: "POST /v1/sessions/{sid}/deliver", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/deliver",
			map[string]any{"text": "hi", "allow_takeover": true}}
	}},
	{key: "POST /v1/sessions/{sid}/resume", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/resume", map[string]any{}}
	}},
	{key: "POST /v1/sessions/{sid}/relay", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/relay", map[string]string{"mode": "on"}}
	}},
	{key: "POST /v1/sessions/{sid}/nudges", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/nudges",
			map[string]any{"kind": "every", "interval_sec": 600, "text": "hi"}}
	}},
	// A person's session watch: the job belongs to the OWNER of the session, so the own half is
	// the caller watching its own job from its own terminal (a job credential skips the session
	// rule on this route, which the handler's own branch covers).
	{key: "POST /v1/sessions/{sid}/watches", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/watches", map[string]string{"job_id": e.job.ID}}
	}},
	{key: "POST /v1/sessions/{sid}/watches/complete", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/watches/complete", map[string]any{"job_ids": []string{}}}
	}},
	{key: "DELETE /v1/sessions/{sid}/watches/{job_id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/sessions/" + e.session() + "/watches/" + e.job.ID, nil}
	}},
	{key: "POST /v1/sessions/{sid}/turns/{id}/ack", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/turns/turn-1/ack", nil}
	}},
	{key: "DELETE /v1/sessions/{sid}/turns/{id}/ack", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/sessions/" + e.session() + "/turns/turn-1/ack", nil}
	}},
	{key: "POST /v1/sessions/{sid}/turns/{id}/complete-watches", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/turns/turn-1/complete-watches", map[string]any{"job_ids": []string{}}}
	}},
	{key: "POST /v1/sessions/{sid}/mail/claim", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/mail/claim", map[string]any{}}
	}},
	{key: "POST /v1/sessions/{sid}/mail/confirm", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/mail/confirm", map[string]any{"ids": []int64{}}}
	}},
	{key: "POST /v1/sessions/{sid}/turns/{id}/complete-mail", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/turns/turn-1/complete-mail", map[string]any{}}
	}},
	{key: "PATCH /v1/nudges/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPatch, "/v1/nudges/" + e.nudge(), map[string]string{"state": "paused"}}
	}},
	{key: "DELETE /v1/nudges/{id}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/nudges/" + e.nudge(), nil}
	}},

	// ---- manage (`session.manage.others`, deny) ----
	{key: "DELETE /v1/sessions/{sid}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodDelete, "/v1/sessions/" + e.session(), nil}
	}},
	{key: "PATCH /v1/sessions/{sid}", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPatch, "/v1/sessions/" + e.session(), map[string]any{"topic": "t"}}
	}},
	{key: "POST /v1/sessions/{sid}/turns", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/turns", map[string]any{"body": "hi"}}
	}},
	{key: "POST /v1/sessions/{sid}/turns/{id}/release", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/turns/turn-1/release", map[string]any{"idle_sec": 300}}
	}},
	{key: "POST /v1/sessions/{sid}/release-takeover", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/release-takeover", nil}
	}},

	// ---- 工具权限提示作答 (`session.permission.answer`, deny) ----
	{key: "POST /v1/sessions/{sid}/permissions/{id}/answer", req: func(e *authzCaseEnv) caseReq {
		return caseReq{http.MethodPost, "/v1/sessions/" + e.session() + "/permissions/perm-1/answer",
			map[string]string{"answer": "allow"}}
	}},

	// ---- 说话入口按目标分流 ----
	// A terminal session follows session.reply.others; a SESSION JOB (ACP / pty) is 对会话 job
	// 说话 and follows job.manage.others. The two halves therefore stand for the route's two
	// permission branches, like POST /v1/decisions/{id}/answer does for T1.5b: the own half
	// asks about the case JOB (its id is the session id an ACP / pty session job is addressed
	// by), which the job branch answers.
	{key: "POST /v1/session-ask",
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/session-ask",
				map[string]string{"session_id": e.session(), "text": "hi"}}
		},
		reqOwn: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/session-ask",
				map[string]string{"session_id": e.job.ID, "text": "hi"}}
		}},
	// The capture box's recommendation is a FILTER read: the answer stays 200 and only loses
	// the other caller's sessions (they are removed before ranking). The item is the case's
	// work item, whose project "self" every case session shares.
	{key: "GET /v1/work-items/{id}/dispatch-targets",
		req: func(e *authzCaseEnv) caseReq {
			// Every case shares one store and only the top three come back: give this case's
			// session a topic of its own and name it in the item, so it always ranks first.
			word := "dtx" + strings.NewReplacer("-", "", "_", "").Replace(e.job.ID)
			if _, _, _, err := e.s.jobs.Meta().SetSessionTopic(e.session(), &word, nil, jobstore.SessionTopicHuman); err != nil {
				e.t.Fatalf("set topic: %v", err)
			}
			w, err := e.s.jobs.Meta().CreateWorkItem(jobstore.WorkItemInput{Title: word + " 想法", ProjectKey: "self", By: e.owner})
			if err != nil {
				e.t.Fatalf("create work item: %v", err)
			}
			return caseReq{http.MethodGet, "/v1/work-items/" + w.ID + "/dispatch-targets", nil}
		},
		wantOthers: sessionListHides,
		wantOwn:    sessionListShows},
	// report-request always targets a TERMINAL session (the ledger refuses an ACP / pty job
	// session), so its denial is per session and shows up as an unwritten request instead of a
	// 403: the answer is 200 with `sent:false` and the reason the ledger recorded.
	{key: "POST /v1/work-items/{id}/report-request",
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/work-items/" + e.workItem() + "/report-request", map[string]any{}}
		},
		wantOthers: reportRequestRefused},

	// The capture box's hand-over decides a different permission per target: to=session
	// session.reply.others on the target session, to=job job.read.others on the linked job,
	// to=todo plan.manage.others on the chosen plan. The "someone else's" half therefore asks
	// all three (the first one is req, the other two extraOthers); the own half converts the
	// item into the caller's own plan and is accepted.
	{key: "POST /v1/work-items/{id}/dispatch",
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/work-items/" + e.capturedItem() + "/dispatch",
				map[string]any{"to": "session", "session_id": e.session()}}
		},
		extraOthers: []func(e *authzCaseEnv) caseReq{
			func(e *authzCaseEnv) caseReq {
				return caseReq{http.MethodPost, "/v1/work-items/" + e.capturedItem() + "/dispatch",
					map[string]any{"to": "job", "job_id": e.job.ID}}
			},
			func(e *authzCaseEnv) caseReq {
				return caseReq{http.MethodPost, "/v1/work-items/" + e.capturedItem() + "/dispatch",
					map[string]any{"to": "todo", "plan_id": e.plan()}}
			},
		},
		reqOwn: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/work-items/" + e.capturedItem() + "/dispatch",
				map[string]any{"to": "todo", "plan_id": e.plan()}}
		},
		wantOwn: func(t *testing.T, resp *http.Response, _ *authzCaseEnv) {
			t.Helper()
			assertStatusOK(t, resp)
		}},

	// ---- the workbench turn: a RELAY thread is 说话, an agent / one-shot thread is 对会话 job ----
	{key: "POST /v1/workbench/threads/{id}/turn",
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/workbench/threads/r:" + e.session() + "/turn",
				map[string]string{"text": "hi"}}
		},
		reqOwn: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/workbench/threads/s:" + e.foreignAgentThread() + "/turn",
				map[string]string{"text": "hi"}}
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
	{name: "terminal sessions, nudges and the workbench", derived: sessionDomainInHandler, cases: sessionAuthzCases},
	{name: "plans, todos and work items", derived: planDomainInHandler, cases: planAuthzCases},
	{name: "schedules", derived: scheduleDomainInHandler, cases: scheduleAuthzCases},
	{name: "the tunnels", derived: tunnelDomainInHandler, cases: tunnelAuthzCases},
	{name: "the transfers and the steward's session tail", derived: transferDomainInHandler, cases: transferAuthzCases},
	{name: "the configuration, the workers and the updates", derived: adminDomainInHandler, cases: adminAuthzCases},
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

// session seeds a registered terminal session owned by e.owner and returns its id. Every
// session route of the domain resolves it through the relay (relay.Session), which is what
// the permission rules read for the owner.
func (e *authzCaseEnv) session() string {
	if e.sessionID == "" {
		e.sessionID = "sess-" + e.owner + "-" + e.job.ID
		now := time.Now().Unix()
		if _, err := e.s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
			SessionID: e.sessionID, Agent: "claude", ProjectKey: "self", Runner: "local",
			Cwd: ".", CallerID: e.owner, State: jobstore.SessionIdle, StartedAt: now, LastSeenAt: now,
		}); err != nil {
			e.t.Fatalf("upsert session for %s: %v", e.owner, err)
		}
	}
	return e.sessionID
}

// nudge creates one nudge on e.session() and returns its id.
func (e *authzCaseEnv) nudge() string {
	if e.nudgeID == "" {
		n, err := e.s.relay.CreateNudge(e.session(), sessionrelay.NudgeInput{
			Kind: jobstore.NudgeEvery, Interval: time.Hour, Text: "ping", By: e.owner,
		})
		if err != nil {
			e.t.Fatalf("create nudge for %s: %v", e.owner, err)
		}
		e.nudgeID = n.ID
	}
	return e.nudgeID
}

// workItem seeds one work item whose CURRENT session is e.session(), which is what a report
// request walks (the ledger only asks the item's current sessions).
func (e *authzCaseEnv) workItem() string {
	if e.workItemID == "" {
		w, err := e.s.jobs.Meta().CreateWorkItem(jobstore.WorkItemInput{
			Title: "case item", Goal: "case", ProjectKey: "self",
			SessionIDs: []string{e.session()}, By: e.owner,
		})
		if err != nil {
			e.t.Fatalf("create work item for %s: %v", e.owner, err)
		}
		e.workItemID = w.ID
	}
	return e.workItemID
}

// transferListHides / transferListShows assert the FILTER answer of the transfer list: the
// other caller's transfer is not in it, the caller's own is.
func transferListHides(t *testing.T, resp *http.Response, e *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); strings.Contains(body, e.transferID) {
		t.Fatalf("the transfer filter leaked another caller's transfer %s: %s", e.transferID, body)
	}
}

func transferListShows(t *testing.T, resp *http.Response, e *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); !strings.Contains(body, e.transferID) {
		t.Fatalf("the caller's own transfer %s is missing from its list: %s", e.transferID, body)
	}
}

// sessionListHides / sessionListShows assert the FILTER answer of the session list: the other
// caller's session is not in it, the caller's own is.
func sessionListHides(t *testing.T, resp *http.Response, e *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); strings.Contains(body, e.sessionID) {
		t.Fatalf("the session filter leaked another caller's session %s: %s", e.sessionID, body)
	}
}

func sessionListShows(t *testing.T, resp *http.Response, e *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); !strings.Contains(body, e.sessionID) {
		t.Fatalf("the caller's own session %s is missing from its list: %s", e.sessionID, body)
	}
}

// nudgeListHides / nudgeListShows assert the same for the cross-session nudge list.
func nudgeListHides(t *testing.T, resp *http.Response, e *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); strings.Contains(body, e.nudgeID) {
		t.Fatalf("the nudge filter leaked another caller's nudge %s: %s", e.nudgeID, body)
	}
}

func nudgeListShows(t *testing.T, resp *http.Response, e *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); !strings.Contains(body, e.nudgeID) {
		t.Fatalf("the caller's own nudge %s is missing from its list: %s", e.nudgeID, body)
	}
}

// reportRequestRefused asserts the denial the ledger records for a session the caller may
// not speak to: the request is not sent, and the request itself answers 200 (the denial is
// per session and data, not a route-level 403).
func reportRequestRefused(t *testing.T, resp *http.Response, _ *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); strings.Contains(body, `"sent":true`) {
		t.Fatalf("another caller's session was asked to report: %s", body)
	}
}

// assertStatusOK asserts a plain 200.
func assertStatusOK(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
}

// workbenchThreadHides / workbenchThreadShows assert the FILTER answer of the workbench
// thread list (the case job's thread id carries the job id).
func workbenchThreadHides(t *testing.T, resp *http.Response, e *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); strings.Contains(body, e.job.ID) {
		t.Fatalf("the workbench filter leaked another caller's thread: %s", body)
	}
}

func workbenchThreadShows(t *testing.T, resp *http.Response, e *authzCaseEnv) {
	t.Helper()
	assertStatusOK(t, resp)
	if body := readBody(t, resp); !strings.Contains(body, e.job.ID) {
		t.Fatalf("the caller's own thread is missing from its list: %s", body)
	}
}

// foreignAgentThread seeds one AGENT thread — a session-bound job — owned by another caller and
// returns the SESSION ID of the thread (its workbench thread id is "s:<session id>"). The agent
// branch of the turn is the one that continues the thread with a new job, so a thread belonging
// to someone else is what makes both job.manage.others AND job.submit decided. The job carries a
// session state and is RUNNING, so the service refuses to continue it without starting anything
// (the 409 the pre-migration route gave for a thread that is not waiting for input).
func (e *authzCaseEnv) foreignAgentThread() string {
	if e.foreignThreadID == "" {
		sid, id := "sess-other-"+e.job.ID, e.job.ID+"-agent"
		now := time.Now().Unix()
		if err := e.s.jobs.Meta().UpsertJob(jobstore.JobRecord{
			ID: id, ProjectKey: "self", Agent: "claude", Runner: "worker", Interactive: true,
			SessionID: sid, SessionStateJSON: "{}", Status: job.StatusRunning, Cwd: ".",
			StartedAt: now, UpdatedAt: now, CallerID: "bob",
		}); err != nil {
			e.t.Fatalf("upsert the other caller's session job: %v", err)
		}
		e.foreignThreadID = sid
	}
	return e.foreignThreadID
}

// TestSessionNonUserArmsAnswerAsBeforeTheMigration pins the arm every session rule gives a
// caller that is NOT governed by permissions (design §2「只对 user 类调用方计算」): the
// pre-migration sessionMayAnswer answer, never a catalog lookup. A job credential still reads
// an OWNERLESS session's pending messages (CallerID == "": nobody to compare against), while a
// worker token is refused there exactly as before the migration — a worker runs jobs, it does
// not answer for a person.
func TestSessionNonUserArmsAnswerAsBeforeTheMigration(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
	})
	now := time.Now().Unix()
	if _, err := s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "sess-ownerless", Agent: "claude", ProjectKey: "self", Cwd: ".",
		State: jobstore.SessionIdle, StartedAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatalf("upsert the ownerless session: %v", err)
	}

	jobTok := seedJobToken(t, s, "job-outbox", jobstore.JobCredentialMember, "")
	resp := do(t, s, http.MethodGet, "/v1/sessions/sess-ownerless/outbox", jobTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a job credential reading an ownerless session's outbox = %d, want 200 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	resp = do(t, s, http.MethodGet, "/v1/sessions/sess-ownerless/outbox", "tok-worker", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a worker token reading a session's outbox = %d, want 403 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
}

// TestPlanNonUserArmsAnswerAsBeforeTheMigration pins the arm the plan rules give a caller that
// is NOT governed by permissions (design §2「只对 user 类调用方计算」): the pre-migration answer,
// never a catalog lookup. The non-user caller that reaches a plan route is a LEADER job
// credential — SEC-01 opens PATCH /v1/todos/* and PUT /v1/plans/*/handoff, and what narrows it
// there is the credential's own target rule, not a permission. (A worker token never gets into
// a plan handler at all: workercredential.go's whitelist holds no plan route.)
func TestPlanNonUserArmsAnswerAsBeforeTheMigration(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
	})
	now := time.Now().Unix()
	for _, id := range []string{"plan-leader", "plan-third"} {
		if err := s.jobs.Meta().InsertPlan(jobstore.Plan{
			PlanID: id, Title: "someone else's plan", Status: jobstore.PlanOpen, Owner: "bob",
			ProjectKey: "self", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("insert plan %s: %v", id, err)
		}
	}
	if err := s.jobs.Meta().InsertTodo(jobstore.PlanTodo{
		TodoID: "todo-leader", PlanID: "plan-leader", Title: "the leader's item",
		Status: jobstore.TodoPending, Auto: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("insert todo: %v", err)
	}
	leader := seedJobToken(t, s, "job-leader", jobstore.JobCredentialLeader, "plan-leader")

	// Its own plan's item: SEC-01 plus jobMaySetTodo decide, no permission is consulted.
	resp := do(t, s, http.MethodPatch, "/v1/todos/todo-leader", leader,
		map[string]any{"status": jobstore.TodoReady})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a leader job credential moving its own plan's item to ready = %d, want 200 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	// Its own plan's handoff: the same.
	resp = do(t, s, http.MethodPut, "/v1/plans/plan-leader/handoff", leader,
		planHandoffPutReq{Body: "carry on", ExpectedVersion: 0})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a job credential writing its own plan's handoff = %d, want 200 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	// Another plan's handoff still belongs to the credential's own target rule.
	resp = do(t, s, http.MethodPut, "/v1/plans/plan-third/handoff", leader,
		planHandoffPutReq{Body: "carry on", ExpectedVersion: 0})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a job credential writing another plan's handoff = %d, want 403 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
}

// TestScheduleNonUserArmsAnswerAsBeforeTheMigration pins the arm the schedule rules give a
// caller that is NOT governed by permissions (design §2「只对 user 类调用方计算」): the
// pre-migration answer, never a catalog lookup. No non-user caller reaches a schedule WRITE
// handler at all — SEC-01 (jobcredential.go) refuses every one of them to a job credential, and
// workercredential.go's whitelist holds no schedule route — so the write side of the domain is
// untouched by the migration. The READS stay open to a job credential (SEC-01 admits every GET),
// with the webhook secret masked for it because it owns nothing (gofer-hxld).
func TestScheduleNonUserArmsAnswerAsBeforeTheMigration(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
	})
	const secret = "aliceScheduleToken"
	raw, err := json.Marshal(job.JobRequest{
		CallerID: "alice", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := s.jobs.Meta().InsertSchedule(jobstore.ScheduleRecord{
		ID: "sch-alice", Name: "alice's schedule", ScheduleType: "cron", CronExpr: "*/5 * * * *",
		RequestJSON: string(raw), Enabled: 1, NextRunAt: now + 300, CatchUp: 1,
		ProjectKey: "self", TriggerToken: secret, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	jobTok := seedJobToken(t, s, "job-schedule", jobstore.JobCredentialMember, "")

	// A schedule write stays refused by SEC-01: its credential is the authority there, and the
	// migration did not open the route to it.
	resp := do(t, s, http.MethodDelete, "/v1/schedules/sch-alice", jobTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a job credential deleting a schedule = %d, want 403 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); !strings.Contains(body, "job credential may not delete a schedule") {
		t.Fatalf("the refusal must stay SEC-01's: %s", body)
	}

	// A worker token never reaches the schedule surface either.
	resp = do(t, s, http.MethodPost, "/v1/schedules/sch-alice/run-now", "tok-worker", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a worker token running a schedule = %d, want 403 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	// The read it always had, with the secret masked for a caller that owns nothing.
	resp = do(t, s, http.MethodGet, "/v1/schedules/sch-alice", jobTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a job credential reading a schedule = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	var view scheduleView
	decode(t, resp, &view)
	wantMasked := secret[:4] + strings.Repeat("*", len(secret)-4)
	if view.TriggerToken != wantMasked {
		t.Fatalf("a job credential read trigger_token=%q, want the masked %q", view.TriggerToken, wantMasked)
	}
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

// plan seeds one plan owned by e.owner — the resource of every plan-domain rule — and
// returns its id.
func (e *authzCaseEnv) plan() string {
	if e.planID == "" {
		e.planID = "plan-" + e.owner + "-" + e.job.ID
		now := time.Now().Unix()
		if err := e.s.jobs.Meta().InsertPlan(jobstore.Plan{
			PlanID: e.planID, Title: "case plan", Status: jobstore.PlanOpen, Owner: e.owner,
			ProjectKey: "self", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			e.t.Fatalf("insert plan for %s: %v", e.owner, err)
		}
	}
	return e.planID
}

// todo seeds one checklist item of e.plan() and returns its id. Its resource owner is the
// owner of that plan.
func (e *authzCaseEnv) todo() string {
	if e.todoID == "" {
		e.todoID = "todo-" + e.owner + "-" + e.job.ID
		now := time.Now().Unix()
		if err := e.s.jobs.Meta().InsertTodo(jobstore.PlanTodo{
			TodoID: e.todoID, PlanID: e.plan(), Title: "case item", Status: jobstore.TodoPending,
			Auto: true, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			e.t.Fatalf("insert todo for %s: %v", e.owner, err)
		}
	}
	return e.todoID
}

// schedule seeds one schedule of e.owner — the resource of every schedule rule — and returns its
// id. The creator is recorded the way the create handler records it: inside the stored request.
// That request names a project that does not exist, so the run-now case reaches its job.submit
// decision and then fails to submit instead of starting a job (the harness starts none).
func (e *authzCaseEnv) schedule() string {
	if e.scheduleID == "" {
		e.scheduleID = "sch-" + e.owner + "-" + e.job.ID
		raw, err := json.Marshal(job.JobRequest{
			CallerID: e.owner, ProjectKey: "no-such-project", Agent: "exec", Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		})
		if err != nil {
			e.t.Fatalf("marshal the case schedule request: %v", err)
		}
		now := time.Now().Unix()
		if err := e.s.jobs.Meta().InsertSchedule(jobstore.ScheduleRecord{
			ID: e.scheduleID, Name: "case schedule", ScheduleType: "cron", CronExpr: "*/5 * * * *",
			RequestJSON: string(raw), Enabled: 1, NextRunAt: now + 300, CatchUp: 1,
			ProjectKey: "self", TriggerToken: "caseSchedulesecret", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			e.t.Fatalf("insert schedule for %s: %v", e.owner, err)
		}
	}
	return e.scheduleID
}

// forwarder registers one external forwarder process of e.owner (with the stop capability)
// and returns its hub-assigned id.
func (e *authzCaseEnv) forwarder() string {
	if e.forwarderID == "" {
		reg := e.s.forwarders.Register(e.owner, tunnel.ForwarderRegistration{
			Worker: "w-case", Host: "case-host", PID: 1,
			Specs: []tunnel.ForwardSpec{{Network: "tcp", Bind: "127.0.0.1", LocalPort: 1502, Target: "127.0.0.1:1"}},
			Caps:  []string{tunnel.CapStop},
		})
		e.forwarderID = reg.ID
	}
	return e.forwarderID
}

// hostedForwarder starts one hosted forwarder of e.owner — on an ephemeral port, so the case
// needs no free port — and returns the name it is managed by. The dial is never reached: a
// case never connects to the listener.
func (e *authzCaseEnv) hostedForwarder() string {
	if e.hostedName == "" {
		e.hostedName = "hosted-" + e.owner + "-" + e.job.ID
		specs := []tunnel.ForwardSpec{{Network: "tcp", Bind: "127.0.0.1", LocalPort: 0, Target: "127.0.0.1:1"}}
		if _, err := e.s.hostedForwarders.Start(e.hostedName, e.owner, "w-case", specs); err != nil {
			e.t.Fatalf("start hosted forwarder of %s: %v", e.owner, err)
		}
		e.t.Cleanup(func() { _ = e.s.hostedForwarders.Stop(e.hostedName) })
	}
	return e.hostedName
}

// transfer stages one transfer of e.owner and returns its id. The row is a PUT with a
// finished state, so a download of it answers 409 (nothing to fetch) while its journal line is
// a real record for the status, list and delete routes.
func (e *authzCaseEnv) transfer() string {
	if e.transferID == "" {
		e.transferID = "xf-" + e.job.ID
		if err := e.s.jobs.Meta().InsertXfer(jobstore.XferRecord{
			ID: e.transferID, Op: string(xfer.OpPut), Runner: "local", ProjectKey: "self",
			Path: "tmp/" + e.transferID + ".bin", Size: 3, SHA256: "abc", State: string(xfer.StateDone),
			CallerID: e.owner, CreatedAt: time.Now().Unix(),
		}); err != nil {
			e.t.Fatalf("insert transfer row for %s: %v", e.owner, err)
		}
	}
	return e.transferID
}

// wakeup / retry / workflow rows are created on demand, so the cases that need one pay for
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
		// The mailbox routes: a letter's recipient may be a session job (job.manage.others)
		// and redaction follows job redaction (job.delete.others), so such a route's only
		// declared permission can be a job.* one — but its decision is the caller's RELATION
		// to the letter first and the recipient's type second, which has its own cell-by-cell
		// tests (authz_mail_test.go, mail_redact_test.go). Not claimed here, like the
		// session domain's exclusion below.
		if strings.HasPrefix(key, "POST /v1/mail") || strings.HasPrefix(key, "GET /v1/mail") {
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

// sessionDomainInHandler derives plan T1.5c's InHandler routes from the declaration table:
// every one that carries a `session.*` permission. POST /v1/decisions/{id}/answer also carries
// session.reply.others (for its relay kind) but is decided by interaction.answer, and it
// belongs to the interactions domain above, so a route with that permission is not claimed
// here; two further routes that carry a session.* permission are another task's (their handlers
// live there), and the two exclusions below name them.
func sessionDomainInHandler() []string {
	var out []string
	for key, decl := range routeDecls {
		if decl.user.kind != userInHandler || slices.Contains(decl.user.perms, authz.PermInteractionAnswer) {
			continue
		}
		// GET /v1/sessions/{sid}/tail carries a session.* permission too: T1.4's SEC-01
		// snapshot forced its declaration (the steward's read allowlist), while the handler is
		// steward_handler.go — a T1.5g route, migrated there, whose reverse case the T1.5g
		// derivation registers. It is not claimed here so the domain keeps its own rules in its
		// own task (transferDomainInHandler).
		if key == "GET /v1/sessions/{sid}/tail" {
			continue
		}
		// The mailbox routes carry a session.* permission because a letter to a terminal
		// session is session.reply.others; their rules (relation to the letter first, the
		// recipient's type second) have their own cell-by-cell tests (authz_mail_test.go).
		if strings.HasPrefix(key, "POST /v1/mail") || strings.HasPrefix(key, "GET /v1/mail") {
			continue
		}
		for _, p := range decl.user.perms {
			if strings.HasPrefix(p, "session.") {
				out = append(out, key)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// planDomainInHandler derives plan T1.5d's InHandler routes from the declaration table:
// every one that carries a `plan.*` permission (the plan container, its checklist items and
// the work item that is converted into one). Like the derivations above it reads the table,
// so a new plan route cannot be declared without a reverse case.
func planDomainInHandler() []string {
	var out []string
	for key, decl := range routeDecls {
		if decl.user.kind != userInHandler {
			continue
		}
		for _, p := range decl.user.perms {
			if strings.HasPrefix(p, "plan.") {
				out = append(out, key)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// scheduleDomainInHandler derives plan T1.5e's InHandler routes from the declaration table:
// every one that carries a `schedule.*` permission (the delete / 启停 / 轮换 token / run-now
// entries). It claims neither the domain's two Public reads nor POST /v1/schedules, which is a
// Require(job.submit) the route gate enforces.
func scheduleDomainInHandler() []string {
	var out []string
	for key, decl := range routeDecls {
		if decl.user.kind != userInHandler {
			continue
		}
		for _, p := range decl.user.perms {
			if strings.HasPrefix(p, "schedule.") {
				out = append(out, key)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// tunnelDomainInHandler derives plan T1.5f's InHandler routes from the declaration table: every
// one that carries a `tunnel.` permission. The hosted routes the GATE enforces are Require and
// are covered by the gate test instead (they need no reverse case: a missing permission is
// refused before any handler runs); only the three where a handler decides are claimed here.
func tunnelDomainInHandler() []string {
	var out []string
	for key, decl := range routeDecls {
		if decl.user.kind != userInHandler {
			continue
		}
		for _, p := range decl.user.perms {
			if strings.HasPrefix(p, "tunnel.") {
				out = append(out, key)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// transferDomainInHandler derives plan T1.5g's InHandler routes from the declaration table:
// every route carrying one of this domain's own permissions (xfer.* / memory.write /
// tracker.* / steward.manage — the four the plan's table 领域子任务 names), plus
// GET /v1/sessions/{sid}/tail. The tail is the reason this derivation cannot simply be a
// prefix test: T1.4 declared it with session.read.others (the SEC-01 snapshot forced it) while
// its handler lives in steward_handler.go, so the session domain excludes it by key and this
// task owns its reverse case. The domain's other routes are Public or Require, which the route
// gate covers (TestAuthzGateCategories) and which need no reverse case of their own.
func transferDomainInHandler() []string {
	var out []string
	for key, decl := range routeDecls {
		if decl.user.kind != userInHandler {
			continue
		}
		for _, p := range decl.user.perms {
			if strings.HasPrefix(p, "xfer.") || strings.HasPrefix(p, "tracker.") ||
				p == authz.PermMemoryWrite || p == authz.PermStewardManage {
				out = append(out, key)
				break
			}
		}
	}
	out = append(out, "GET /v1/sessions/{sid}/tail")
	sort.Strings(out)
	return out
}

// adminDomainInHandler derives plan T1.5h's InHandler routes from the declaration table: every
// route carrying one of the domain's own permission prefixes (config.* / project.* / rules.* /
// skills.* / mcp.* / worker.* / update.* — the two cross-domain entries the domain reaches are
// worker.upgrade and worker.upgrade.upload, so no other domain can claim them). The domain's
// other routes are Public reads or plain requirements the route gate enforces, which
// TestAuthzGateCategories and TestAdminGroupCellsMatchCallerMayAdmin cover; only the two whose
// body decides whether a SECOND permission of the catalog row applies are InHandler — POST
// /v1/workers/{id}/upgrade (`source=staged` runs an uploaded binary) and POST /v1/update/apply
// (a body that names workers upgrades them too).
func adminDomainInHandler() []string {
	var out []string
	for key, decl := range routeDecls {
		if decl.user.kind != userInHandler {
			continue
		}
		for _, p := range decl.user.perms {
			if strings.HasPrefix(p, "config.") || strings.HasPrefix(p, "project.") ||
				strings.HasPrefix(p, "rules.") || strings.HasPrefix(p, "skills.") ||
				strings.HasPrefix(p, "mcp.") || strings.HasPrefix(p, "worker.") ||
				strings.HasPrefix(p, "update.") {
				out = append(out, key)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// adminAuthzCases is the reverse-case registry of plan T1.5h's two InHandler routes. Each
// request names the resource that decides the route's SECOND permission, so the two halves walk
// the same path: the 「someone else's resource」 half holds nothing and is refused with the FIRST
// permission of the row named, the own half holds both and reaches the answer the never-matched
// resource gives (an offline worker, an unknown worker id) — the union assertion below therefore
// sees both declared permissions decided.
var adminAuthzCases = []authzCase{
	{
		key: "POST /v1/workers/{id}/upgrade",
		// `source=staged` is the branch that needs worker.upgrade.upload as well: the worker
		// executes the binary an earlier PUT .../upgrade/file stored.
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/workers/" + adminCaseWorkerID + "/upgrade",
				workerUpgradeRequest{Source: workerUpgradeSourceStaged, Force: true}}
		},
		wantOwn: func(t *testing.T, resp *http.Response, _ *authzCaseEnv) {
			t.Helper()
			// Both permissions were decided; the harness has no worker hub, so the upgrade
			// stops at "worker offline" and hands nothing over.
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("a staged upgrade of an offline worker = %d, want 409 (body %s)",
					resp.StatusCode, readBody(t, resp))
			}
		},
	},
	{
		key: "POST /v1/update/apply",
		// The body names a worker, which is what makes the request need worker.upgrade as
		// well as update.apply. The worker is not configured, so the request stops at the
		// id AFTER both permissions were decided — no download, no apply.
		req: func(e *authzCaseEnv) caseReq {
			return caseReq{http.MethodPost, "/v1/update/apply",
				updateApplyRequest{Workers: []string{"w-unknown-to-this-deployment"}}}
		},
		wantOwn: func(t *testing.T, resp *http.Response, _ *authzCaseEnv) {
			t.Helper()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("an update naming an unknown worker = %d, want 400 (body %s)",
					resp.StatusCode, readBody(t, resp))
			}
		},
	},
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
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
		// The worker the T1.5h upgrade case targets: a configured id walks past the route's
		// target lookup to the permission its body decides (see adminGroupConfig).
		Workers: map[string]config.WorkerAuthConfig{adminCaseWorkerID: {Token: "tok-case-worker"}},
	}, func(cfg *config.Config) {
		// The comment case of the job domain needs an agent a mention can dispatch: the fixture
		// project only allows exec, whose argv belongs to the caller rather than to a comment.
		p := cfg.Projects["self"]
		p.AllowedAgents = append(p.AllowedAgents, authzCaseMentionAgent)
		cfg.Projects["self"] = p
		cfg.Agents = map[string]config.AgentConfig{
			authzCaseMentionAgent: {
				Type: agent.TypeCLIAgent, Command: testcmd.Path(t),
				Args: []string{"argv", "{{prompt}}"},
			},
		}
	})
	if s.rootAdmin("alice") || s.rootAdmin("bob") {
		t.Fatal("the harness deployment must have no root administrator")
	}
	// The transfer routes of plan T1.5g read their rows through the manager, and the harness
	// server wires none (a bare /v1/xfer answers 503), so the domain's cases mount one.
	setTransferManager(t, s)
	// The same for plan T1.5h: its two InHandler routes read the worker upgrade staging area
	// and the release/apply seams before they reach their second permission.
	setAdminDomainSeams(t, s)

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
				s.setDBGrants(nil)
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
				for n, extra := range tc.extraOthers {
					xr, xdecided := serveAuthzCase(t, s, "tok-alice", extra(other))
					if xr.StatusCode != http.StatusForbidden {
						t.Fatalf("extra request %d answered %d, want 403 (body %s)", n, xr.StatusCode, readBody(t, xr))
					}
					assertNamesDeclaredPermission(t, readBody(t, xr), decl.user.perms)
					assertDecidedOnly(t, xdecided, decl.user.perms, "someone else's resource (extra)")
					decided = append(decided, xdecided...)
				}

				// --- its own resource: alice holds exactly the declared permissions --
				ownReq := tc.req
				if tc.reqOwn != nil {
					ownReq = tc.reqOwn
				}
				var ownDecided []decidedPerm
				if !tc.skipOwn {
					own := newAuthzCaseEnv(t, s, "alice", "alice", i)
					s.setDBGrants(grantsFor(decl.user.perms))
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
func grantsFor(perms []string) *grantScope {
	g := &grantScope{grants: map[string]string{}}
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
