package httpapi

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// TestResidentSessionMayTouchOnlyItsOwnMail is the server side of gofer-sh7r: the
// approval gate lets a resident 收件会话 answer a letter without a human, so the letter
// it may touch must be exactly the mail delivered to the session it currently is —
// anything else stays refused by the job-credential rule.
func TestResidentSessionMayTouchOnlyItsOwnMail(t *testing.T) {
	e := mailEnvWithAgent(t)
	st := e.s.jobs.Meta()
	org := mail.Origin{Kind: jobstore.MailKindSession, ID: e.sA, Caller: "alice"}
	out, err := e.s.mail.Send(mail.SendInput{To: "agent:suga@p1", Body: "hello", Origin: org, Project: "p1"})
	if err != nil {
		t.Fatalf("send to agent:suga@p1: %v", err)
	}
	if out.Mail.ToKind != jobstore.MailKindAgent {
		t.Fatalf("seeded letter goes to %s:%s", out.Mail.ToKind, out.Mail.ToID)
	}

	// No resident session serves the address yet: no job credential owns the letter.
	if e.s.mailJobMayTouch("job-resident", out.Mail) {
		t.Fatal("a job touched a letter nobody delivered to it")
	}
	if err := st.PutResidentAgent(jobstore.MailResidentAgent{
		Name: "suga", Project: "p1", JobID: "job-resident", Status: jobstore.MailResidentLive,
	}); err != nil {
		t.Fatalf("seed resident row: %v", err)
	}
	if !e.s.mailJobMayTouch("job-resident", out.Mail) {
		t.Fatal("the resident session may not answer the letter delivered to it")
	}
	// Another job (another conversation) may not.
	if e.s.mailJobMayTouch("job-elsewhere", out.Mail) {
		t.Fatal("a different job credential touched the resident session's letter")
	}
	// A rotated / ended session may not either: the address now belongs to its replacement.
	if err := st.EndResidentAgent("suga", "p1", jobstore.MailResidentEnded, "test"); err != nil {
		t.Fatalf("end resident row: %v", err)
	}
	if e.s.mailJobMayTouch("job-resident", out.Mail) {
		t.Fatal("an ended resident session kept its letters")
	}
}
