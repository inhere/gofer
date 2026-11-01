package job

import (
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/store"
)

// The steward's idle-end resume loads the earlier session as a NEW job: a continuation
// does not inherit the steward stamp by itself, so the resume asks for it, and gets the
// steward marker (durable) and credential again. Without the option it is an ordinary job.
func TestStewardResumeCarriesTheStewardStamp(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{EnvPrint: []string{"GOFER_STEWARD"}})
	st := stewardSession(t, s, true)
	if err := s.EndSession(st.ID); err != nil {
		t.Fatal(err)
	}
	s.Wait(st.ID)
	src, _ := s.Get(st.ID)

	resumed, err := s.ResumeJobWith(st.ID, "wake up", "", "gofer", ResumeOptions{Mode: ResumeModeSession, Steward: true, Internal: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.Session || resumed.SessionID != src.SessionID {
		t.Fatalf("resumed = session %v id %q, want a resident continuation of %q", resumed.Session, resumed.SessionID, src.SessionID)
	}
	if !s.meta.IsStewardJob(resumed.ID) {
		t.Fatal("the resumed steward job must carry the durable steward marker")
	}
	waitSessionTurn(t, s, resumed.ID, 1)
	if got := envLine(readJobLog(t, resumed, store.StderrFile), "GOFER_STEWARD"); got != "1" {
		t.Fatalf("the resumed steward session lost the steward marker env (%q)", got)
	}
	if err := s.EndSession(resumed.ID); err != nil {
		t.Fatal(err)
	}
	s.Wait(resumed.ID)

	plain, err := s.ResumeJobWith(resumed.ID, "again", "", "gofer", ResumeOptions{Mode: ResumeModeSession, Internal: true})
	if err != nil {
		t.Fatalf("plain resume: %v", err)
	}
	if s.meta.IsStewardJob(plain.ID) {
		t.Fatal("a continuation without the option must not become the steward")
	}
	waitSessionTurn(t, s, plain.ID, 1)
	_ = s.EndSession(plain.ID)
	s.Wait(plain.ID)
}
