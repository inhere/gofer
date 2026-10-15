package job

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestResumeInheritsTheEndNotification: a caller who asked to be told when the work
// ends is asking about the WORK, so a continuation of that job keeps the
// notification (gofer-3db4) — aimed at the same session, because a continuation has no
// source session of its own to fall back on. `--no-notify` drops it.
func TestResumeInheritsTheEndNotification(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newClaudeInjectService(t, root)
	_, err := s.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "resume-notify-target", Agent: "claude", ProjectKey: "self",
		Runner: "local", Cwd: root, CallerID: "caller",
	})
	assert.NoErr(t, err)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "source",
		Cwd: ".", TimeoutSec: 30,
		Notify: true, NotifySession: "resume-notify-target",
	})

	next, err := s.ResumeJobWith(src.ID, "continue", "", "caller", ResumeOptions{})
	assert.NoErr(t, err)
	assert.True(t, next.Notify, "a continuation inherits the end-notification")
	assert.Eq(t, "resume-notify-target", next.NotifySession)

	// The inheritance is a real pending notification: the continuation registered the
	// target's watch, which is what its own end will claim.
	watches, err := s.meta.ListSessionJobWatches("resume-notify-target")
	assert.NoErr(t, err)
	found := false
	for _, w := range watches {
		if w.JobID == next.ID {
			found = true
		}
	}
	assert.True(t, found, "the continuation's notify target must have a pending record")

	dropped, err := s.ResumeJobWith(src.ID, "continue again", "", "caller", ResumeOptions{NoNotify: true})
	assert.NoErr(t, err)
	assert.False(t, dropped.Notify, "--no-notify drops the inherited notification")
	assert.Eq(t, "", dropped.NotifySession)
}

// TestResumeWithoutNotifyStaysSilent pins the other half: a source job that never
// asked for a notification produces continuations that do not notify either (nothing
// is inherited from nothing, and no messenger process is started).
func TestResumeWithoutNotifyStaysSilent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newClaudeInjectService(t, root)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "source",
		Cwd: ".", TimeoutSec: 30,
	})
	next, err := s.ResumeJobWith(src.ID, "continue", "", "caller", ResumeOptions{})
	assert.NoErr(t, err)
	assert.False(t, next.Notify)
	assert.Eq(t, "", next.NotifySession)
}
