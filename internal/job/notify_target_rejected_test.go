package job

import (
	"errors"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestNotifyTargetRefusalIsIdentifiable: a submit refused for its notify target (not a
// registered session, owned by another caller, or none at all) is an invalid request
// that also matches ErrNotifyTargetRejected, while the message text stays as it was.
func TestNotifyTargetRefusalIsIdentifiable(t *testing.T) {
	root := t.TempDir()
	svc := newTestService(t, root)
	_, err := svc.meta.UpsertAgentSession(jobstore.AgentSession{
		SessionID: "notify-id-foreign", Agent: "claude", ProjectKey: "self",
		Runner: "local", Cwd: root, CallerID: "caller-b",
	})
	assert.NoErr(t, err)
	for name, req := range map[string]JobRequest{
		"foreign": {Notify: true, NotifySession: "notify-id-foreign", CallerID: "caller-a"},
		"unknown": {Notify: true, NotifySession: "notify-id-missing", CallerID: "caller-a"},
		"none":    {Notify: true, CallerID: "caller-a"},
	} {
		req.ProjectKey, req.Agent, req.Runner, req.Cmd, req.Cwd = "self", "exec", "local", []string{"go", "version"}, "."
		_, err := svc.Submit(req)
		assert.True(t, errors.Is(err, ErrNotifyTargetRejected), name)
		assert.True(t, errors.Is(err, ErrInvalidRequest), name)
	}
	// an unrelated invalid request is not the notify refusal
	_, err = svc.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: "."})
	assert.Err(t, err)
	assert.False(t, errors.Is(err, ErrNotifyTargetRejected))
}
