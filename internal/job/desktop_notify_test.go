package job

import (
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/desktop"
)

// desktopRecorder stands in for the platform popup: it records what would have been
// shown, so the test proves the wiring without opening a window.
type desktopRecorder struct {
	mu   sync.Mutex
	seen []desktop.Notification
}

func (r *desktopRecorder) Show(n desktop.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, n)
	return nil
}

func (r *desktopRecorder) shown() []desktop.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]desktop.Notification(nil), r.seen...)
}

// withDesktop turns the desktop channel on for a test service, merging disabled so a
// popup is shown at once, and swaps in the recorder.
func withDesktop(t *testing.T, s *Service, events []string) *desktopRecorder {
	t.Helper()
	zero := 0
	n := s.config().Server.Notification
	if n == nil {
		n = &config.NotificationConfig{}
		s.config().Server.Notification = n
	}
	n.Desktop = &config.DesktopNotifyConfig{Events: events, MergeSec: &zero}
	rec := &desktopRecorder{}
	s.desktopNotifier = rec
	t.Cleanup(s.closeDesktop)
	return rec
}

// TestDesktopNotifiesApprover (gofer-3wce): a held job's job.awaiting_approval pops a
// desktop notification carrying the same approval text the IM adapters get, with the
// job page as the click target. job.terminal is not a default desktop event, so running
// and finishing the job adds nothing.
func TestDesktopNotifiesApprover(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, nil, nil)
	rec := withDesktop(t, s, nil)
	s.config().Server.WebBaseURL = "https://gofer.example.com/"

	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Title: "push release",
		Cmd: []string{"git", "push", "origin", "main"}, Cwd: ".", TimeoutSec: 30,
		Hold: true, HoldReason: "ship v1",
	})
	if err != nil || res.Status != StatusAwaitingApproval {
		t.Fatalf("held submit = %s, %v", res.Status, err)
	}

	got := rec.shown()
	if len(got) != 1 {
		t.Fatalf("desktop notifications = %d, want 1: %+v", len(got), got)
	}
	n := got[0]
	if n.EventType != EventJobAwaitingApproval {
		t.Errorf("event = %q, want %q", n.EventType, EventJobAwaitingApproval)
	}
	if !strings.Contains(n.Title, "待批准：push release") {
		t.Errorf("title = %q, want the approval title", n.Title)
	}
	if !strings.Contains(n.Text, "ship v1") {
		t.Errorf("text = %q, want the hold reason", n.Text)
	}
	if want := "https://gofer.example.com/jobs/" + res.ID; n.Link != want {
		t.Errorf("link = %q, want %q", n.Link, want)
	}

	if _, err := s.RejectJob(res.ID, "alice", "", false); err != nil {
		t.Fatalf("RejectJob: %v", err)
	}
	for _, extra := range rec.shown()[1:] {
		t.Errorf("unexpected desktop notification for %q (not a default desktop event)", extra.EventType)
	}
}

// TestDesktopNotifiesNeedsReview: job.needs_review is a default desktop event and has
// no dedicated IM shape, so it renders as the job headline with the job page as the
// click target.
func TestDesktopNotifiesNeedsReview(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, nil, nil)
	rec := withDesktop(t, s, nil)
	s.config().Server.WebBaseURL = "https://gofer.example.com/"

	s.notifyDesktopJobEvent(s.config(),
		JobResult{ID: "job-1", ProjectKey: "self", Agent: "exec", Title: "review me"},
		EventJobNeedsReview, `{"job_id":"job-1","exit_code":0}`, 100)

	got := rec.shown()
	if len(got) != 1 {
		t.Fatalf("desktop notifications = %d, want 1: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Title, "job 等待验收：review me") {
		t.Errorf("title = %q", got[0].Title)
	}
	if !strings.Contains(got[0].Text, "project self") || !strings.Contains(got[0].Text, "agent exec") {
		t.Errorf("text = %q, want project and agent", got[0].Text)
	}
	if want := "https://gofer.example.com/jobs/job-1"; got[0].Link != want {
		t.Errorf("link = %q, want %q", got[0].Link, want)
	}
}

// TestDesktopProjectsFilter: the channel's projects filter keeps a project out.
func TestDesktopProjectsFilter(t *testing.T) {
	root := t.TempDir()
	s := newNotifyService(t, root, nil, nil)
	rec := withDesktop(t, s, nil)
	s.config().Server.Notification.Desktop.Projects = []string{"other"}

	if _, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Title: "push release",
		Cmd: []string{"git", "push", "origin", "main"}, Cwd: ".", TimeoutSec: 30,
		Hold: true, HoldReason: "ship v1",
	}); err != nil {
		t.Fatalf("held submit: %v", err)
	}
	if got := rec.shown(); len(got) != 0 {
		t.Errorf("desktop notifications = %+v, want none (project filtered out)", got)
	}
}
