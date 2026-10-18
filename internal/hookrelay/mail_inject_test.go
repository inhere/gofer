package hookrelay

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// mailFake is the fake hub plus an in-memory mailbox with the real states
// (pending → delivering → delivered), so the tests can see what the hook left behind.
type mailFake struct {
	*fakeAPI
	mmu       sync.Mutex
	letters   []*fakeLetter
	failClaim error
	// failConfirm makes every confirmation fail (the at-most-once case).
	failConfirm error
	// webWins makes CompleteSessionMailTurn lose the race to a web answer.
	webWins bool
	claims  int
	// claimDelay makes a claim slow (the timeout case).
	claimDelay time.Duration
}

type fakeLetter struct {
	client.TakenMail
	state string
}

func newMailFake() *mailFake { return &mailFake{fakeAPI: newFake()} }

func (f *mailFake) add(id int64, urgent bool, body string) {
	f.mmu.Lock()
	defer f.mmu.Unlock()
	f.letters = append(f.letters, &fakeLetter{state: "pending", TakenMail: client.TakenMail{
		ID: id, ThreadID: "m" + strconv.FormatInt(id, 10), Urgent: urgent, Body: body, Topic: "gofer 开发",
		From: client.MailParty{Kind: "session", ID: "sess-a", Display: "会话 sess-a（调用方 alice）"},
	}})
}

func (f *mailFake) states() map[int64]string {
	f.mmu.Lock()
	defer f.mmu.Unlock()
	out := map[int64]string{}
	for _, l := range f.letters {
		out[l.ID] = l.state
	}
	return out
}

func (f *mailFake) pending() int {
	n := 0
	for _, l := range f.letters {
		if l.state == "pending" || l.state == "delivering" {
			n++
		}
	}
	return n
}

func (f *mailFake) withMail(a client.AgentSession) client.AgentSession {
	f.mmu.Lock()
	defer f.mmu.Unlock()
	a.MailPending = f.pending()
	return a
}

func (f *mailFake) RegisterSession(in client.SessionRegister) (client.AgentSession, error) {
	a, err := f.fakeAPI.RegisterSession(in)
	return f.withMail(a), err
}

func (f *mailFake) HeartbeatSession(sid string, hb client.SessionHeartbeat) (client.AgentSession, error) {
	a, err := f.fakeAPI.HeartbeatSession(sid, hb)
	return f.withMail(a), err
}

// claimLocked is the server's ordering and limits: urgent first, then oldest, within 5 letters
// and 8000 characters (the first letter always fits, truncated).
func (f *mailFake) claimLocked(take client.MailTake) (out []client.TakenMail, remaining int) {
	var elig []*fakeLetter
	for _, urgentPass := range []bool{true, false} {
		for _, l := range f.letters {
			if l.state == "pending" && l.Urgent == urgentPass && (!take.UrgentOnly || l.Urgent) {
				elig = append(elig, l)
			}
		}
	}
	chars := 0
	for _, l := range elig {
		n := len([]rune(l.Body))
		if len(out) >= take.Limit || (len(out) > 0 && chars+n > take.MaxChars) {
			break
		}
		l.state = "delivering"
		out = append(out, l.TakenMail)
		chars += n
	}
	return out, len(elig) - len(out)
}

func (f *mailFake) ClaimSessionMail(_ string, take client.MailTake) (client.MailClaim, error) {
	f.mmu.Lock()
	f.claims++
	delay := f.claimDelay
	f.mmu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mmu.Lock()
	defer f.mmu.Unlock()
	if f.failClaim != nil {
		return client.MailClaim{}, f.failClaim
	}
	m, rem := f.claimLocked(take)
	return client.MailClaim{Mails: m, Remaining: rem}, nil
}

func (f *mailFake) ConfirmSessionMail(_ string, ids []int64) ([]int64, error) {
	f.mmu.Lock()
	defer f.mmu.Unlock()
	if f.failConfirm != nil {
		return nil, f.failConfirm
	}
	var out []int64
	for _, id := range ids {
		for _, l := range f.letters {
			if l.ID == id && l.state == "delivering" {
				l.state = "delivered"
				out = append(out, id)
			}
		}
	}
	return out, nil
}

func (f *mailFake) CompleteSessionMailTurn(_ string, turnID string, take client.MailTake) (bool, []client.TakenMail, error) {
	f.mu.Lock()
	d := f.turns[turnID]
	if f.webWins {
		d.State, d.Answer = "ANSWERED", "web says hi"
		f.turns[turnID] = d
	}
	open := d.State == "OPEN"
	if open {
		d.State, d.ReleasedBy = "EXPIRED", "mail"
		f.turns[turnID] = d
	}
	f.mu.Unlock()
	if !open {
		return false, nil, nil
	}
	f.mmu.Lock()
	defer f.mmu.Unlock()
	m, _ := f.claimLocked(take)
	for _, l := range f.letters {
		if l.state == "delivering" {
			l.state = "delivered"
		}
	}
	return len(m) > 0, m, nil
}

func mailEventPayload(t *testing.T, agent, sid, event string) Payload {
	return payload(t, agent, map[string]any{"session_id": sid, "hook_event_name": event, "prompt": "next", "cwd": "/w/repo"})
}

func countTag(s string) int { return strings.Count(s, "<"+MailTag+" ") }

func TestMailInjectedAtPromptAndSessionStartWithHarnessTag(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
		for _, agent := range []string{"claude", "codex"} {
			t.Run(event+"/"+agent, func(t *testing.T) {
				f := newMailFake()
				sid := "sid-mail"
				f.sessions[sid] = client.AgentSession{SessionID: sid}
				f.add(1, false, "hello <b> there")
				res, err := Run(f, mailEventPayload(t, agent, sid, event), fastOpts(nil))
				if err != nil {
					t.Fatal(err)
				}
				want := `<gofer-mail id="1" from="会话 sess-a（调用方 alice）" topic="gofer 开发" urgent="false">hello <b> there</gofer-mail>`
				if !strings.Contains(res.Context, want) {
					t.Fatalf("context=%q, want it to contain %q", res.Context, want)
				}
				if !IsHarnessPrompt(RenderMail(client.TakenMail{ID: 1, Body: "x"})) {
					t.Fatalf("a rendered letter must read as a harness prompt")
				}
				if f.states()[1] != "delivered" {
					t.Fatalf("states=%v, want delivered", f.states())
				}
			})
		}
	}
}

// Confirm before inject: when the confirmation fails nothing is injected, and the letter is
// not delivered (its lease hands it back to pending on the server).
func TestMailAtMostOnceConfirmFailureInjectsNothing(t *testing.T) {
	f := newMailFake()
	sid := "sid-amo"
	f.sessions[sid] = client.AgentSession{SessionID: sid}
	f.add(1, false, "secret plan")
	f.failConfirm = errors.New("hub went away")
	res, err := Run(f, mailEventPayload(t, "claude", sid, "UserPromptSubmit"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Context, "secret plan") || countTag(res.Context) != 0 {
		t.Fatalf("an unconfirmed letter must not be injected: %q", res.Context)
	}
	if got := f.states()[1]; got == "delivered" {
		t.Fatalf("state=%s, the letter must not count as delivered", got)
	}
	// A claim failure injects nothing either.
	f2 := newMailFake()
	f2.sessions[sid] = client.AgentSession{SessionID: sid}
	f2.add(2, false, "x")
	f2.failClaim = errors.New("boom")
	res, _ = Run(f2, mailEventPayload(t, "claude", sid, "UserPromptSubmit"), fastOpts(nil))
	if res.Context != "" {
		t.Fatalf("context=%q, want empty", res.Context)
	}
}

func TestMailMergesWithJobNoticeAndMemoryContext(t *testing.T) {
	f := newMailFake()
	sid := "sid-merge"
	f.sessions[sid] = client.AgentSession{SessionID: sid}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-5", Title: "tests", Status: "done"}}
	f.add(7, false, "mail body")
	opts := fastOpts(nil)
	opts.MemoryStateDir = t.TempDir()
	opts.PromptMemories = func(string) ([]PromptMemory, error) {
		return []PromptMemory{{Memory: kwMemory("release-flow", "", "step 1", "发版")}}, nil
	}
	p := payload(t, "claude", map[string]any{"session_id": sid, "hook_event_name": "UserPromptSubmit", "prompt": "开始发版", "cwd": "/w/repo"})
	res, err := Run(f, p, opts)
	if err != nil {
		t.Fatal(err)
	}
	if mem := strings.Index(res.Context, "step 1"); mem < 0 || mem < strings.Index(res.Context, "<gofer-mail id=\"7\"") {
		t.Fatalf("the memory must share the same context after the letter: %q", res.Context)
	}
	job := strings.Index(res.Context, "[gofer job 完成] job-5")
	mailAt := strings.Index(res.Context, "<gofer-mail id=\"7\"")
	if job < 0 || mailAt < 0 || job > mailAt {
		t.Fatalf("one context with the job notice then the letter, got %q", res.Context)
	}
	// SessionStart appends the topic reminder after the letter, still one context.
	f2 := newMailFake()
	f2.sessions[sid] = client.AgentSession{SessionID: sid}
	f2.add(8, false, "b")
	res, _ = Run(f2, mailEventPayload(t, "claude", sid, "SessionStart"), fastOpts(nil))
	if !strings.Contains(res.Context, "<gofer-mail id=\"8\"") || !strings.Contains(res.Context, TopicReminder) ||
		strings.Index(res.Context, "gofer-mail") > strings.Index(res.Context, TopicReminder) {
		t.Fatalf("context=%q", res.Context)
	}
}

func TestMailInjectionLimitsAndMoreLine(t *testing.T) {
	sid := "sid-lim"
	t.Run("five letters", func(t *testing.T) {
		f := newMailFake()
		f.sessions[sid] = client.AgentSession{SessionID: sid}
		for i := int64(1); i <= 7; i++ {
			f.add(i, false, "m"+strconv.FormatInt(i, 10))
		}
		res, _ := Run(f, mailEventPayload(t, "claude", sid, "UserPromptSubmit"), fastOpts(nil))
		if countTag(res.Context) != 5 {
			t.Fatalf("letters injected=%d, want 5: %q", countTag(res.Context), res.Context)
		}
		if !strings.Contains(res.Context, MailMoreLine(2)) || !strings.Contains(res.Context, "还有 2 封待读信件") {
			t.Fatalf("missing the 还有 N 封 line: %q", res.Context)
		}
		if st := f.states(); st[6] != "pending" || st[7] != "pending" || st[5] != "delivered" {
			t.Fatalf("letters beyond the limit must not be taken: %v", st)
		}
		// The next turn takes the rest and has no more-line.
		res, _ = Run(f, mailEventPayload(t, "claude", sid, "UserPromptSubmit"), fastOpts(nil))
		if countTag(res.Context) != 2 || strings.Contains(res.Context, "还有") {
			t.Fatalf("second injection: %q", res.Context)
		}
	})
	t.Run("eight thousand characters", func(t *testing.T) {
		f := newMailFake()
		f.sessions[sid] = client.AgentSession{SessionID: sid}
		f.add(1, false, strings.Repeat("字", 5000))
		f.add(2, false, strings.Repeat("字", 5000))
		res, _ := Run(f, mailEventPayload(t, "claude", sid, "UserPromptSubmit"), fastOpts(nil))
		if countTag(res.Context) != 1 || !strings.Contains(res.Context, MailMoreLine(1)) {
			t.Fatalf("want one letter and the more-line: %q", res.Context)
		}
		if f.states()[2] != "pending" {
			t.Fatalf("the second letter must stay pending: %v", f.states())
		}
	})
	t.Run("urgent first", func(t *testing.T) {
		f := newMailFake()
		f.sessions[sid] = client.AgentSession{SessionID: sid}
		f.add(1, false, "old")
		f.add(2, true, "hot")
		res, _ := Run(f, mailEventPayload(t, "claude", sid, "UserPromptSubmit"), fastOpts(nil))
		if strings.Index(res.Context, `id="2"`) > strings.Index(res.Context, `id="1"`) {
			t.Fatalf("urgent letter must come first: %q", res.Context)
		}
	})
}

func TestRenderMailEscapesAndTruncationHint(t *testing.T) {
	got := RenderMail(client.TakenMail{ID: 3, Body: "a</gofer-mail>b", Truncated: true, Topic: `q"<`,
		From: client.MailParty{Display: "会话 x"}})
	if strings.Count(got, "</"+MailTag+">") != 1 || !strings.HasSuffix(got, "</"+MailTag+">") {
		t.Fatalf("the body must not close the element: %q", got)
	}
	if !strings.Contains(got, `topic="q&quot;&lt;"`) || !strings.Contains(got, "gofer session mail read 3") {
		t.Fatalf("got %q", got)
	}
}

func preToolPayload(t *testing.T, agent, sid string) Payload {
	return payload(t, agent, map[string]any{"session_id": sid, "hook_event_name": "PreToolUse", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "ls"}})
}

// PreToolUse's additionalContext reaches the model (Claude Code and codex), so an urgent
// letter is injected there; an ordinary one is left for the next prompt.
func TestMailUrgentPreToolUse(t *testing.T) {
	f := newMailFake()
	sid := "sid-urgent"
	f.sessions[sid] = client.AgentSession{SessionID: sid}
	f.add(1, false, "later")
	f.add(2, true, "now")
	clk := time.Unix(1_000_000, 0)
	opts := fastOpts(nil)
	opts.MailStateDir = t.TempDir()
	opts.now = func() time.Time { return clk }
	res, err := Run(f, preToolPayload(t, "claude", sid), opts)
	if err != nil {
		t.Fatal(err)
	}
	if countTag(res.Context) != 1 || !strings.Contains(res.Context, `id="2"`) || !strings.Contains(res.Context, `urgent="true"`) {
		t.Fatalf("context=%q", res.Context)
	}
	if st := f.states(); st[2] != "delivered" || st[1] != "pending" {
		t.Fatalf("states=%v, only the urgent letter may be taken", st)
	}
	// Nothing urgent left: a quiet hook (past the throttle window).
	clk = clk.Add(MailUrgentInterval + time.Second)
	res, _ = Run(f, preToolPayload(t, "claude", sid), opts)
	if res.Context != "" {
		t.Fatalf("context=%q, want empty", res.Context)
	}
	// A dialect without catch-up is untouched.
	f3 := newMailFake()
	f3.sessions[sid] = client.AgentSession{SessionID: sid}
	f3.add(1, true, "x")
	res, _ = Run(f3, preToolPayload(t, "omp", sid), opts)
	if res.Context != "" || f3.claims != 0 {
		t.Fatalf("omp must not take mail: ctx=%q claims=%d", res.Context, f3.claims)
	}
}

func TestMailDialectWithoutCatchUpIsUntouched(t *testing.T) {
	f := newMailFake()
	sid := "sid-omp"
	f.sessions[sid] = client.AgentSession{SessionID: sid}
	f.add(1, false, "x")
	res, _ := Run(f, mailEventPayload(t, "omp", sid, "UserPromptSubmit"), fastOpts(nil))
	if res.Context != "" || f.claims != 0 || f.states()[1] != "pending" {
		t.Fatalf("ctx=%q claims=%d states=%v", res.Context, f.claims, f.states())
	}
}

// TestMailStopTruthTable: a Stop blocks with the letters only when letters are already pending
// AND the person is away by the relay's rule; stop_job_wait does not matter, and a Stop never
// blocks just to wait for a letter that has not arrived.
func TestMailStopTruthTable(t *testing.T) {
	const threshold = 300
	cases := []struct {
		name      string
		pending   bool
		relayMode string
		idle      int64
		stopWait  string
		want      bool
	}{
		{"pending, away (idle past threshold)", true, client.RelayModeAuto, 600, "", true},
		{"pending, at the keyboard", true, client.RelayModeAuto, 10, "", false},
		{"pending, unknown reading", true, client.RelayModeAuto, -1, "", false},
		{"pending, relay on", true, client.RelayModeOn, 10, "", true},
		{"pending, relay off", true, client.RelayModeOff, 600, "", false},
		{"pending, stop_job_wait=always does not block a present person", true, client.RelayModeAuto, 10, config.StopJobWaitAlways, false},
		{"pending, stop_job_wait=never still delivers to an absent person", true, client.RelayModeAuto, 600, config.StopJobWaitNever, true},
		{"nothing pending, away", false, client.RelayModeAuto, 600, "", false},
		{"nothing pending, relay on", false, client.RelayModeOn, 10, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			injectIdleProbe(t, tc.idle)
			f := newMailFake()
			sid := "sid-stop"
			f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: tc.relayMode, WaitReasonDetail: "supervising 1 jobs"}
			if tc.pending {
				f.add(4, false, "please look")
			}
			opts := fastOpts(nil)
			opts.AutoRelayIdleSec = threshold
			opts.StopJobWait = tc.stopWait
			res, err := Run(f, stopPayload(t, "claude", sid), opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Blocked != tc.want {
				t.Fatalf("blocked=%v, want %v (reason=%q)", res.Blocked, tc.want, res.Reason)
			}
			if tc.want {
				if !strings.HasPrefix(res.Reason, "<gofer-mail id=\"4\"") || !strings.HasSuffix(res.Reason, "</gofer-mail>") || !IsHarnessPrompt(res.Reason) {
					t.Fatalf("reason=%q, want the rendered letter", res.Reason)
				}
				if f.states()[4] != "delivered" {
					t.Fatalf("states=%v", f.states())
				}
			} else if tc.pending && f.states()[4] != "pending" {
				t.Fatalf("a released Stop must leave the letter pending: %v", f.states())
			}
			if len(f.turns) != 0 {
				t.Fatalf("no relay turn expected: %+v", f.turns)
			}
		})
	}
}

// With a relay turn open, the letters close it through the server's one operation; a web
// answer that won the race is consumed instead and the letters stay pending.
func TestMailStopRelayTurnRaceWithWebReply(t *testing.T) {
	setup := func(webWins bool) (*mailFake, Result) {
		injectIdleProbe(t, 5)
		f := newMailFake()
		sid := "sid-race"
		f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
		f.add(1, false, "from a peer")
		f.webWins = webWins
		f.answerAfter, f.answer = 1, "web says hi"
		res, err := Run(f, stopPayload(t, "claude", sid), fastOpts(nil))
		if err != nil {
			t.Fatal(err)
		}
		return f, res
	}
	f, res := setup(false)
	if !res.Blocked || !strings.Contains(res.Reason, `<gofer-mail id="1"`) || strings.Contains(res.Reason, "web says hi") {
		t.Fatalf("mail must win when the turn was still open: %+v", res)
	}
	if f.states()[1] != "delivered" || f.turns["dec-1"].ReleasedBy != "mail" {
		t.Fatalf("states=%v turn=%+v", f.states(), f.turns["dec-1"])
	}
	f, res = setup(true)
	if !res.Blocked || !strings.Contains(res.Reason, "web says hi") || strings.Contains(res.Reason, "gofer-mail") {
		t.Fatalf("web must win when it answered first: %+v", res)
	}
	if f.states()[1] != "pending" {
		t.Fatalf("the letter must stay pending when the web won: %v", f.states())
	}
}

// The PreToolUse take is throttled per session through a marker on disk: one claim per 30 s,
// a 2 minute back-off after a timeout or error, and an urgent letter still arrives on the
// first eligible event.
func TestMailUrgentPreToolUseThrottle(t *testing.T) {
	sid := "sid-throttle"
	clk := time.Unix(2_000_000, 0)
	mk := func(f *mailFake) Options {
		o := fastOpts(nil)
		o.MailStateDir = t.TempDir()
		o.now = func() time.Time { return clk }
		return o
	}
	t.Run("one claim per window", func(t *testing.T) {
		f := newMailFake()
		f.sessions[sid] = client.AgentSession{SessionID: sid}
		opts := mk(f)
		Run(f, preToolPayload(t, "claude", sid), opts)
		f.add(1, true, "now")
		clk = clk.Add(10 * time.Second)
		res, _ := Run(f, preToolPayload(t, "claude", sid), opts)
		if f.claims != 1 || res.Context != "" {
			t.Fatalf("claims=%d ctx=%q, want the second event inside 30s skipped", f.claims, res.Context)
		}
		clk = clk.Add(25 * time.Second)
		res, _ = Run(f, preToolPayload(t, "claude", sid), opts)
		if f.claims != 2 || !strings.Contains(res.Context, `id="1"`) {
			t.Fatalf("claims=%d ctx=%q, want the urgent letter on the first eligible event", f.claims, res.Context)
		}
	})
	t.Run("first event delivers", func(t *testing.T) {
		f := newMailFake()
		f.sessions[sid] = client.AgentSession{SessionID: sid}
		f.add(2, true, "hot")
		res, _ := Run(f, preToolPayload(t, "claude", sid), mk(f))
		if !strings.Contains(res.Context, `id="2"`) {
			t.Fatalf("ctx=%q", res.Context)
		}
	})
	t.Run("timeout backs off two minutes", func(t *testing.T) {
		f := newMailFake()
		f.sessions[sid] = client.AgentSession{SessionID: sid}
		f.claimDelay = 2 * MailUrgentDeadline
		opts := mk(f)
		Run(f, preToolPayload(t, "claude", sid), opts)
		clk = clk.Add(MailUrgentInterval + time.Second) // past the normal window, inside the back-off
		Run(f, preToolPayload(t, "claude", sid), opts)
		f.mmu.Lock()
		claims := f.claims
		f.mmu.Unlock()
		if claims > 1 {
			t.Fatalf("claims=%d, want the check skipped inside the back-off window", claims)
		}
		clk = clk.Add(MailUrgentBackoff)
		f.claimDelay = 0
		f.add(3, true, "late")
		res, _ := Run(f, preToolPayload(t, "claude", sid), opts)
		if !strings.Contains(res.Context, `id="3"`) {
			t.Fatalf("after the back-off ctx=%q", res.Context)
		}
	})
	t.Run("claim error backs off", func(t *testing.T) {
		f := newMailFake()
		f.sessions[sid] = client.AgentSession{SessionID: sid}
		f.failClaim = errors.New("boom")
		opts := mk(f)
		Run(f, preToolPayload(t, "claude", sid), opts)
		clk = clk.Add(MailUrgentInterval + time.Second)
		Run(f, preToolPayload(t, "claude", sid), opts)
		if f.claims != 1 {
			t.Fatalf("claims=%d, want 1", f.claims)
		}
	})
}
