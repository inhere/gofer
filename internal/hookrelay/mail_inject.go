package hookrelay

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/mail"
)

// Mailbox delivery by the hook (design 2026-10-16「二、gofer 信箱」, plan M4b). A letter is
// injected at the start of the session's next turn (SessionStart / UserPromptSubmit), an
// URGENT one at the next PreToolUse inside a turn, and — when the person is away — at Stop as
// the block reason. Every path takes letters the same way: claim on the server, confirm, and
// inject only what the confirmation returned, so a letter is injected AT MOST once (a hook
// that dies after the confirmation loses the letter, which the design accepts; a hook that
// fails before it loses nothing — the lease hands the letter back to pending).

const (
	// MailInjectLimit / MailInjectChars are 单次注入 (设计「默认数值」): at most 5 letters and
	// 8000 characters of body per injection; the rest wait for the next one.
	MailInjectLimit = 5
	MailInjectChars = 8000
	// MailUrgentDeadline bounds the PreToolUse take: the event fires before every tool call,
	// so a slow hub must cost almost nothing (past it the hook injects nothing).
	MailUrgentDeadline = 300 * time.Millisecond
	// MailUrgentInterval is how often one session's PreToolUse looks for urgent letters; a
	// failed or timed-out take backs off for MailUrgentBackoff instead, so a down hub costs
	// at most one slow tool call per window. The marker lives on disk because the hook
	// process is short-lived.
	MailUrgentInterval = 30 * time.Second
	MailUrgentBackoff  = 2 * time.Minute
)

// MailAPI is the hub surface of the mail take; *client.Client satisfies it. It is kept apart
// from API so a hub client without it (a test double, an older build) simply gets no mail
// delivery instead of failing to compile.
type MailAPI interface {
	ClaimSessionMail(sid string, take client.MailTake) (client.MailClaim, error)
	ConfirmSessionMail(sid string, ids []int64) ([]int64, error)
	CompleteSessionMailTurn(sid, turnID string, take client.MailTake) (bool, []client.TakenMail, error)
}

func (r *runner) mailAPI() (MailAPI, bool) {
	m, ok := r.api.(MailAPI)
	return m, ok && strings.TrimSpace(r.p.SessionID) != ""
}

func mailTake(urgentOnly bool) client.MailTake {
	return client.MailTake{Limit: MailInjectLimit, MaxChars: MailInjectChars, UrgentOnly: urgentOnly}
}

// RenderMail renders one letter as the fixed harness tag, so the model (and IsHarnessPrompt)
// can tell a letter from something a person typed. Attribute values are escaped; a body that
// contains the closing tag cannot end the element early.
func RenderMail(m client.TakenMail) string {
	from := m.From.Display
	if from == "" {
		from = m.From.Kind + " " + m.From.ID
	}
	return mail.RenderLetter(mail.Letter{
		ID: m.ID, From: from, Topic: m.Topic, Urgent: m.Urgent, Body: m.Body, Truncated: m.Truncated,
		BroadcastID: m.BroadcastID, Recipients: m.Recipients,
	})
}

// MailMoreLine is the one line that says letters are still waiting after a capped injection.
func MailMoreLine(n int) string {
	return fmt.Sprintf("还有 %d 封待读信件，下一轮继续送达，或 gofer session mail ls", n)
}

// renderMails joins the letters; the limits are the server's, but the hook never renders
// beyond them either, whatever an unexpected server returned.
func renderMails(mails []client.TakenMail) string {
	parts := make([]string, 0, len(mails))
	chars := 0
	for _, m := range mails {
		if len(parts) >= MailInjectLimit {
			break
		}
		n := utf8.RuneCountInString(m.Body)
		if len(parts) > 0 && chars+n > MailInjectChars {
			break
		}
		chars += n
		parts = append(parts, RenderMail(m))
	}
	return strings.Join(parts, "\n")
}

// takeMail is the claim → confirm half shared by every hook path. It returns the confirmed
// letters only; a confirmation failure means NOTHING is injected (the lease returns the
// letters to pending). remaining counts the eligible letters left on the server.
func (r *runner) takeMail(urgentOnly bool, abandoned *atomic.Bool, failed *atomic.Bool) (mails []client.TakenMail, remaining int) {
	api, ok := r.mailAPI()
	if !ok {
		return nil, 0
	}
	claim, err := api.ClaimSessionMail(r.p.SessionID, mailTake(urgentOnly))
	if err != nil {
		r.log("mail claim failed: %v", err)
		if failed != nil {
			failed.Store(true)
		}
		return nil, 0
	}
	if len(claim.Mails) == 0 {
		return nil, claim.Remaining
	}
	if abandoned != nil && abandoned.Load() {
		r.log("mail claim finished past the deadline, %d letter(s) left to the lease", len(claim.Mails))
		return nil, 0
	}
	ids := make([]int64, 0, len(claim.Mails))
	for _, m := range claim.Mails {
		ids = append(ids, m.ID)
	}
	confirmed, err := api.ConfirmSessionMail(r.p.SessionID, ids)
	if err != nil {
		r.log("mail confirm failed, injecting nothing: %v", err)
		if failed != nil {
			failed.Store(true)
		}
		return nil, 0
	}
	ok2 := make(map[int64]bool, len(confirmed))
	for _, id := range confirmed {
		ok2[id] = true
	}
	for _, m := range claim.Mails {
		if ok2[m.ID] {
			mails = append(mails, m)
		}
	}
	return mails, claim.Remaining
}

// catchUpMail appends the session's pending letters to the context SessionStart /
// UserPromptSubmit injects (next to the job notices and memories: one additionalContext).
func (r *runner) catchUpMail(res Result, a client.AgentSession) Result {
	if a.MailPending <= 0 || !CatchUpAgent(r.p.dialect()) {
		return res
	}
	mails, remaining := r.takeMail(false, nil, nil)
	if len(mails) == 0 {
		return res
	}
	text := renderMails(mails)
	if remaining > 0 {
		text += "\n" + MailMoreLine(remaining)
	}
	r.log("catch-up: injecting %d letter(s), %d more waiting", len(mails), remaining)
	if res.Context != "" {
		res.Context += "\n\n"
	}
	res.Context += text
	return res
}

// urgentMailContext is the PreToolUse take: urgent letters only, within MailUrgentDeadline.
func (r *runner) urgentMailContext() string {
	if _, ok := r.mailAPI(); !ok || !CatchUpAgent(r.p.dialect()) {
		return ""
	}
	type out struct {
		mails     []client.TakenMail
		remaining int
	}
	if !r.urgentMailDue() {
		return ""
	}
	var abandoned, failed atomic.Bool
	done := make(chan out, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				r.log("urgent mail: panic: %v", v)
				done <- out{}
			}
		}()
		m, rem := r.takeMail(true, &abandoned, &failed)
		done <- out{m, rem}
	}()
	timer := time.NewTimer(MailUrgentDeadline)
	defer timer.Stop()
	select {
	case o := <-done:
		if failed.Load() {
			r.urgentMailBackoff()
		}
		if len(o.mails) == 0 {
			return ""
		}
		text := renderMails(o.mails)
		if o.remaining > 0 {
			text += "\n" + MailMoreLine(o.remaining)
		}
		r.log("urgent mail: injecting %d letter(s) before the tool call", len(o.mails))
		return text
	case <-timer.C:
		abandoned.Store(true)
		r.urgentMailBackoff()
		r.log("urgent mail: deadline %s exceeded, nothing injected", MailUrgentDeadline)
		return ""
	}
}

// mayBlockForMail decides whether a Stop may be parked on already-pending letters: the person
// must be away by the relay's own reading (mayBlockForJobs in `away` mode), whatever
// session.stop_job_wait says — `always` must not hold a person at the keyboard hostage for a
// letter. It never waits for letters that have not arrived: the caller only asks while
// mail_pending > 0.
func (r *runner) mayBlockForMail(a client.AgentSession, idle int64) bool {
	return a.MailPending > 0 && CatchUpAgent(r.p.dialect()) && r.mayBlockForJobs(config.StopJobWaitAway, a, idle)
}

// stopMailBlock is the Stop delivery with NO relay turn open: take, confirm, block with the
// letters as the reason.
func (r *runner) stopMailBlock() (Result, bool) {
	mails, _ := r.takeMail(false, nil, nil)
	if len(mails) == 0 {
		return Result{}, false
	}
	r.log("stop: delivering %d letter(s) as the block reason", len(mails))
	return Result{Blocked: true, Reason: renderMails(mails)}, true
}

// stopMailTurn is the Stop delivery while the relay turn is open: the server closes the turn
// and takes the letters in one operation; a web answer that won the race is left to the wait
// loop.
func (r *runner) stopMailTurn(turnID string) (Result, bool) {
	api, ok := r.mailAPI()
	if !ok {
		return Result{}, false
	}
	completed, mails, err := api.CompleteSessionMailTurn(r.p.SessionID, turnID, mailTake(false))
	if err != nil {
		r.log("complete mail turn failed: %v", err)
		return Result{}, false
	}
	if !completed || len(mails) == 0 {
		return Result{}, false
	}
	r.log("stop: delivering %d letter(s), relay turn %s closed", len(mails), turnID)
	return Result{Blocked: true, Reason: renderMails(mails)}, true
}

func (r *runner) urgentMailStatePath() string {
	dir := strings.TrimSpace(r.opts.MailStateDir)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "gofer-hook-mail")
	}
	hash := sha1.Sum([]byte(r.p.SessionID))
	return filepath.Join(dir, fmt.Sprintf("%x.next", hash[:]))
}

// urgentMailDue reports whether this PreToolUse may look for urgent letters, and — when it
// may — books the next check MailUrgentInterval ahead BEFORE calling the hub, so even a take
// that hangs or a hook killed mid-call keeps the next tool calls free.
func (r *runner) urgentMailDue() bool {
	path := r.urgentMailStatePath()
	now := r.opts.now()
	if b, err := os.ReadFile(path); err == nil {
		var next int64
		if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &next); err == nil && now.Unix() < next {
			return false
		}
	}
	r.writeUrgentNext(now.Add(MailUrgentInterval))
	return true
}

// urgentMailBackoff postpones the next check after a failed or timed-out take.
func (r *runner) urgentMailBackoff() { r.writeUrgentNext(r.opts.now().Add(MailUrgentBackoff)) }

func (r *runner) writeUrgentNext(t time.Time) {
	path := r.urgentMailStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\n", t.Unix())), 0o600)
}
