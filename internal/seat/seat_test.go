package seat

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

type fakeProjects map[string][2]string

func (f fakeProjects) Paths(k string) (string, string, bool) { v, ok := f[k]; return v[0], v[1], ok }

type fakeAgents map[string]config.AgentConfig

func (f fakeAgents) Get(k string) (config.AgentConfig, bool) { v, ok := f[k]; return v, ok }

type fakeWaiting map[string]string

func (f fakeWaiting) WaitingSubjects(_ string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, r := range refs {
		if c, ok := f[r]; ok {
			out[r] = c
		}
	}
	return out, nil
}

type fakeRestarter struct {
	calls []RestartRequest
	newID string
}

func (f *fakeRestarter) RestartACP(_ context.Context, r RestartRequest) (string, error) {
	f.calls = append(f.calls, r)
	return f.newID, nil
}

type env struct {
	st  *jobstore.Store
	svc *Service
	now time.Time
	rs  *fakeRestarter
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, now: time.Now(), rs: &fakeRestarter{newID: "job-new"}}
	st.SetClock(func() time.Time { return e.now })
	e.svc = New(st, Deps{
		Projects: fakeProjects{"proj": {`D:\work\proj`, "/d/work/proj"}, "other": {"/o", "/o"}},
		Agents: fakeAgents{
			"claude": {Type: agent.TypeCLIAgent, Command: "claude", GlobalArgs: []string{"--dangerously-skip-permissions"},
				SessionResumeInteractive: []string{"--resume", "{{session_id}}"}},
			"acp": {Type: agent.TypeACPAgent, Command: "acp-agent"},
		},
		Waiting:   fakeWaiting{},
		Restarter: e.rs,
		Now:       func() time.Time { return e.now },
	})
	return e
}

func (e *env) session(t *testing.T, id, cwd string) {
	t.Helper()
	if _, err := e.st.UpsertAgentSession(jobstore.AgentSession{SessionID: id, Agent: "claude", ProjectKey: "proj", Cwd: cwd, Runner: "server", CallerID: "u1"}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) seat(t *testing.T, name, rel string) jobstore.Seat {
	t.Helper()
	x, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: name, RelCwd: rel})
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func ids(xs []jobstore.Seat) []string {
	var out []string
	for _, x := range xs {
		out = append(out, x.Name)
	}
	slices.Sort(out)
	return out
}

func TestCreateNormalizesDirectory(t *testing.T) {
	e := newEnv(t)
	for in, want := range map[string]string{"": "", ".": "", "sub/dir": "sub/dir", `D:\work\proj\sub`: "sub", "/d/work/proj/a/b": "a/b"} {
		x, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "n-" + in, RelCwd: in})
		if err != nil || x.RelCwd != want {
			t.Fatalf("rel %q -> %q err %v, want %q", in, x.RelCwd, err, want)
		}
	}
	if _, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "bad", RelCwd: "/elsewhere"}); !errors.Is(err, jobstore.ErrSeatInvalid) {
		t.Fatalf("outside the project: %v", err)
	}
	if _, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "bad2", RelCwd: "../x"}); !errors.Is(err, jobstore.ErrSeatInvalid) {
		t.Fatalf("climbing out: %v", err)
	}
}

func TestCandidatesRules(t *testing.T) {
	e := newEnv(t)
	e.seat(t, "root", "")
	sub := e.seat(t, "sub", "pkg/api")
	e.seat(t, "deeper", "pkg/api/v2")
	e.session(t, "s-host", `D:\work\proj\pkg\api\handlers`)
	e.session(t, "s-cont", "/d/work/proj/pkg/api")

	got, err := e.svc.Candidates("s-host")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"root", "sub"}; !slices.Equal(ids(got), want) {
		t.Fatalf("host-spelled subdirectory: %v, want %v (deeper must not match)", ids(got), want)
	}
	got, _ = e.svc.Candidates("s-cont")
	if want := []string{"root", "sub"}; !slices.Equal(ids(got), want) {
		t.Fatalf("container-spelled directory: %v, want %v", ids(got), want)
	}

	// an offline session keeps its seat: not a candidate
	e.session(t, "s-old", "/d/work/proj/pkg/api")
	if _, err := e.st.BindSeat(sub.ID, "s-old", false, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetSessionState("s-old", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	got, _ = e.svc.Candidates("s-host")
	if slices.Contains(ids(got), "sub") {
		t.Fatalf("a seat whose session is merely offline must not be a candidate: %v", ids(got))
	}
	// ended for less than 7 days: still not a candidate; more: candidate
	if _, err := e.st.SetSessionState("s-old", jobstore.SessionEnded); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(6 * 24 * time.Hour)
	got, _ = e.svc.Candidates("s-host")
	if slices.Contains(ids(got), "sub") {
		t.Fatalf("ended 6 days ago: %v", ids(got))
	}
	e.now = e.now.Add(2 * 24 * time.Hour)
	got, _ = e.svc.Candidates("s-host")
	if !slices.Contains(ids(got), "sub") {
		t.Fatalf("ended more than 7 days ago must be a candidate: %v", ids(got))
	}
}

// TestOpenPlanOmitsGlobalArgsAndUsesTheSessionsOwnDirectory: the resume command is typed where the
// session says it works, in that machine's spelling — a container worker's session reports
// `/d/work/...`, not the host's `D:\work\...`. A vacant seat has no session to ask, so it keeps the
// host path.
func TestOpenPlanOmitsGlobalArgsAndUsesTheSessionsOwnDirectory(t *testing.T) {
	e := newEnv(t)
	x := e.seat(t, "dev", "pkg/api")
	e.session(t, "s1", `/d/work/proj/pkg/api`)
	if _, err := e.svc.Bind(x.ID, "s1", false, "user"); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.OpenPlan(x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Resumable || p.Runner != "server" {
		t.Fatalf("plan = %+v", p)
	}
	for _, cmd := range []string{p.Sh, p.PowerShell, p.NewSh, p.NewPowerShell} {
		if strings.Contains(cmd, "dangerously") {
			t.Fatalf("GlobalArgs leaked into %q", cmd)
		}
		if !strings.Contains(cmd, `/d/work/proj/pkg/api`) {
			t.Fatalf("the directory must be the one the session reports: %q", cmd)
		}
		if strings.Contains(cmd, `D:\work`) {
			t.Fatalf("the host spelling is useless on the session's machine: %q", cmd)
		}
	}
	if !strings.Contains(p.Sh, "claude --resume s1") || !strings.HasPrefix(p.PowerShell, "Set-Location ") {
		t.Fatalf("sh=%q ps=%q", p.Sh, p.PowerShell)
	}
	// A host-run session reports a host path, which is what its command uses.
	e.session(t, "s2", `D:\work\proj\pkg\api`)
	if _, err := e.svc.Bind(x.ID, "s2", true, "user"); err != nil {
		t.Fatal(err)
	}
	p, _ = e.svc.OpenPlan(x.ID)
	if p.Cwd != `D:\work\proj\pkg\api` || !strings.Contains(p.Sh, `claude --resume s2`) {
		t.Fatalf("a host session's own spelling must be used: cwd=%q sh=%q", p.Cwd, p.Sh)
	}
	// vacant: not resumable, a fresh-start command is still offered, in the seat's host directory
	y := e.seat(t, "idle", "pkg/api")
	if _, err := e.svc.Update(y.ID, jobstore.SeatPatch{Agent: strptr("claude")}); err != nil {
		t.Fatal(err)
	}
	vp, _ := e.svc.OpenPlan(y.ID)
	if vp.Resumable || vp.Reason != ReasonVacant || vp.NewSh == "" {
		t.Fatalf("vacant plan = %+v", vp)
	}
	if !strings.Contains(vp.NewSh, `D:\work\proj\pkg\api`) {
		t.Fatalf("a vacant seat has no session to ask, so the host path stands: %q", vp.NewSh)
	}
}

func strptr(s string) *string { return &s }

func TestOpenPlanRestartsEndedACPSession(t *testing.T) {
	e := newEnv(t)
	x, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: "weekly", Kind: jobstore.SeatKindACP, Agent: "acp", Purpose: "weekly report", Charter: "be brief"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertJob(jobstore.JobRecord{ID: "job-old", ProjectKey: "proj", Agent: "acp", Runner: "local", Status: job.StatusDone, SessionID: "acp-sess", Cwd: ".", ResultDir: "/r", RequestJSON: "{}", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertJob(jobstore.JobRecord{ID: "job-new", ProjectKey: "proj", Agent: "acp", Runner: "local", Status: job.StatusRunning, SessionID: "acp-sess", Cwd: ".", ResultDir: "/r", RequestJSON: "{}", StartedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Bind(x.ID, "job-old", false, "user"); err != nil {
		t.Fatal(err)
	}
	p, _ := e.svc.OpenPlan(x.ID)
	if !p.NeedsRestart || p.Restarted || len(e.rs.calls) != 0 {
		t.Fatalf("OpenPlan only reads, nothing is restarted: %+v", p)
	}
	if _, err := e.svc.Restart(context.Background(), "seat-none"); !errors.Is(err, jobstore.ErrSeatNotFound) {
		t.Fatalf("restart of an unknown seat: %v", err)
	}
	p, err = e.svc.Restart(context.Background(), x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Restarted || p.JobID != "job-new" || p.ThreadID != "s:acp-sess" {
		t.Fatalf("plan = %+v", p)
	}
	if len(e.rs.calls) != 1 || e.rs.calls[0].SessionID != "acp-sess" || e.rs.calls[0].FromJobID != "job-old" {
		t.Fatalf("the original session id must be carried over: %+v", e.rs.calls)
	}
	got, _ := e.svc.Get(x.ID)
	if got.CurrentRef != "job-new" {
		t.Fatalf("the seat follows the new job: %q", got.CurrentRef)
	}
}

func TestStatusesBatchWaitingAndUnread(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.seat(t, "a", ""), e.seat(t, "b", ""), e.seat(t, "c", "")
	e.session(t, "s-a", "/d/work/proj")
	e.session(t, "s-b", "/d/work/proj")
	for id, ref := range map[string]string{a.ID: "s-a", b.ID: "s-b"} {
		if _, err := e.svc.Bind(id, ref, false, "user"); err != nil {
			t.Fatal(err)
		}
	}
	e.svc.d.Waiting = fakeWaiting{"s-a": "needs_reply"}
	if _, err := e.st.SetSessionState("s-b", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	// the viewer looked at thread r:s-b after its last activity
	if err := e.st.UpsertWorkbenchThreadPref(jobstore.WorkbenchThreadPref{CallerID: "viewer", ThreadID: "r:s-b", SeenAt: e.now.Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	seats, _ := e.svc.List(jobstore.SeatFilter{CallerID: "u1"})
	sts, err := e.svc.Statuses("viewer", seats)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Status{}
	for _, s := range sts {
		by[s.Seat.Name] = s
	}
	if by["a"].Waiting != "needs_reply" || !by["a"].Unread || by["a"].State != StateRunning {
		t.Fatalf("a = %+v", by["a"])
	}
	if by["b"].State != StateOffline || by["b"].Unread || by["b"].Waiting != "" {
		t.Fatalf("b = %+v", by["b"])
	}
	if by["c"].State != StateNone || by["c"].ThreadID != "" {
		t.Fatalf("c = %+v", by["c"])
	}
	_ = c
}

func TestContinuationTextBudgetAndContent(t *testing.T) {
	e := newEnv(t)
	e.svc.d.Lang = func() i18n.Lang { return i18n.EN }
	x := e.seat(t, "dev", "")
	x, _ = e.svc.Update(x.ID, jobstore.SeatPatch{Purpose: strptr("review prs"), Charter: strptr(strings.Repeat("charter line\n", 800))})
	text, err := e.svc.ContinuationText(x)
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > continuationBudget+8 || !strings.Contains(text, "dev") || !strings.Contains(text, "review prs") {
		t.Fatalf("len=%d text=%q", len(text), text[:80])
	}
	short, _ := e.svc.Update(x.ID, jobstore.SeatPatch{Charter: strptr("short")})
	text, _ = e.svc.ContinuationText(short)
	if !strings.Contains(text, "short") || !strings.Contains(text, "new session") {
		t.Fatalf("text = %q", text)
	}
}
