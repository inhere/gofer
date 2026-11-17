package job

import (
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
)

// A hosted session has no idle timeout and no maximum length; Submit refuses the flag
// without a persistent session or next to an explicit limit.
func TestSubmitHostedSession(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	base := JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".", Prompt: "hi", TimeoutSec: 60}

	for name, req := range map[string]JobRequest{
		"without session":   withHosted(base, false, 0, 0),
		"with idle timeout": withHosted(base, true, 120, 0),
		"with max length":   withHosted(base, true, 0, 600),
	} {
		if _, err := s.Submit(req); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}

	res, err := s.Submit(withHosted(base, true, 0, 0))
	if err != nil {
		t.Fatalf("submit hosted session: %v", err)
	}
	waitStatus(t, s, res.ID, 15*time.Second, StatusAwaitingInput)
	got, _ := s.Get(res.ID)
	if got.IdleTimeoutSec != 0 || got.MaxSessionSec != 0 {
		t.Fatalf("hosted session limits = idle %d / max %d, want none", got.IdleTimeoutSec, got.MaxSessionSec)
	}
	if got.IdleDeadlineAt != 0 {
		t.Fatalf("hosted session has an idle deadline %d", got.IdleDeadlineAt)
	}
}

func withHosted(req JobRequest, session bool, idle, max int) JobRequest {
	req.Session, req.Hosted, req.IdleTimeoutSec, req.MaxSessionSec = session, true, idle, max
	return req
}
