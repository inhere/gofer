package job

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/waitnotice"
)

// The desktop channel pops the same words as the others: the default desktop events include
// session.awaiting_reply, so a 「等我」 notice shows with its title and thread link, and the
// popup has no text beyond that.
func TestNotifyWaitingPopsOnDesktopWithoutText(t *testing.T) {
	s := newNotifyService(t, t.TempDir(), nil, nil)
	rec := withDesktop(t, s, nil)
	s.config().Server.WebBaseURL = "https://gofer.example.com/"

	notice := waitnotice.Notice{Subject: waitnotice.Subject{Key: "job:j1", Category: "answer", ProjectKey: "self", Title: "ACP 助手", ThreadID: "j:j1"}}
	s.NotifyWaiting(notice)
	notice.Reminder = true
	s.NotifyWaiting(notice)

	got := rec.shown()
	if len(got) != 2 {
		t.Fatalf("desktop notifications = %d, want 2 (notice and reminder): %+v", len(got), got)
	}
	if got[0].Title != "ACP 助手 在等你回答" || got[0].Text != "" || !strings.HasSuffix(got[0].Link, "/workbench?thread=j%3Aj1") {
		t.Fatalf("notice = %+v", got[0])
	}
	if got[1].Title != "提醒：ACP 助手 在等你回答" {
		t.Fatalf("reminder = %+v", got[1])
	}
}
