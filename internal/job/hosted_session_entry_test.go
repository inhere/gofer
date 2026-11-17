package job

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
)

// A hosted session keeps its "no idle timeout, no maximum length" shape across a
// resume: the continuation is the same always-on session, so re-deriving the limits
// from a source row that has none must carry Hosted with them.
func TestResumeInheritsHosted(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	ac := s.config().Agents["acpbot"]
	src := JobResult{ID: "job-src", Runner: "local", Session: true, Hosted: true, SessionID: "sess-1"}

	got, err := s.acpResumeRequest(JobRequest{ProjectKey: "self"}, src, ac, "acpbot", "next turn", true, 0, false)
	if err != nil {
		t.Fatalf("acpResumeRequest: %v", err)
	}
	if !got.Session || !got.Hosted {
		t.Fatalf("resume of a hosted session = session %v / hosted %v, want both", got.Session, got.Hosted)
	}
	if got.IdleTimeoutSec != 0 || got.MaxSessionSec != 0 {
		t.Fatalf("resume limits = idle %d / max %d, want none", got.IdleTimeoutSec, got.MaxSessionSec)
	}

	// A plain (non-hosted) session stays plain: the flag is inherited, not invented.
	src.Hosted, src.IdleTimeoutSec = false, 900
	got, err = s.acpResumeRequest(JobRequest{ProjectKey: "self"}, src, ac, "acpbot", "next turn", true, 0, false)
	if err != nil {
		t.Fatalf("acpResumeRequest (plain): %v", err)
	}
	if got.Hosted || got.IdleTimeoutSec != 900 {
		t.Fatalf("plain resume = hosted %v / idle %d, want false / 900", got.Hosted, got.IdleTimeoutSec)
	}
}

// The hosted flag survives the database read path: it is persisted inside
// session_state_json, so `job show` / the API can say the session is hosted after a
// restart, not just while the row is in memory.
func TestHostedSessionSurvivesReload(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "hi", TimeoutSec: 60, Session: true, Hosted: true,
	})
	if err != nil {
		t.Fatalf("submit hosted session: %v", err)
	}
	waitStatus(t, s, res.ID, 15*time.Second, StatusAwaitingInput)

	rec, ok, err := s.meta.GetJob(res.ID)
	if err != nil || !ok {
		t.Fatalf("read the row back: ok=%v err=%v", ok, err)
	}
	got := fromRecord(rec)
	if !got.Hosted {
		t.Fatalf("the stored row lost hosted (session_state_json = %q)", rec.SessionStateJSON)
	}
	if !got.Session {
		t.Fatalf("the stored row lost session (session_state_json = %q)", rec.SessionStateJSON)
	}
}
