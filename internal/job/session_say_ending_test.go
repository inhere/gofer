package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
)

func TestSessionJobSayRejectedAfterCancelOrEnd(t *testing.T) {
	for _, ending := range []string{"cancel", "end"} {
		t.Run(ending, func(t *testing.T) {
			s := newACPService(t, t.TempDir(), acptest.Options{})
			created := submitSmokeSession(t, s, 20)
			waitSessionTurn(t, s, created.ID, 1)
			if ending == "cancel" {
				if err := s.Cancel(created.ID); err != nil {
					t.Fatal(err)
				}
			} else if err := s.EndSession(created.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.SaySession(created.ID, "late message"); !errors.Is(err, ErrJobNotRunning) || !strings.Contains(err.Error(), "session is ending") {
				t.Fatalf("say after %s = %v, want conflict explaining session is ending", ending, err)
			}
			if _, ok := s.Wait(created.ID); !ok {
				t.Fatal("session job disappeared")
			}
		})
	}
}
