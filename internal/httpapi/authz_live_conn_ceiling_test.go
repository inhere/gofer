package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// 调用方上限第二期：长连接复核与 /v1/ws 的范围（计划 E1 / E2，gofer-7798）。
//
//   - 一个已打开的长连接在复核时按调用方当前的上限重新判定：上限收紧后越界的 SSE / attach 断开，
//     只读上限断开写型连接（隧道、托管转发、写模式 attach），隧道这类没有项目的连接在调用方有上限
//     时断开；job 凭据按它的上限所有者判定，求不到所有者时失败关闭；
//   - 没有上限的调用方（默认）完全不变，这正是回归断言；
//   - 受限调用方的 ws 只收到范围内的 job 主题与范围化的 stats 快照；未受限连接与改动前一致。

// ceilingJobFor seeds one finished job whose ceiling owner is owner, i.e. the row a job
// credential's ceiling is resolved from (job.Service.CredentialCeilingOwner).
func ceilingJobFor(t *testing.T, s *Server, id, project, owner string) {
	t.Helper()
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: id, ProjectKey: project, Agent: "exec", Runner: "worker", Status: job.StatusDone,
		Cwd: ".", ResultDir: t.TempDir(), StartedAt: now, EndedAt: now, UpdatedAt: now,
		CallerID: owner, OriginCaller: owner,
	}); err != nil {
		t.Fatalf("seed job %s: %v", id, err)
	}
}

// TestLiveConnCeilingRejudgesOpenConnections is the E1 acceptance case: the re-judgment of one open
// long connection against the caller's ceiling, kind by kind, with the unrestricted caller as the
// regression row.
func TestLiveConnCeilingRejudgesOpenConnections(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	w1 := seedCeilingWorld(t, s, "p1")
	w2 := seedCeilingWorld(t, s, "p2")
	// Job credentials: their ceiling is the one of the job's ceiling owner (决策 3).
	ceilingJobFor(t, s, "cred-lim", "p1", "lim")
	ceilingJobFor(t, s, "cred-plain", "p2", "plain")

	cases := []struct {
		name string
		conn liveConn
		want bool
	}{
		{"lim keeps an SSE on a job of its own project",
			liveConn{caller: "lim", callerKind: callerKindUser, projects: s.liveConnJobProjects(w1.jobID)}, true},
		{"lim loses an SSE on a job outside its projects",
			liveConn{caller: "lim", callerKind: callerKindUser, projects: s.liveConnJobProjects(w2.jobID)}, false},
		{"lim loses a tunnel (a connection whose resource names no project)",
			liveConn{caller: "lim", callerKind: callerKindUser, projects: liveConnProjectless, write: true}, false},
		{"ro loses a write-mode attach of its own project",
			liveConn{caller: "ro", callerKind: callerKindUser, projects: s.liveConnJobProjects(w1.jobID), write: true}, false},
		{"ro keeps a read-mode attach of its own project",
			liveConn{caller: "ro", callerKind: callerKindUser, projects: s.liveConnJobProjects(w1.jobID)}, true},
		{"plain keeps an SSE wherever the job is: it has no ceiling",
			liveConn{caller: "plain", callerKind: callerKindUser, projects: s.liveConnJobProjects(w2.jobID), write: true}, true},
		{"plain keeps a tunnel: it has no ceiling",
			liveConn{caller: "plain", callerKind: callerKindUser, projects: liveConnProjectless, write: true}, true},
		{"a job credential of lim keeps the job it streamed inside lim's ceiling",
			liveConn{caller: "cred-lim", callerKind: callerKindJob, projects: s.liveConnJobProjects("cred-lim")}, true},
		{"a job credential of lim loses a job outside its ceiling",
			liveConn{caller: "cred-lim", callerKind: callerKindJob, projects: s.liveConnJobProjects(w2.jobID)}, false},
		{"a job credential of plain is unrestricted",
			liveConn{caller: "cred-plain", callerKind: callerKindJob, projects: s.liveConnJobProjects(w2.jobID)}, true},
		{"a job credential whose job is gone fails closed",
			liveConn{caller: "cred-gone", callerKind: callerKindJob, projects: s.liveConnJobProjects(w1.jobID)}, false},
		{"a worker token is never judged by a ceiling",
			liveConn{caller: "w1", callerKind: callerKindWorker, projects: liveConnProjectless}, true},
	}
	for _, c := range cases {
		if got := s.liveConnStillAllowed(c.conn); got != c.want {
			t.Errorf("%s: allowed=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestTunnelDropsWhenCallerGainsACeiling is the integration half of E1: a tunnel that was opened
// while its caller was unrestricted ends as soon as a reload gives that caller a ceiling — the
// listener keeps nothing of a caller whose range the server can no longer name.
func TestTunnelDropsWhenCallerGainsACeiling(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice", CanTunnel: true},
		},
		Governance: config.GovernanceConfig{RequireTunnelCapability: true},
	})
	conn := startTunnel(t, s, "tok-alice", "")

	reloaded := config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "root", Token: "tok-root", CanAdmin: true},
			{ID: "alice", Token: "tok-alice", CanTunnel: true,
				Ceiling: &config.CallerCeiling{Projects: []string{"p1"}}},
		},
		Governance: config.GovernanceConfig{RequireTunnelCapability: true},
	}
	s.SetServerConfig(&reloaded)
	waitLiveConnClosed(t, s, conn, "a ceiling added by a reload must close the tunnel")
}

// readWSSnapTopic reads frames until the snapshot of topic arrives.
func readWSSnapTopic(t *testing.T, conn *websocket.Conn, topic string) map[string]any {
	t.Helper()
	var data map[string]any
	wait.Until(t, 5*time.Second, "the "+topic+" snapshot", func() bool {
		m := readWSFrame(t, conn)
		if m["t"] != "snap" || m["topic"] != topic {
			return false
		}
		data, _ = m["data"].(map[string]any)
		return data != nil
	})
	return data
}

// TestWSRestrictedCallerOnlySeesItsProjects covers E2: the job topic of a job outside the caller's
// ceiling is refused exactly like an unknown topic, the stats snapshot is computed for the
// caller's projects only (and carries no server-global numbers), and an unrestricted connection
// still gets the payload it always got.
func TestWSRestrictedCallerOnlySeesItsProjects(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	grantAll(s)
	w1 := seedCeilingWorld(t, s, "p1")
	w2 := seedCeilingWorld(t, s, "p2")

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	_, ticket := postWSTicket(t, s, ceilTokLim)
	lim, _, err := dialWS(t, srv, ticket, "")
	if err != nil {
		t.Fatalf("dial ws as lim: %v", err)
	}
	t.Cleanup(func() { lim.CloseNow() })
	if hello := readWSFrame(t, lim); hello["t"] != "hello" {
		t.Fatalf("want hello, got %v", hello)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 5*time.Second))
	defer cancel()
	sub := `{"t":"sub","topics":["job:` + w1.jobID + `","job:` + w2.jobID + `"]}`
	if err := lim.Write(ctx, websocket.MessageText, []byte(sub)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	var rejected []string
	wait.Until(t, 5*time.Second, "the out-of-scope job topic is refused", func() bool {
		m := readWSFrame(t, lim)
		if m["t"] != "error" {
			return false
		}
		for _, raw := range m["topics"].([]any) {
			rejected = append(rejected, raw.(string))
		}
		return true
	})
	if len(rejected) != 1 || rejected[0] != "job:"+w2.jobID {
		t.Fatalf("rejected topics = %v, want exactly [job:%s] (job:%s is lim's own job)", rejected, w2.jobID, w1.jobID)
	}

	// The scoped stats snapshot: lim's projects only, and the server-global blocks left out.
	if err := lim.Write(ctx, websocket.MessageText, []byte(`{"t":"sub","topics":["stats"]}`)); err != nil {
		t.Fatalf("subscribe stats: %v", err)
	}
	stats := readWSSnapTopic(t, lim, "stats")
	if got := stats["projects"]; got != float64(1) {
		t.Errorf("restricted stats projects = %v, want 1 (p1 only)", got)
	}
	jobsBlock, _ := stats["jobs"].(map[string]any)
	if jobsBlock == nil || jobsBlock["total"] != float64(1) {
		t.Errorf("restricted stats jobs = %v, want the single job of p1", stats["jobs"])
	}
	dbBlock, _ := stats["db"].(map[string]any)
	if dbBlock == nil || dbBlock["size_bytes"] != float64(0) || dbBlock["path"] != "" {
		t.Errorf("restricted stats db block = %v, want the omitted (zero) block", stats["db"])
	}
	if sessBlock, _ := stats["sessions"].(map[string]any); sessBlock == nil || len(sessBlock) == 0 {
		t.Errorf("restricted stats sessions block = %v, want it present but empty", stats["sessions"])
	}

	// The unrestricted caller keeps the payload it always had.
	_, pticket := postWSTicket(t, s, ceilTokPlain)
	plain, _, err := dialWS(t, srv, pticket, "")
	if err != nil {
		t.Fatalf("dial ws as plain: %v", err)
	}
	t.Cleanup(func() { plain.CloseNow() })
	if hello := readWSFrame(t, plain); hello["t"] != "hello" {
		t.Fatalf("want hello, got %v", hello)
	}
	if err := plain.Write(ctx, websocket.MessageText, []byte(`{"t":"sub","topics":["stats"]}`)); err != nil {
		t.Fatalf("subscribe stats as plain: %v", err)
	}
	openStats := readWSSnapTopic(t, plain, "stats")
	if got, want := openStats["projects"], float64(len(s.projects.List())); got != want {
		t.Errorf("unrestricted stats projects = %v, want %v (every project the server serves)", got, want)
	}
	openJobs, _ := openStats["jobs"].(map[string]any)
	if openJobs == nil || openJobs["total"] != float64(2) {
		t.Errorf("unrestricted stats jobs = %v, want both seeded jobs", openStats["jobs"])
	}
}

// TestPendingSnapshotIsScoped: the bell's snapshot of a restricted connection keeps only the rows
// of the projects the ceiling admits (E2). The decisions are the rows the current fixture can
// seed without a live job; interactions and approvals go through the same job-project judgment.
func TestPendingSnapshotIsScoped(t *testing.T) {
	t.Parallel()
	s := newCeilingServer(t)
	seedCeilingWorld(t, s, "p1")
	seedCeilingWorld(t, s, "p2")
	ceil := &config.CallerCeiling{Projects: []string{"p1"}}
	scope := func(p string) bool { return ceilingAllowsProject(ceil, p) }

	dropped, err := s.pendingSnapshotFor(scope)
	if err != nil {
		t.Fatalf("scoped pending snapshot: %v", err)
	}
	kept := dropped.(map[string]any)["decisions"].([]decisionView)
	if len(kept) != 1 || kept[0].PlanID != "plan-p1" {
		t.Fatalf("scoped decisions = %+v, want only the decision of plan-p1", kept)
	}

	// The regression side: without a scope nothing is filtered out.
	full, err := s.pendingSnapshot()
	if err != nil {
		t.Fatalf("unscoped pending snapshot: %v", err)
	}
	all := full.(map[string]any)["decisions"].([]decisionView)
	if len(all) <= len(kept) {
		t.Fatalf("unscoped decisions = %d rows, want more than the %d of p1", len(all), len(kept))
	}
}
