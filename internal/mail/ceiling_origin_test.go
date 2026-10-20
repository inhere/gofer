package mail

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The deliverer speaks under its own label ("gofer-mail"), but the relay job it dispatches
// belongs to whoever the letter came from (sessionrelay's ceiling_origin.go): a restricted
// sender must not reach a session's runner unbounded through the mailbox.

// TestDelivererCarriesTheLetterSenderAsCeilingOwner pins the owner of a letter body delivery.
func TestDelivererCarriesTheLetterSenderAsCeilingOwner(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-suga", "suga", jobstore.SessionRunning, time.Minute)
	if _, err := f.svc.Send(SendInput{
		To: "session:sess-suga", Body: "hi", Origin: Origin{Kind: jobstore.MailKindCaller, Caller: "bot"},
	}); err != nil {
		t.Fatal(err)
	}
	d, snd, _ := newDeliverer(f)

	d.Tick(context.Background())
	if _, l := snd.counts(); l != 1 {
		t.Fatalf("letters=%d, want 1", l)
	}
	if len(snd.letterOrigins) != 1 || snd.letterOrigins[0] != "bot" {
		t.Fatalf("letter origins = %v, want [bot]", snd.letterOrigins)
	}
}

// TestDelivererWakeCarriesTheLetterSenderAsCeilingOwner is the wake half: the wake prompt is
// gofer's own text, but the job runs for the caller whose pending letter caused it.
func TestDelivererWakeCarriesTheLetterSenderAsCeilingOwner(t *testing.T) {
	f := newFixture(t)
	f.sessionSeenAt(t, "sess-idle", "claude", jobstore.SessionIdle, 10*time.Minute)
	if _, err := f.svc.Send(SendInput{
		To: "session:sess-idle", Body: "hi", Origin: Origin{Kind: jobstore.MailKindCaller, Caller: "bot"},
	}); err != nil {
		t.Fatal(err)
	}
	d, snd, _ := newDeliverer(f)

	d.Tick(context.Background())
	if w, _ := snd.counts(); w != 1 {
		t.Fatalf("wakes=%d, want 1", w)
	}
	if len(snd.wakeOrigins) != 1 || snd.wakeOrigins[0] != "bot" {
		t.Fatalf("wake origins = %v, want [bot]", snd.wakeOrigins)
	}
}

// TestLetterOriginFallsBackToTheSenderRecord: a letter whose stored FromCaller is empty (the
// authenticated caller behind it was not recorded) resolves to the owner of what sent it.
func TestLetterOriginFallsBackToTheSenderRecord(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "sess-from", "claude", "", jobstore.SessionRunning)
	d, _, _ := newDeliverer(f)

	got := d.letterOrigin(jobstore.Mail{FromKind: jobstore.MailKindSession, FromID: "sess-from"})
	if got != "alice" {
		t.Fatalf("origin of a session letter = %q, want the session owner alice", got)
	}
	// A stored sender wins over the record lookup (a caller: letter names its sender directly).
	got = d.letterOrigin(jobstore.Mail{FromKind: jobstore.MailKindCaller, FromCaller: "bot"})
	if got != "bot" {
		t.Fatalf("origin = %q, want bot", got)
	}
}
