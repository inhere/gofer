package seat

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// takeNotice binds a session to the seat, takes the one-time notice for that start source and
// gives the seat up again, so the next round starts with the notice still owed.
func takeNotice(t *testing.T, e *env, seatID, ref, source string) string {
	t.Helper()
	if _, err := e.svc.Bind(seatID, ref, true, "user"); err != nil {
		t.Fatal(err)
	}
	text, ok, err := e.svc.TakeContinuationNotice(ref, source, i18n.EN)
	if err != nil || !ok {
		t.Fatalf("notice for source %q: ok=%v err=%v", source, ok, err)
	}
	if _, err := e.svc.Unbind(seatID, "user"); err != nil {
		t.Fatal(err)
	}
	return text
}

// TestNoticeOnlyClaimsLostContextOnStartup: the first SessionStart of a session that resumes an
// earlier conversation must not be told that conversation is gone.
func TestNoticeOnlyClaimsLostContextOnStartup(t *testing.T) {
	e := newEnv(t)
	x := e.seat(t, "dev", "")
	e.session(t, "s1", "/d/work/proj")
	for _, c := range []struct {
		source string
		fresh  bool
	}{{"startup", true}, {"resume", false}, {"compact", false}, {"clear", false}, {"", false}} {
		text := takeNotice(t, e, x.ID, "s1", c.source)
		if got := strings.Contains(text, "new session"); got != c.fresh {
			t.Errorf("source %q: fresh wording = %v, want %v: %s", c.source, got, c.fresh, text)
		}
		if !strings.Contains(text, "dev") {
			t.Errorf("source %q: the notice must still name the seat: %s", c.source, text)
		}
	}
}

// TestCharterAppearsOnceInTheStartupContext: the seat block renders the charter, so the notice
// that follows it must not paste the same text a second time.
func TestCharterAppearsOnceInTheStartupContext(t *testing.T) {
	e := newEnv(t)
	x := e.seat(t, "dev", "")
	const charter = "RULE-9: always run the tests of the touched package"
	if _, err := e.svc.Update(x.ID, jobstore.SeatPatch{Charter: strptr(charter)}); err != nil {
		t.Fatal(err)
	}
	x, err := e.svc.Get(x.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.session(t, "s1", "/d/work/proj")
	text := takeNotice(t, e, x.ID, "s1", SourceStartup)

	block := Section(SectionInput{Name: x.Name, Purpose: x.Purpose, Charter: x.Charter, Continuation: text, Lang: i18n.EN})
	if n := strings.Count(block, charter); n != 1 {
		t.Fatalf("the charter appears %d times in the SessionStart context, want once:\n%s", n, block)
	}
	if strings.Contains(text, charter) {
		t.Errorf("the notice itself must not carry the charter: %s", text)
	}
}
