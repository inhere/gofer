package mail

import (
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// The entry layer's Authorize hook and the capture box's readdress (plan M3).

var errTestRefused = errors.New("refused by the entry layer")

func TestSendAuthorizeRunsBeforeAnythingIsStored(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-a", "claude", "", jobstore.SessionIdle)
	org := Origin{Kind: jobstore.MailKindCaller, Caller: "alice"}

	var seen Target
	_, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "hi", Origin: org, Authorize: func(tg Target) error {
		seen = tg
		return errTestRefused
	}})
	if !errors.Is(err, errTestRefused) {
		t.Fatalf("err = %v, want the hook's error as is", err)
	}
	if seen.Kind != jobstore.MailKindSession || seen.ID != "sess-a" {
		t.Fatalf("hook saw %+v, want the resolved session", seen)
	}
	if list, _ := f.store.ListMail(jobstore.ListMailOpts{Limit: 50}); len(list) != 0 {
		t.Fatalf("a refused send stored %d letter(s)", len(list))
	}
	// An address that resolved to nobody never reaches the hook (it is held, not delivered).
	res, err := f.svc.Send(SendInput{To: "session:ghost", Body: "hi", Origin: org, Authorize: func(Target) error { return errTestRefused }})
	if err != nil || !res.Held {
		t.Fatalf("unresolved address: %+v %v, want a held letter", res, err)
	}
}

func TestReplyAuthorizeJudgesTheOriginalSender(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-a", "claude", "", jobstore.SessionIdle)
	sent, err := f.svc.Send(SendInput{To: "session:sess-a", Body: "q", Origin: Origin{Kind: jobstore.MailKindSession, ID: "sess-a", Caller: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	var seen Target
	_, err = f.svc.Reply(ReplyInput{ID: sent.Mail.ID, Body: "a", Origin: Origin{Kind: jobstore.MailKindCaller, Caller: "alice"},
		Authorize: func(tg Target) error { seen = tg; return errTestRefused }})
	if !errors.Is(err, errTestRefused) || seen.ID != "sess-a" {
		t.Fatalf("reply: err=%v target=%+v", err, seen)
	}
	if n, _ := f.store.CountThreadMail(sent.Mail.ThreadID); n != 1 {
		t.Fatalf("thread has %d letters, want the original only", n)
	}
}

func TestReaddressHandsAHeldLetterOn(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-a", "claude", "", jobstore.SessionIdle)
	org := Origin{Kind: jobstore.MailKindCaller, Caller: "alice"}
	held, err := f.svc.Send(SendInput{To: "session:ghost", Body: "lost", Urgent: true, Origin: org})
	if err != nil || !held.Held {
		t.Fatalf("setup: %+v %v", held, err)
	}
	out, err := f.svc.Readdress(ReaddressInput{ID: held.Mail.ID, To: "session:sess-a", Origin: org})
	if err != nil {
		t.Fatal(err)
	}
	if out.Held || out.Mail.ToID != "sess-a" || out.Mail.ThreadID != held.Mail.ThreadID || out.Mail.Body != "lost" || !out.Mail.Urgent {
		t.Fatalf("readdressed letter = %+v", out.Mail)
	}
	if old, _, _ := f.store.GetMail(held.Mail.ID); old.State != jobstore.MailCancelled {
		t.Fatalf("original is %s, want cancelled", old.State)
	}
	// Only a held letter can be readdressed; an unknown one is not found.
	if _, err := f.svc.Readdress(ReaddressInput{ID: out.Mail.ID, To: "caller", Origin: org}); !errors.Is(err, jobstore.ErrMailState) {
		t.Fatalf("readdress of a pending letter: %v, want ErrMailState", err)
	}
	if _, err := f.svc.Readdress(ReaddressInput{ID: 999999, To: "caller", Origin: org}); !errors.Is(err, ErrMailNotFound) {
		t.Fatalf("readdress of an unknown letter: %v, want ErrMailNotFound", err)
	}
	// A refused new recipient leaves the held letter untouched.
	held2, _ := f.svc.Send(SendInput{To: "session:ghost", Body: "again", Origin: org})
	if _, err := f.svc.Readdress(ReaddressInput{ID: held2.Mail.ID, To: "session:sess-a", Origin: org, Authorize: func(Target) error { return errTestRefused }}); !errors.Is(err, errTestRefused) {
		t.Fatalf("authorized readdress: %v", err)
	}
	if cur, _, _ := f.store.GetMail(held2.Mail.ID); cur.State != jobstore.MailHeld {
		t.Fatalf("after a refused readdress the letter is %s, want held", cur.State)
	}
}
