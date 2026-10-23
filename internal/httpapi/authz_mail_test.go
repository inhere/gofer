package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// The mailbox routes (plan M3): the permission table of the design cell by cell, the origin of
// a letter, the error statuses and the caller ceiling.

const (
	mailTokAlice = "tok-m-alice"
	mailTokBob   = "tok-m-bob"
	mailTokCarol = "tok-m-carol" // ceiling: projects [p1]
	mailTokDave  = "tok-m-dave"  // a stranger to every fixture letter
)

type mailEnv struct {
	s *Server
	// sessions: sA of alice, sB of bob (topic "billing refactor"), sEnded of alice, sP2 of bob in p2.
	sA, sB, sEnded, sP2 string
	// jobs: jA of alice, jB of bob (live, mailable), jCredSrc is a job of alice whose source
	// session is sA and whose plan's supervisor session is sSup; jCredNone has no source session.
	jA, jB, jCredSrc, jCredNone string
	sSup                        string
	tokSrc, tokNone             string
}

func newMailEnv(t *testing.T) *mailEnv {
	t.Helper()
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: mailTokAlice},
			{ID: "bob", Token: mailTokBob},
			{ID: "carol", Token: mailTokCarol, Ceiling: &config.CallerCeiling{Projects: []string{"p1"}}},
			{ID: "dave", Token: mailTokDave},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
		Workers: map[string]config.WorkerAuthConfig{"w-mail": {Token: "tok-w-mail"}},
	}, func(cfg *config.Config) {
		for _, p := range []string{"p1", "p2"} {
			cfg.Projects[p] = config.ProjectConfig{HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true}
		}
	})
	e := &mailEnv{s: s, sA: "sess-alice", sB: "sess-bob", sEnded: "sess-alice-ended", sP2: "sess-bob-p2", sSup: "sess-sup",
		jA: "job-alice", jB: "job-bob", jCredSrc: "job-cred-src", jCredNone: "job-cred-none"}
	st := s.jobs.Meta()
	now := time.Now().Unix()
	for _, a := range []jobstore.AgentSession{
		{SessionID: e.sA, ProjectKey: "p1", CallerID: "alice", State: jobstore.SessionIdle},
		{SessionID: e.sB, ProjectKey: "p1", CallerID: "bob", State: jobstore.SessionIdle},
		{SessionID: e.sEnded, ProjectKey: "p1", CallerID: "alice", State: jobstore.SessionEnded, EndedAt: now},
		{SessionID: e.sP2, ProjectKey: "p2", CallerID: "bob", State: jobstore.SessionIdle},
		{SessionID: e.sSup, ProjectKey: "p1", CallerID: "alice", State: jobstore.SessionIdle},
	} {
		a.Agent, a.Runner, a.Cwd, a.StartedAt, a.LastSeenAt = "claude", "local", ".", now, now
		if _, err := st.UpsertAgentSession(a); err != nil {
			t.Fatalf("seed session %s: %v", a.SessionID, err)
		}
	}
	topic := "billing refactor"
	if _, ok, _, err := st.SetSessionTopic(e.sB, &topic, nil, jobstore.SessionTopicSelf); err != nil || !ok {
		t.Fatalf("seed topic: %v %v", ok, err)
	}
	if err := st.InsertPlan(jobstore.Plan{PlanID: "plan-m", Title: "p", Status: jobstore.PlanOpen, Owner: "alice", ProjectKey: "p1",
		SupervisorSessionID: e.sSup, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, j := range []jobstore.JobRecord{
		{ID: e.jA, CallerID: "alice"}, {ID: e.jB, CallerID: "bob"},
		{ID: e.jCredSrc, CallerID: "alice", SourceSessionID: e.sA, PlanID: "plan-m"},
		{ID: e.jCredNone, CallerID: "alice"},
	} {
		j.ProjectKey, j.Agent, j.Runner, j.Status, j.Cwd, j.StartedAt, j.UpdatedAt = "p1", "exec", "worker", job.StatusRunning, ".", now, now
		// `session: true`: an ACP session job is the only job that can take a letter.
		j.SessionStateJSON = `{"session":true}`
		if err := st.UpsertJob(j); err != nil {
			t.Fatalf("seed job %s: %v", j.ID, err)
		}
	}
	e.tokSrc = seedJobToken(t, s, e.jCredSrc, jobstore.JobCredentialMember, "")
	e.tokNone = seedJobToken(t, s, e.jCredNone, jobstore.JobCredentialMember, "")
	return e
}

// req sends a request with an optional session header.
func (e *mailEnv) req(t *testing.T, token, sid, method, path string, body any) (int, string) {
	t.Helper()
	var r *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	rq := httptest.NewRequest(method, path, r)
	rq.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if sid != "" {
		rq.Header.Set(sessionHeader, sid)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, rq)
	return rec.Code, rec.Body.String()
}

// grant sets the permissions every user-class caller holds (the database half).
func (e *mailEnv) grant(perms ...string) { e.s.setDBGrants(grantsFor(perms)) }

func decodeSent(t *testing.T, body string) mailSendView {
	t.Helper()
	var v mailSendView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

func wantPermission(t *testing.T, what string, status int, body, perm string) {
	t.Helper()
	if status != http.StatusForbidden || !strings.Contains(body, perm) {
		t.Errorf("%s = %d (%s), want 403 naming %s", what, status, body, perm)
	}
}

// TestMailSendPermissionCells: POST /v1/mail, cell by cell of the design table.
func TestMailSendPermissionCells(t *testing.T) {
	e := newMailEnv(t)
	send := func(tok, to string) (int, string) {
		return e.req(t, tok, "", "POST", "/v1/mail", map[string]any{"to": to, "body": "hello"})
	}

	e.grant()
	// Own session / own job / own box: no permission.
	for _, to := range []string{"session:" + e.sA, "job:" + e.jA, "caller", ""} {
		if status, body := send(mailTokAlice, to); status != http.StatusOK {
			t.Errorf("alice -> %q (own, no permission) = %d (%s), want 200", to, status, body)
		}
	}
	// Another caller's terminal session: session.reply.others (deny).
	status, body := send(mailTokAlice, "session:"+e.sB)
	wantPermission(t, "alice -> bob's session without the permission", status, body, authz.PermSessionReplyOthers)
	// Another caller's session job: job.manage.others (deny).
	status, body = send(mailTokAlice, "job:"+e.jB)
	wantPermission(t, "alice -> bob's job without the permission", status, body, authz.PermJobManageOthers)
	// The wrong .others permission does not open the other kind.
	e.grant(authz.PermJobManageOthers)
	status, body = send(mailTokAlice, "session:"+e.sB)
	wantPermission(t, "bob's session with only job.manage.others", status, body, authz.PermSessionReplyOthers)
	e.grant(authz.PermSessionReplyOthers)
	status, body = send(mailTokAlice, "job:"+e.jB)
	wantPermission(t, "bob's job with only session.reply.others", status, body, authz.PermJobManageOthers)

	// With the permissions both are delivered (pending, not held).
	e.grant(authz.PermSessionReplyOthers, authz.PermJobManageOthers)
	for _, to := range []string{"session:" + e.sB, "job:" + e.jB} {
		status, body := send(mailTokAlice, to)
		if status != http.StatusOK || decodeSent(t, body).Mail.State != jobstore.MailPending {
			t.Errorf("alice -> %s with the permission = %d (%s), want a pending letter", to, status, body)
		}
	}
	// A refused letter stored nothing.
	e.grant()
	before, _ := e.s.jobs.Meta().ListMail(jobstore.ListMailOpts{FromCaller: "alice", Limit: 500})
	send(mailTokAlice, "session:"+e.sB)
	after, _ := e.s.jobs.Meta().ListMail(jobstore.ListMailOpts{FromCaller: "alice", Limit: 500})
	if len(after) != len(before) {
		t.Errorf("a refused send stored %d letter(s)", len(after)-len(before))
	}
}

// TestMailSendToOneOffOrEndedJobIsRefused (gofer-0n6r): a job: address names a job that can never
// take a letter — a one-off run, or an ended job — and the send is refused with what to do
// instead, instead of a letter that waits for a delivery nobody will make.
func TestMailSendToOneOffOrEndedJobIsRefused(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	st := e.s.jobs.Meta()
	now := time.Now().Unix()
	for id, status := range map[string]string{"job-batch": job.StatusRunning, "job-finished": job.StatusDone} {
		if err := st.UpsertJob(jobstore.JobRecord{
			ID: id, CallerID: "alice", ProjectKey: "p1", Agent: "exec", Runner: "local", Status: status,
			Cwd: ".", StartedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"job-batch", "job-finished"} {
		status, body := e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{"to": "job:" + id, "body": "hi"})
		if status != http.StatusConflict || !strings.Contains(body, "job cannot receive mail") ||
			!strings.Contains(body, "gofer job comment") {
			t.Errorf("mail to %s = %d (%s), want 409 with the alternatives", id, status, body)
		}
	}
	// The session job of the same fixture is still mailable.
	if status, body := e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{"to": "job:" + e.jA, "body": "hi"}); status != http.StatusOK {
		t.Errorf("mail to a session job = %d (%s), want 200", status, body)
	}
}

// TestMailSearchAddressesJudgeTheResolvedRecipient: a topic: address is filtered by
// session.read.others (an invisible session is not even a candidate), and the unique hit is
// then judged like an explicit address.
func TestMailSearchAddressesJudgeTheResolvedRecipient(t *testing.T) {
	e := newMailEnv(t)
	send := func() (int, string) {
		return e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{"to": "topic:billing", "body": "hi"})
	}
	// Cannot read bob's sessions: the topic matches nothing visible, the letter is held with
	// no candidates (no disclosure).
	e.grant()
	status, body := send()
	v := decodeSent(t, body)
	if status != http.StatusOK || !v.Held || len(v.Candidates) != 0 {
		t.Errorf("invisible topic = %d %+v, want a held letter without candidates", status, v)
	}
	// Can read but not reply: the unique hit is bob's session -> 403 reply.others.
	e.grant(authz.PermSessionReadOthers)
	status, body = send()
	wantPermission(t, "unique topic hit without session.reply.others", status, body, authz.PermSessionReplyOthers)
	// Both: delivered.
	e.grant(authz.PermSessionReadOthers, authz.PermSessionReplyOthers)
	status, body = send()
	if v = decodeSent(t, body); status != http.StatusOK || v.Held || v.Mail.To.ID != e.sB {
		t.Errorf("topic hit with both permissions = %d %s, want delivery to %s", status, body, e.sB)
	}
}

// TestMailJobCredentialTargets: a job credential mails only its job's source session, its
// plan's supervisor session and its caller (or only the caller when it has no source session).
func TestMailJobCredentialTargets(t *testing.T) {
	e := newMailEnv(t)
	e.grant(authz.PermSessionReplyOthers, authz.PermJobManageOthers) // a credential is not governed by permissions
	send := func(tok, to string) (int, string) {
		return e.req(t, tok, "", "POST", "/v1/mail", map[string]any{"to": to, "body": "report"})
	}
	for _, to := range []string{"session:" + e.sA, "session:" + e.sSup, "caller", ""} {
		status, body := send(e.tokSrc, to)
		if status != http.StatusOK {
			t.Errorf("job with a source session -> %q = %d (%s), want 200", to, status, body)
			continue
		}
		if v := decodeSent(t, body); v.Mail.From.Kind != jobstore.MailKindJob || v.Mail.From.ID != e.jCredSrc || v.Mail.From.Caller != "alice" {
			t.Errorf("origin of a job credential letter = %+v, want job %s of alice", v.Mail.From, e.jCredSrc)
		}
	}
	for _, to := range []string{"session:" + e.sB, "session:" + e.sP2, "job:" + e.jB, "topic:billing", "path:src"} {
		if status, body := send(e.tokSrc, to); status != http.StatusForbidden {
			t.Errorf("job with a source session -> %q = %d (%s), want 403", to, status, body)
		}
	}
	// Without a source session only the caller is allowed.
	if status, body := send(e.tokNone, "caller"); status != http.StatusOK {
		t.Errorf("job without a source session -> caller = %d (%s)", status, body)
	}
	for _, to := range []string{"session:" + e.sA, "session:" + e.sSup} {
		if status, body := send(e.tokNone, to); status != http.StatusForbidden {
			t.Errorf("job without a source session -> %q = %d (%s), want 403", to, status, body)
		}
	}
	// A worker token is refused.
	if status, _ := e.req(t, "tok-w-mail", "", "POST", "/v1/mail", map[string]any{"to": "caller", "body": "x"}); status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Errorf("worker token send = %d, want it refused", status)
	}
}

// mailFixture stores letters between known parties and returns their ids.
func (e *mailEnv) letter(t *testing.T, in jobstore.MailInput) int64 {
	t.Helper()
	if in.Body == "" {
		in.Body = "body"
	}
	m, err := e.s.jobs.Meta().InsertMail(in)
	if err != nil {
		t.Fatalf("insert mail: %v", err)
	}
	return m.ID
}

// TestMailReadReplyAckCancelCells: GET / reply / ack / cancel on one letter, cell by cell.
func TestMailReadReplyAckCancelCells(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	toSession := func() int64 { // alice's session mail from bob (sender bob, recipient alice's session)
		return e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
			ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA})
	}
	toJob := func() int64 { // bob's job mail from bob's caller... recipient: bob's job
		return e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
			ToAddress: "job:" + e.jB, ToKind: jobstore.MailKindJob, ToID: e.jB, JobID: e.jB})
	}
	id := func(n int64) string { return fmt.Sprint(n) }

	// Cells: [route, recipient-side caller (alice for session mail), sender (bob), stranger (carol? no -> use a third: bob for alice's job?)]
	ms := toSession()
	// recipient side (alice) and sender (bob) need nothing.
	for _, tok := range []string{mailTokAlice, mailTokBob} {
		if status, body := e.req(t, tok, "", "GET", "/v1/mail/"+id(ms), nil); status != http.StatusOK {
			t.Errorf("related caller GET = %d (%s)", status, body)
		}
	}
	// An unrelated caller: session mail -> session.reply.others (deny), job mail -> job.read.others.
	stranger := mailTokDave
	status, body := e.req(t, stranger, "", "GET", "/v1/mail/"+id(ms), nil)
	wantPermission(t, "stranger GET of a session letter", status, body, authz.PermSessionReplyOthers)
	mj := toJob()
	status, body = e.req(t, stranger, "", "GET", "/v1/mail/"+id(mj), nil)
	wantPermission(t, "stranger GET of a job letter", status, body, authz.PermJobReadOthers)
	for _, route := range []string{"reply", "ack", "cancel"} {
		status, body = e.req(t, stranger, "", "POST", "/v1/mail/"+id(ms)+"/"+route, map[string]any{"body": "x"})
		wantPermission(t, "stranger "+route+" of a session letter", status, body, authz.PermSessionReplyOthers)
		status, body = e.req(t, stranger, "", "POST", "/v1/mail/"+id(mj)+"/"+route, map[string]any{"body": "x"})
		wantPermission(t, "stranger "+route+" of a job letter", status, body, authz.PermJobManageOthers)
	}
	// With the matching permission the stranger reads / acts.
	e.grant(authz.PermSessionReplyOthers)
	if status, body = e.req(t, stranger, "", "GET", "/v1/mail/"+id(ms), nil); status != http.StatusOK {
		t.Errorf("stranger with session.reply.others GET = %d (%s)", status, body)
	}
	e.grant(authz.PermJobReadOthers)
	if status, body = e.req(t, stranger, "", "GET", "/v1/mail/"+id(mj), nil); status != http.StatusOK {
		t.Errorf("stranger with job.read.others GET = %d (%s)", status, body)
	}
	e.grant()

	// ack: the recipient side marks it read; the sender only gets it back unchanged.
	status, body = e.req(t, mailTokBob, "", "POST", "/v1/mail/"+id(ms)+"/ack", nil)
	var v mailView
	_ = json.Unmarshal([]byte(body), &v)
	if status != http.StatusOK || v.State != jobstore.MailPending || v.ReadAt != 0 {
		t.Errorf("sender ack = %d %+v, want the letter unchanged", status, v)
	}
	status, body = e.req(t, mailTokAlice, "", "POST", "/v1/mail/"+id(ms)+"/ack", nil)
	_ = json.Unmarshal([]byte(body), &v)
	if status != http.StatusOK || v.State != jobstore.MailDelivered || v.ReadAt == 0 {
		t.Errorf("recipient ack = %d %+v, want delivered and read", status, v)
	}
	// reply: the recipient answers; it goes back to the sender (bob's caller box -> held).
	status, body = e.req(t, mailTokAlice, "", "POST", "/v1/mail/"+id(ms)+"/reply", map[string]any{"body": "answer"})
	sent := decodeSent(t, body)
	if status != http.StatusOK || sent.Mail.ReplyTo != ms || sent.Mail.ThreadID == "" || !sent.Held {
		t.Errorf("reply = %d %s, want an answer in the thread (held in the sender's caller box)", status, body)
	}
	// cancel: a delivered letter cannot be withdrawn (409); a pending one can.
	if status, body = e.req(t, mailTokBob, "", "POST", "/v1/mail/"+id(ms)+"/cancel", nil); status != http.StatusConflict {
		t.Errorf("cancel of a delivered letter = %d (%s), want 409", status, body)
	}
	pending := toSession()
	if status, body = e.req(t, mailTokBob, "", "POST", "/v1/mail/"+id(pending)+"/cancel", nil); status != http.StatusOK {
		t.Errorf("cancel of a pending letter = %d (%s), want 200", status, body)
	}
}

// TestMailJobCredentialSeesOnlyItsJobsLetters: reads, answers and withdrawals of a job
// credential are limited to the letters of its own job; readdress is closed.
func TestMailJobCredentialSeesOnlyItsJobsLetters(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	mine := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "alice",
		ToAddress: "job:" + e.jCredSrc, ToKind: jobstore.MailKindJob, ToID: e.jCredSrc, JobID: e.jCredSrc})
	other := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "alice",
		ToAddress: "job:" + e.jA, ToKind: jobstore.MailKindJob, ToID: e.jA, JobID: e.jA})
	if status, body := e.req(t, e.tokSrc, "", "GET", fmt.Sprintf("/v1/mail/%d", mine), nil); status != http.StatusOK {
		t.Errorf("job reads its own letter = %d (%s)", status, body)
	}
	for _, route := range []struct{ m, p string }{{"GET", ""}, {"POST", "/reply"}, {"POST", "/ack"}, {"POST", "/cancel"}} {
		status, body := e.req(t, e.tokSrc, "", route.m, fmt.Sprintf("/v1/mail/%d%s", other, route.p), map[string]any{"body": "x"})
		if status != http.StatusForbidden {
			t.Errorf("job %s %s of another job's letter = %d (%s), want 403", route.m, route.p, status, body)
		}
	}
	// Reading its own letter as the recipient acknowledges it.
	_, body := e.req(t, e.tokSrc, "", "POST", fmt.Sprintf("/v1/mail/%d/ack", mine), nil)
	if !strings.Contains(body, `"state":"delivered"`) {
		t.Errorf("a job acknowledging its own letter: %s", body)
	}
	if status, _ := e.req(t, e.tokSrc, "", "POST", fmt.Sprintf("/v1/mail/%d/readdress", mine), map[string]any{"to": "caller"}); status != http.StatusForbidden {
		t.Errorf("readdress by a job credential = %d, want 403", status)
	}
	// The listing is narrowed to the job's letters.
	_, list := e.req(t, e.tokSrc, "", "GET", "/v1/mail", nil)
	if !strings.Contains(list, fmt.Sprintf(`"id":%d,`, mine)) || strings.Contains(list, fmt.Sprintf(`"id":%d,`, other)) {
		t.Errorf("job listing = %s, want its own letter only", list)
	}
}

// TestMailListFilter: GET /v1/mail is the filter form — own letters need nothing, another
// caller's appear only with the recipient type's .others permission.
func TestMailListFilter(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	own := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "alice", ToAddress: "session:" + e.sB, ToKind: jobstore.MailKindSession, ToID: e.sB})
	incoming := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob", ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA})
	others := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "dave", ToAddress: "session:" + e.sB, ToKind: jobstore.MailKindSession, ToID: e.sB})
	has := func(body string, id int64) bool { return strings.Contains(body, fmt.Sprintf(`"id":%d,`, id)) }

	_, body := e.req(t, mailTokAlice, "", "GET", "/v1/mail", nil)
	if !has(body, own) || !has(body, incoming) || has(body, others) {
		t.Errorf("alice's listing without permissions = %s, want her own sent + received only", body)
	}
	_, body = e.req(t, mailTokAlice, "", "GET", "/v1/mail?box=in", nil)
	if has(body, own) || !has(body, incoming) {
		t.Errorf("box=in = %s, want the received letter only", body)
	}
	_, body = e.req(t, mailTokAlice, "", "GET", "/v1/mail?box=out", nil)
	if !has(body, own) || has(body, incoming) {
		t.Errorf("box=out = %s, want the sent letter only", body)
	}
	e.grant(authz.PermSessionReplyOthers)
	_, body = e.req(t, mailTokAlice, "", "GET", "/v1/mail", nil)
	if !has(body, others) {
		t.Errorf("listing with session.reply.others = %s, want bob's session letter too", body)
	}
	if status, _ := e.req(t, mailTokAlice, "", "GET", "/v1/mail?box=sideways", nil); status != http.StatusBadRequest {
		t.Errorf("invalid box = %d, want 400", status)
	}
	_, body = e.req(t, mailTokAlice, "", "GET", "/v1/mail?state=held", nil)
	if has(body, incoming) {
		t.Errorf("state filter = %s, want pending letters out", body)
	}
}

// TestMailReaddress: a held letter is handed to a new address (judged against the NEW
// recipient); a letter that is not held cannot be.
func TestMailReaddress(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	status, body := e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{"to": "topic:nothing-matches", "body": "lost letter"})
	held := decodeSent(t, body)
	if status != http.StatusOK || !held.Held {
		t.Fatalf("setup: %d %s", status, body)
	}
	id := held.Mail.ID
	path := fmt.Sprintf("/v1/mail/%d/readdress", id)
	status, body = e.req(t, mailTokAlice, "", "POST", path, map[string]any{"to": "session:" + e.sB})
	wantPermission(t, "readdress to another caller's session", status, body, authz.PermSessionReplyOthers)
	status, body = e.req(t, mailTokAlice, "", "POST", path, map[string]any{"to": "session:" + e.sA})
	moved := decodeSent(t, body)
	if status != http.StatusOK || moved.Held || moved.Mail.To.ID != e.sA || moved.Mail.ThreadID != held.Mail.ThreadID || moved.Mail.Body != "lost letter" {
		t.Errorf("readdress to an own session = %d %s, want a delivered copy in the same thread", status, body)
	}
	old, _, _ := e.s.jobs.Meta().GetMail(id)
	if old.State != jobstore.MailCancelled {
		t.Errorf("the held original is %s, want cancelled", old.State)
	}
	if status, body = e.req(t, mailTokAlice, "", "POST", path, map[string]any{"to": "caller"}); status != http.StatusConflict {
		t.Errorf("readdress of a cancelled letter = %d (%s), want 409", status, body)
	}
}

// TestMailOriginDerivation: the session header makes a letter come from that session after the
// server checked it exists, is live and is the caller's own; without it the letter is from the
// caller.
func TestMailOriginDerivation(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	send := func(sid string) (int, string) {
		return e.req(t, mailTokAlice, sid, "POST", "/v1/mail", map[string]any{"to": "caller", "body": "x"})
	}
	status, body := send("")
	if v := decodeSent(t, body); status != http.StatusOK || v.Mail.From.Kind != jobstore.MailKindCaller || v.Mail.From.Caller != "alice" || v.Mail.From.ID != "" {
		t.Errorf("no session header: %d %+v, want a caller origin", status, v.Mail.From)
	}
	status, body = send(e.sA)
	if v := decodeSent(t, body); status != http.StatusOK || v.Mail.From.Kind != jobstore.MailKindSession || v.Mail.From.ID != e.sA || v.Mail.From.Caller != "alice" {
		t.Errorf("own session header: %d %+v, want a session origin", status, v.Mail.From)
	}
	for sid, want := range map[string]int{"sess-nobody": http.StatusNotFound, e.sEnded: http.StatusConflict, e.sB: http.StatusForbidden} {
		if status, body = send(sid); status != want {
			t.Errorf("session header %q = %d (%s), want %d", sid, status, body, want)
		}
	}
}

// TestMailErrorStatuses: the mailbox's errors map to 429 / 413 / 400 / 404.
func TestMailErrorStatuses(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	post := func(body map[string]any) int {
		status, _ := e.req(t, mailTokBob, "", "POST", "/v1/mail", body)
		return status
	}
	if got := post(map[string]any{"to": "bogus:x", "body": "x"}); got != http.StatusBadRequest {
		t.Errorf("unknown address scheme = %d, want 400", got)
	}
	if got := post(map[string]any{"to": "agent:foo", "body": "x"}); got != http.StatusBadRequest {
		t.Errorf("agent: address = %d, want 400", got)
	}
	if got := post(map[string]any{"to": "caller", "body": "  "}); got != http.StatusBadRequest {
		t.Errorf("empty body = %d, want 400", got)
	}
	if got := post(map[string]any{"to": "caller", "body": strings.Repeat("x", 40*1024)}); got != http.StatusRequestEntityTooLarge {
		t.Errorf("40KB body = %d, want 413", got)
	}
	if status, _ := e.req(t, mailTokBob, "", "GET", "/v1/mail/999999", nil); status != http.StatusNotFound {
		t.Errorf("unknown letter = %d, want 404", status)
	}
	if status, _ := e.req(t, mailTokBob, "", "POST", "/v1/mail/999999/reply", map[string]any{"body": "x"}); status != http.StatusNotFound {
		t.Errorf("reply to an unknown letter = %d, want 404", status)
	}
	if status, _ := e.req(t, mailTokBob, "", "GET", "/v1/mail/abc", nil); status != http.StatusBadRequest {
		t.Errorf("non-numeric id = %d, want 400", status)
	}
	limited := 0
	for i := 0; i < 12; i++ {
		if post(map[string]any{"to": "caller", "body": "spam"}) == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("12 letters in a minute were never rate limited (429)")
	}
}

// TestMailCeiling: a caller restricted to p1 mails sessions of p1, never p2 (404), and sees only
// letters with an end inside its projects.
func TestMailCeiling(t *testing.T) {
	e := newMailEnv(t)
	e.grant(authz.PermSessionReplyOthers, authz.PermSessionReadOthers, authz.PermJobManageOthers)
	send := func(to string) (int, string) {
		return e.req(t, mailTokCarol, "", "POST", "/v1/mail", map[string]any{"to": to, "body": "x"})
	}
	if status, body := send("session:" + e.sB); status != http.StatusOK {
		t.Errorf("restricted caller -> p1 session = %d (%s), want 200", status, body)
	}
	for _, to := range []string{"session:" + e.sP2} {
		status, body := send(to)
		if status != http.StatusNotFound {
			t.Errorf("restricted caller -> %s = %d (%s), want 404", to, status, body)
		}
		notFoundShape(t, body)
	}
	if status, _ := e.req(t, mailTokCarol, "", "POST", "/v1/mail", map[string]any{"to": "caller", "body": "x", "project": "p2"}); status != http.StatusNotFound {
		t.Errorf("a named project outside the ceiling = %d, want 404", status)
	}
	inM := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob", ToAddress: "session:" + e.sB, ToKind: jobstore.MailKindSession, ToID: e.sB})
	outM := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob", ToAddress: "session:" + e.sP2, ToKind: jobstore.MailKindSession, ToID: e.sP2})
	if status, _ := e.req(t, mailTokCarol, "", "GET", fmt.Sprintf("/v1/mail/%d", inM), nil); status != http.StatusOK {
		t.Errorf("a p1 letter = %d, want 200", status)
	}
	status, body := e.req(t, mailTokCarol, "", "GET", fmt.Sprintf("/v1/mail/%d", outM), nil)
	if status != http.StatusNotFound {
		t.Errorf("a p2 letter = %d (%s), want 404", status, body)
	}
	_, list := e.req(t, mailTokCarol, "", "GET", "/v1/mail", nil)
	if strings.Contains(list, fmt.Sprintf(`"id":%d,`, outM)) || !strings.Contains(list, fmt.Sprintf(`"id":%d,`, inM)) {
		t.Errorf("restricted listing = %s, want the p1 letter only", list)
	}
}

// TestServerUsesTheInjectedMailbox: SetMail replaces the server's default service, so what
// serve injects (core's instance) is the one the routes send through — its limits apply to
// HTTP sends, and Mail() hands the same pointer back.
func TestServerUsesTheInjectedMailbox(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	shared := mail.NewService(e.s.jobs.Meta(), e.s.relay)
	shared.Limits.PerMinute = 2
	e.s.SetMail(shared)
	if e.s.Mail() != shared {
		t.Fatal("Server.Mail() is not the injected service")
	}
	e.s.SetMail(nil)
	if e.s.Mail() != shared {
		t.Fatal("SetMail(nil) must keep the current service")
	}
	var statuses []int
	for i := 0; i < 3; i++ {
		status, _ := e.req(t, mailTokBob, "", "POST", "/v1/mail", map[string]any{"to": "caller", "body": "x"})
		statuses = append(statuses, status)
	}
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusOK || statuses[2] != http.StatusTooManyRequests {
		t.Fatalf("statuses = %v, want the injected service's 2-a-minute limit to hold over HTTP", statuses)
	}
}

// TestMailSessionAddressPrefix: a session:<prefix> address resolves like --session: a unique
// prefix delivers to the full id, several matches are 409 naming full ids, none is 404.
func TestMailSessionAddressPrefix(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	send := func(to string) (int, string) {
		return e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{"to": to, "body": "hi"})
	}
	status, body := send("session:sess-alice-e")
	if status != http.StatusOK {
		t.Fatalf("unique prefix = %d (%s)", status, body)
	}
	if v := decodeSent(t, body); v.Mail.To.ID != e.sEnded && v.Mail.To.ID != "" {
		t.Errorf("recipient = %+v, want the ended session's full id or a hold", v.Mail.To)
	}
	status, body = send("session:sess-ali")
	if status != http.StatusConflict || !strings.Contains(body, e.sA) || !strings.Contains(body, e.sEnded) {
		t.Errorf("ambiguous prefix = %d (%s), want 409 naming both full ids", status, body)
	}
	if status, body = send("session:nothing-like-it"); status != http.StatusNotFound {
		t.Errorf("unknown prefix = %d (%s), want 404", status, body)
	}
}
