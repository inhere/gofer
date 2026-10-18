package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
)

// The hook's mail take (plan M4b): claim → confirm, the Stop turn completion, mail_pending on
// the session view and the permission cells of the three routes.

func (e *mailEnv) putMail(t *testing.T, to string, urgent bool, body string) int64 {
	t.Helper()
	m, err := e.s.jobs.Meta().InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindSession, FromID: e.sB, FromCaller: "bob",
		ToAddress: "session:" + to, ToKind: jobstore.MailKindSession, ToID: to, Urgent: urgent, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

type claimView struct {
	Mails     []takenMailView `json:"mails"`
	Remaining int             `json:"remaining"`
}

func (e *mailEnv) claim(t *testing.T, tok, sid string, body map[string]any) (int, claimView) {
	t.Helper()
	status, raw := e.req(t, tok, "", "POST", "/v1/sessions/"+sid+"/mail/claim", body)
	var v claimView
	if status == http.StatusOK {
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
	}
	return status, v
}

func TestSessionMailClaimConfirmAndPendingCount(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	old := e.putMail(t, e.sA, false, "old")
	hot := e.putMail(t, e.sA, true, "hot")
	e.putMail(t, e.sA, false, "newer")

	// mail_pending rides the session view (register / heartbeat / get).
	status, raw := e.req(t, mailTokAlice, "", "POST", "/v1/sessions/"+e.sA+"/heartbeat", map[string]any{"event": "UserPromptSubmit"})
	var sv struct {
		MailPending int `json:"mail_pending"`
	}
	_ = json.Unmarshal([]byte(raw), &sv)
	if status != http.StatusOK || sv.MailPending != 3 {
		t.Fatalf("heartbeat = %d %s, want mail_pending 3", status, raw)
	}

	// Urgent first, then oldest; a limit of 2 leaves one.
	status, v := e.claim(t, mailTokAlice, e.sA, map[string]any{"limit": 2})
	if status != http.StatusOK || len(v.Mails) != 2 || v.Mails[0].ID != hot || v.Mails[1].ID != old || v.Remaining != 1 {
		t.Fatalf("claim = %d %+v, want [hot old] remaining 1", status, v)
	}
	if v.Mails[0].Topic != "billing refactor" || v.Mails[0].From.Kind != "session" || !v.Mails[0].Urgent ||
		!strings.Contains(v.Mails[0].From.Display, "sess-bob") {
		t.Fatalf("view=%+v, want the sender's topic and display", v.Mails[0])
	}
	// A claimed letter is not claimable again.
	_, again := e.claim(t, mailTokAlice, e.sA, map[string]any{"limit": 5})
	if len(again.Mails) != 1 || again.Mails[0].Body != "newer" {
		t.Fatalf("second claim=%+v, want only the unclaimed letter", again)
	}
	st := e.s.jobs.Meta()
	if m, _, _ := st.GetMail(hot); m.State != jobstore.MailDelivering || m.ClaimedBy != "hook:"+e.sA {
		t.Fatalf("claimed letter=%+v", m)
	}
	// Confirm: delivering -> delivered; a foreign id or a re-confirm is simply not confirmed.
	status, raw = e.req(t, mailTokAlice, "", "POST", "/v1/sessions/"+e.sA+"/mail/confirm", map[string]any{"ids": []int64{hot, old, 99999}})
	var cf struct {
		Confirmed []int64 `json:"confirmed"`
	}
	_ = json.Unmarshal([]byte(raw), &cf)
	if status != http.StatusOK || len(cf.Confirmed) != 2 {
		t.Fatalf("confirm = %d %s", status, raw)
	}
	if m, _, _ := st.GetMail(hot); m.State != jobstore.MailDelivered || m.DeliveredAt == 0 {
		t.Fatalf("confirmed letter=%+v", m)
	}
	_, raw = e.req(t, mailTokAlice, "", "POST", "/v1/sessions/"+e.sA+"/mail/confirm", map[string]any{"ids": []int64{hot}})
	if strings.Contains(raw, "\"confirmed\":[") && strings.Contains(raw, "\"confirmed\":["+jsonID(hot)) {
		t.Fatalf("re-confirm must confirm nothing: %s", raw)
	}
	// Another session's hook cannot confirm a claim it does not hold.
	other := e.putMail(t, e.sA, false, "x")
	e.claim(t, mailTokAlice, e.sA, map[string]any{})
	if got, _ := st.ConfirmSessionMail(e.sSup, []int64{other}); len(got) != 0 {
		t.Fatalf("a foreign session confirmed %v", got)
	}
}

func jsonID(id int64) string { b, _ := json.Marshal(id); return string(b) }

func TestSessionMailClaimLimits(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	for i := 0; i < 7; i++ {
		e.putMail(t, e.sA, false, "m")
	}
	_, v := e.claim(t, mailTokAlice, e.sA, nil)
	if len(v.Mails) != DefaultMailTakeLimit || v.Remaining != 2 {
		t.Fatalf("default take=%d remaining=%d, want 5 / 2", len(v.Mails), v.Remaining)
	}
	// 8000 characters: the second 5000-character letter does not fit and stays pending.
	e2 := newMailEnv(t)
	e2.grant()
	a := e2.putMail(t, e2.sA, false, strings.Repeat("字", 5000))
	b := e2.putMail(t, e2.sA, false, strings.Repeat("字", 5000))
	_, v = e2.claim(t, mailTokAlice, e2.sA, nil)
	if len(v.Mails) != 1 || v.Mails[0].ID != a || v.Remaining != 1 {
		t.Fatalf("take=%+v, want only the first letter", v)
	}
	if m, _, _ := e2.s.jobs.Meta().GetMail(b); m.State != jobstore.MailPending {
		t.Fatalf("second letter=%+v, want pending", m)
	}
	// A letter that alone exceeds the budget is delivered truncated, never stuck.
	e3 := newMailEnv(t)
	e3.grant()
	big := e3.putMail(t, e3.sA, false, strings.Repeat("x", 9000))
	_, v = e3.claim(t, mailTokAlice, e3.sA, nil)
	if len(v.Mails) != 1 || v.Mails[0].ID != big || !v.Mails[0].Truncated || len([]rune(v.Mails[0].Body)) != DefaultMailTakeChars {
		t.Fatalf("oversize take=%+v", v)
	}
	// Urgent-only (the PreToolUse path) skips ordinary letters.
	e4 := newMailEnv(t)
	e4.grant()
	e4.putMail(t, e4.sA, false, "plain")
	hot := e4.putMail(t, e4.sA, true, "hot")
	_, v = e4.claim(t, mailTokAlice, e4.sA, map[string]any{"urgent_only": true})
	if len(v.Mails) != 1 || v.Mails[0].ID != hot || v.Remaining != 0 {
		t.Fatalf("urgent-only take=%+v", v)
	}
}

// An abandoned claim (the hook died before confirming) returns to pending when its lease
// ran out and is then claimable again.
func TestSessionMailLeaseExpiryReturnsClaim(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	id := e.putMail(t, e.sA, false, "lost")
	st := e.s.jobs.Meta()
	taken, _, err := st.ClaimSessionMail(e.sA, jobstore.TakeMailOpts{TTL: time.Minute})
	if err != nil || len(taken) != 1 {
		t.Fatalf("claim=%v err=%v", taken, err)
	}
	if again, _, _ := st.ClaimSessionMail(e.sA, jobstore.TakeMailOpts{}); len(again) != 0 {
		t.Fatalf("a live lease must hold the letter: %v", again)
	}
	// Rewind the lease instead of sleeping.
	if _, err := st.ReleaseExpiredClaims(1 << 40); err != nil {
		t.Fatal(err)
	}
	again, _, err := st.ClaimSessionMail(e.sA, jobstore.TakeMailOpts{})
	if err != nil || len(again) != 1 || again[0].ID != id {
		t.Fatalf("after expiry claim=%v err=%v", again, err)
	}
}

func TestSessionMailRoutePermissionCells(t *testing.T) {
	e := newMailEnv(t)
	e.putMail(t, e.sA, false, "x")
	claimPath := "/v1/sessions/" + e.sA + "/mail/claim"
	e.grant()
	// The owner needs no permission.
	if status, _ := e.req(t, mailTokAlice, "", "POST", claimPath, map[string]any{}); status != http.StatusOK {
		t.Errorf("owner claim = %d, want 200", status)
	}
	// Someone else's session needs session.reply.others (deny), on all three routes.
	for _, path := range []string{claimPath, "/v1/sessions/" + e.sA + "/mail/confirm", "/v1/sessions/" + e.sA + "/turns/t1/complete-mail"} {
		status, body := e.req(t, mailTokBob, "", "POST", path, map[string]any{"ids": []int64{1}})
		wantPermission(t, "bob on alice's session "+path, status, body, authz.PermSessionReplyOthers)
	}
	e.grant(authz.PermSessionReplyOthers)
	if status, body := e.req(t, mailTokBob, "", "POST", claimPath, map[string]any{}); status != http.StatusOK {
		t.Errorf("bob with the permission = %d (%s), want 200", status, body)
	}
	// A job credential and a worker token are refused outright.
	for _, tok := range []string{e.tokSrc, e.tokNone, "tok-w-mail"} {
		status, _ := e.req(t, tok, "", "POST", claimPath, map[string]any{})
		if status != http.StatusForbidden && status != http.StatusUnauthorized {
			t.Errorf("token %q claim = %d, want refused", tok, status)
		}
	}
	// A caller outside the ceiling does not even see the session.
	if status, _ := e.req(t, mailTokDave, "", "POST", "/v1/sessions/no-such/mail/claim", map[string]any{}); status == http.StatusOK {
		t.Errorf("unknown session claim = %d, want an error", status)
	}
}

func (e *mailEnv) openTurn(t *testing.T) string {
	t.Helper()
	if status, body := e.req(t, mailTokAlice, "", "POST", "/v1/sessions/"+e.sA+"/relay", map[string]any{"mode": "on"}); status != http.StatusOK {
		t.Fatalf("relay on = %d %s", status, body)
	}
	status, body := e.req(t, mailTokAlice, "", "POST", "/v1/sessions/"+e.sA+"/turns", map[string]any{"body": "waiting"})
	var turn decisionView
	if err := json.Unmarshal([]byte(body), &turn); err != nil || status != http.StatusOK {
		t.Fatalf("open turn = %d %s", status, body)
	}
	return turn.ID
}

func TestSessionMailCompleteTurnClosesTurnAndConfirms(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	id := e.putMail(t, e.sA, false, "for the stop")
	turn := e.openTurn(t)
	status, raw := e.req(t, mailTokAlice, "", "POST", "/v1/sessions/"+e.sA+"/turns/"+turn+"/complete-mail", map[string]any{})
	var out struct {
		Completed bool            `json:"completed"`
		Mails     []takenMailView `json:"mails"`
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if status != http.StatusOK || !out.Completed || len(out.Mails) != 1 || out.Mails[0].ID != id {
		t.Fatalf("complete = %d %s", status, raw)
	}
	st := e.s.jobs.Meta()
	if d, _, _ := st.GetDecision(turn); d.State != jobstore.DecisionExpired || d.ReleasedBy != "mail" {
		t.Fatalf("turn=%+v, want expired by mail", d)
	}
	if m, _, _ := st.GetMail(id); m.State != jobstore.MailDelivered {
		t.Fatalf("letter=%+v, want delivered (confirmed before the hook injects)", m)
	}
}

func TestSessionMailCompleteTurnNothingPendingLeavesTurnOpen(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	turn := e.openTurn(t)
	_, raw := e.req(t, mailTokAlice, "", "POST", "/v1/sessions/"+e.sA+"/turns/"+turn+"/complete-mail", map[string]any{})
	if !strings.Contains(raw, `"completed":false`) {
		t.Fatalf("raw=%s, want completed=false", raw)
	}
	if d, _, _ := e.s.jobs.Meta().GetDecision(turn); d.State != jobstore.DecisionOpen {
		t.Fatalf("turn=%+v, an empty mailbox must not close the turn", d)
	}
}

// The web reply and the letter race for the same open turn: exactly one wins, and the loser
// leaves no trace (a letter that lost stays pending; an answer that lost is not accepted).
func TestSessionMailWebReplyVersusLetterOnlyOneWins(t *testing.T) {
	for i := 0; i < 10; i++ {
		e := newMailEnv(t)
		e.grant()
		id := e.putMail(t, e.sA, false, "race")
		turn := e.openTurn(t)
		st := e.s.jobs.Meta()
		var wg sync.WaitGroup
		var answered bool
		var completed bool
		wg.Add(2)
		go func() {
			defer wg.Done()
			answered, _ = st.AnswerDecision(turn, "web says hi", "alice")
		}()
		go func() {
			defer wg.Done()
			completed, _, _ = e.s.relay.CompleteMailTurn(e.sA, turn, jobstore.TakeMailOpts{Limit: 5, MaxRunes: 8000})
		}()
		wg.Wait()
		if answered == completed {
			t.Fatalf("round %d: answered=%v completed=%v, want exactly one winner", i, answered, completed)
		}
		m, _, _ := st.GetMail(id)
		d, _, _ := st.GetDecision(turn)
		if answered && (m.State != jobstore.MailPending || d.State != jobstore.DecisionAnswered) {
			t.Fatalf("web won but letter=%s turn=%s, want pending / answered", m.State, d.State)
		}
		if completed && (m.State != jobstore.MailDelivered || d.ReleasedBy != "mail") {
			t.Fatalf("mail won but letter=%s turn=%s/%s, want delivered / released by mail", m.State, d.State, d.ReleasedBy)
		}
	}
}
