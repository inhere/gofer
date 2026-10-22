package mail

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// The `agent:` address (设计 2026-10-22 mail-agent-address「D1」「D3」): parsing, the configured
// names, the resident session's start / reuse / rotation / resume, the global ceiling, the
// events and the reply path. The job work is a fake ResidentHost, so no session runs.

type fakeHost struct {
	started []ResidentSpec
	jobs    map[string]*ResidentJob
	ended   []string
	next    int
	fail    error
}

func newFakeHost() *fakeHost { return &fakeHost{jobs: map[string]*ResidentJob{}} }

func (h *fakeHost) StartSession(sp ResidentSpec) (string, error) {
	if h.fail != nil {
		return "", h.fail
	}
	h.next++
	id := fmt.Sprintf("job-%d", h.next)
	h.started = append(h.started, sp)
	h.jobs[id] = &ResidentJob{
		ID: id, Agent: sp.Agent, SessionID: "sess-" + id, Status: "awaiting_input",
		Live: true, AwaitingInput: true,
	}
	return id, nil
}

func (h *fakeHost) Job(id string) (ResidentJob, bool) {
	j, ok := h.jobs[id]
	if !ok {
		return ResidentJob{}, false
	}
	return *j, true
}

func (h *fakeHost) End(id string) error {
	if j, ok := h.jobs[id]; ok {
		j.Live = false
		j.Status = "ended"
		h.ended = append(h.ended, id)
	}
	return nil
}

// dies marks a resident job terminal without going through End (a crash / idle timeout).
func (h *fakeHost) dies(id string) {
	if j, ok := h.jobs[id]; ok {
		j.Live = false
		j.Status = "failed"
	}
}

type fakeSayer struct {
	said []string
	fail error
}

func (s *fakeSayer) SaySession(id, message string) error {
	if s.fail != nil {
		return s.fail
	}
	s.said = append(s.said, id+"\x00"+message)
	return nil
}

type agentFixture struct {
	*fixture
	host  *fakeHost
	sayer *fakeSayer
	dl    *Deliverer
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	f := newFixture(t)
	f.svc.SetResidents(ResidentsConfig{Max: 6, Agents: map[string]ResidentAgentSpec{
		"suga":  {Name: "suga", Agent: "suga-acp", Idle: 120 * time.Minute, MaxLetters: 50, MaxHours: 24},
		"codex": {Name: "codex", Agent: "codex-acp", Idle: 30 * time.Minute, MaxLetters: 2, MaxHours: 24},
	}})
	af := &agentFixture{fixture: f, host: newFakeHost(), sayer: &fakeSayer{}}
	f.svc.SetResidentHost(af.host)
	af.dl = NewDeliverer(DelivererOptions{Svc: f.svc, Jobs: af.sayer, Now: func() time.Time { return f.now }})
	return af
}

func (f *agentFixture) send(t *testing.T, to, project string) SendResult {
	t.Helper()
	out, err := f.svc.Send(SendInput{To: to, Body: "please handle this", Origin: sender(), Project: project})
	if err != nil {
		t.Fatalf("Send(%q): %v", to, err)
	}
	return out
}

func TestAgentAddressParsing(t *testing.T) {
	for _, tc := range []struct {
		raw           string
		name, project string
		err           error
	}{
		{raw: "agent:suga", name: "suga"},
		{raw: "agent:suga@gofer", name: "suga", project: "gofer"},
		{raw: "  Agent:SUGA@gofer  ", name: "SUGA", project: "gofer"},
		{raw: "agent:", err: ErrInvalidAddress},
		{raw: "agent:@gofer", err: ErrInvalidAddress},
	} {
		addr, err := ParseAddress(tc.raw)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Fatalf("ParseAddress(%q) err=%v, want %v", tc.raw, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseAddress(%q): %v", tc.raw, err)
		}
		if addr.Scheme != SchemeAgent || addr.Value != tc.name || addr.Project != tc.project {
			t.Fatalf("ParseAddress(%q) = %+v", tc.raw, addr)
		}
	}
	// each:agent: keeps its broadcast meaning (it never starts a session).
	each, err := ParseAddress("each:agent:suga")
	if err != nil || !each.Each || each.Value != "suga" {
		t.Fatalf("ParseAddress(each:agent:suga) = %+v, %v", each, err)
	}
}

func TestAgentAddressNeedsAConfiguredName(t *testing.T) {
	f := newAgentFixture(t)
	_, err := f.svc.Send(SendInput{To: "agent:nobody", Body: "hi", Origin: sender(), Project: "gofer"})
	if !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("Send(agent:nobody) err=%v, want ErrUnknownAgent", err)
	}
	if !strings.Contains(err.Error(), "codex") || !strings.Contains(err.Error(), "suga") {
		t.Fatalf("the error must list the configured names: %v", err)
	}
	// The default project is the SENDER's; an explicit one wins.
	out := f.send(t, "agent:suga", "gofer")
	if out.Mail.ToKind != jobstore.MailKindAgent || out.Mail.ToID != jobstore.ResidentKey("suga", "gofer") {
		t.Fatalf("default project: to = %s:%s", out.Mail.ToKind, out.Mail.ToID)
	}
	out = f.send(t, "agent:suga@other-proj", "gofer")
	if out.Mail.ToID != jobstore.ResidentKey("suga", "other-proj") {
		t.Fatalf("@project: to_id = %q", out.Mail.ToID)
	}
}

func TestAgentFirstLetterStartsAResidentSession(t *testing.T) {
	f := newAgentFixture(t)
	out := f.send(t, "agent:suga", "gofer")
	if out.Held || out.Mail.State != jobstore.MailPending {
		t.Fatalf("the letter must wait pending (the start is async): %+v", out.Mail)
	}
	f.dl.Tick(t.Context())
	if len(f.host.started) != 1 {
		t.Fatalf("started = %d sessions, want 1", len(f.host.started))
	}
	sp := f.host.started[0]
	if sp.Project != "gofer" || sp.Agent != "suga-acp" || sp.IdleSec != 7200 {
		t.Fatalf("start spec = %+v", sp)
	}
	if !strings.Contains(sp.Prompt, "<"+TagLetter+">") || !strings.Contains(sp.Prompt, "agent:suga") {
		t.Fatalf("the opening prompt must explain the letters: %q", sp.Prompt)
	}
	// The session is not delivered into on the same sweep it was started in.
	if len(f.sayer.said) != 0 {
		t.Fatalf("said on the starting sweep: %v", f.sayer.said)
	}
	row, ok, err := f.store.GetResidentAgent("suga", "gofer")
	if err != nil || !ok || row.Status != jobstore.MailResidentLive || row.JobID == "" {
		t.Fatalf("resident row = %+v ok=%v err=%v", row, ok, err)
	}
	// The next sweep delivers it into the session (the awaiting_input → job say path).
	f.dl.Tick(t.Context())
	if len(f.sayer.said) != 1 || !strings.HasPrefix(f.sayer.said[0], row.JobID+"\x00") {
		t.Fatalf("said = %v, want one letter into %s", f.sayer.said, row.JobID)
	}
	if !strings.Contains(f.sayer.said[0], "<"+TagLetter) {
		t.Fatalf("the letter must travel in its tag: %q", f.sayer.said[0])
	}
	if row, _, _ = f.store.GetResidentAgent("suga", "gofer"); row.Letters != 1 {
		t.Fatalf("letters = %d, want 1", row.Letters)
	}
	m, _, _ := f.store.GetMail(out.Mail.ID)
	if m.State != jobstore.MailDelivered {
		t.Fatalf("letter state = %s, want delivered", m.State)
	}
}

func TestAgentSecondLetterReusesTheSession(t *testing.T) {
	f := newAgentFixture(t)
	f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	f.dl.Tick(t.Context())
	f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	if len(f.host.started) != 1 {
		t.Fatalf("a second letter started another session: %d", len(f.host.started))
	}
	if len(f.sayer.said) != 2 {
		t.Fatalf("said = %d letters, want 2", len(f.sayer.said))
	}
}

func TestAgentRotationStartsAFreshSession(t *testing.T) {
	f := newAgentFixture(t)
	f.send(t, "agent:codex", "gofer") // max_letters 2
	f.dl.Tick(t.Context())
	f.dl.Tick(t.Context())
	f.send(t, "agent:codex", "gofer")
	f.dl.Tick(t.Context())
	f.send(t, "agent:codex", "gofer")
	f.dl.Tick(t.Context())
	if len(f.host.started) != 2 {
		t.Fatalf("started = %d sessions, want 2 (rotation)", len(f.host.started))
	}
	if f.host.started[1].ResumeSessionID != "" {
		t.Fatalf("a rotation must start fresh, got resume %q", f.host.started[1].ResumeSessionID)
	}
	if len(f.host.ended) != 1 {
		t.Fatalf("the rotated session was not ended: %v", f.host.ended)
	}
	// The rotation is on the audit feed.
	ev, err := f.store.ListAuditEvents("mail_resident", jobstore.ResidentKey("codex", "gofer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) == 0 {
		t.Fatal("no mail_resident event was recorded")
	}
}

func TestAgentDeadSessionIsResumed(t *testing.T) {
	f := newAgentFixture(t)
	f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	f.dl.Tick(t.Context())
	first, _, _ := f.store.GetResidentAgent("suga", "gofer")
	f.host.dies(first.JobID)
	f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	if len(f.host.started) != 2 {
		t.Fatalf("started = %d, want 2", len(f.host.started))
	}
	if got := f.host.started[1]; got.ResumeSessionID != "sess-"+first.JobID || got.ResumeFromJobID != first.JobID {
		t.Fatalf("the dead session was not carried over: %+v", got)
	}
}

func TestAgentCeilingParksTheLetter(t *testing.T) {
	f := newAgentFixture(t)
	f.svc.SetResidents(ResidentsConfig{Max: 1, Agents: map[string]ResidentAgentSpec{
		"suga":  {Name: "suga", Agent: "suga-acp", Idle: time.Hour},
		"codex": {Name: "codex", Agent: "codex-acp", Idle: time.Hour},
	}})
	f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	held := f.send(t, "agent:codex", "gofer")
	f.dl.Tick(t.Context())
	if len(f.host.started) != 1 {
		t.Fatalf("the ceiling did not stop a second session: %d", len(f.host.started))
	}
	m, _, _ := f.store.GetMail(held.Mail.ID)
	if m.State != jobstore.MailHeld || !strings.Contains(m.LastError, "agents_max_resident") {
		t.Fatalf("the over-ceiling letter = %s (%q), want held with the reason", m.State, m.LastError)
	}
}

func TestAgentStartFailureIsRecordedAndRetriedLater(t *testing.T) {
	f := newAgentFixture(t)
	f.host.fail = errors.New("boom")
	f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	row, ok, err := f.store.GetResidentAgent("suga", "gofer")
	if err != nil || !ok || row.Status != jobstore.MailResidentFailed || !strings.Contains(row.LastError, "boom") {
		t.Fatalf("failed row = %+v ok=%v err=%v", row, ok, err)
	}
	// Inside the retry window nothing is retried; after it, the start runs again.
	f.dl.Tick(t.Context())
	if len(f.host.started) != 0 {
		t.Fatalf("a failed start was retried at once")
	}
	f.host.fail = nil
	f.now = f.now.Add(residentStartRetry + time.Second)
	f.dl.Tick(t.Context())
	if len(f.host.started) != 1 {
		t.Fatalf("started = %d after the retry window, want 1", len(f.host.started))
	}
}

func TestAgentReplyStaysInTheThread(t *testing.T) {
	f := newAgentFixture(t)
	f.addSession(t, "sess-b", "suga", "", jobstore.SessionIdle)
	out := f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	f.dl.Tick(t.Context())
	// The resident session answers from its own job credential: the reply goes back to the
	// letter's sender, in the same thread.
	jobCred := Origin{Kind: jobstore.MailKindJob, ID: out.Mail.ToID, Caller: "alice"}
	rep, err := f.svc.Reply(ReplyInput{ID: out.Mail.ID, Body: "done", Origin: jobCred})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if rep.Mail.ThreadID != out.Mail.ThreadID {
		t.Fatalf("thread = %q, want %q", rep.Mail.ThreadID, out.Mail.ThreadID)
	}
	if rep.Mail.ToKind != jobstore.MailKindSession || rep.Mail.ToID != "sess-b" {
		t.Fatalf("the reply goes back to the sender, got %s:%s", rep.Mail.ToKind, rep.Mail.ToID)
	}
}

func TestEndResidentByHand(t *testing.T) {
	f := newAgentFixture(t)
	f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	jobID, err := f.svc.EndResident("suga", "gofer", "")
	if err != nil {
		t.Fatalf("EndResident: %v", err)
	}
	if len(f.host.ended) != 1 || f.host.ended[0] != jobID {
		t.Fatalf("ended = %v, want %s", f.host.ended, jobID)
	}
	row, _, _ := f.store.GetResidentAgent("suga", "gofer")
	if row.Status != jobstore.MailResidentEnded {
		t.Fatalf("status = %s, want ended", row.Status)
	}
	if _, err := f.svc.EndResident("suga", "nowhere", ""); !errors.Is(err, ErrAgentNotResident) {
		t.Fatalf("EndResident(no row) err=%v, want ErrAgentNotResident", err)
	}
	// The listing shows the configured names and the session row.
	list, err := f.svc.ListResidents()
	if err != nil || len(list) != 1 || list[0].Name != "suga" || !list[0].Configured {
		t.Fatalf("ListResidents = %+v, %v", list, err)
	}
}

// TestAgentResidentSessionCarriesItsApproval (gofer-81b4): the agent's own approval gate
// (mail.agents.<名字>.approval) reaches the session the deliverer starts.
func TestAgentResidentSessionCarriesItsApproval(t *testing.T) {
	f := newAgentFixture(t)
	f.svc.SetResidents(ResidentsConfig{Max: 6, Agents: map[string]ResidentAgentSpec{
		"suga": {
			Name: "suga", Agent: "suga-acp", Idle: 120 * time.Minute, MaxLetters: 50, MaxHours: 24,
			Approval: &config.ApprovalConfig{Mode: config.ApprovalAsk},
		},
	}})
	f.send(t, "agent:suga", "gofer")
	f.dl.Tick(t.Context())
	if len(f.host.started) != 1 {
		t.Fatalf("started = %d sessions, want 1", len(f.host.started))
	}
	sp := f.host.started[0]
	if sp.Approval == nil || sp.Approval.Mode != config.ApprovalAsk {
		t.Fatalf("start spec approval = %+v, want the configured ask", sp.Approval)
	}
}
