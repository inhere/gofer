package steward

import (
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// notShort keeps a session out of the noise rule (enough human prompts) in the tests that
// are about something else.
func notShort(t *testing.T, st *jobstore.Store, sid string) {
	t.Helper()
	for i := 0; i < 3; i++ {
		_, err := st.IncrementSessionHumanPrompts(sid)
		assert.NoErr(t, err)
	}
}

// shortSession attaches a session that ended right after one prompt: a test / try-out
// session, which the design 2026-10-20 noise rule keeps out of the steward.
func (e *env) shortSession(t *testing.T, w jobstore.WorkItem, sid string) {
	t.Helper()
	_, err := e.st.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p", Cwd: "/ws"})
	assert.NoErr(t, err)
	_, _, err = e.st.TouchAgentSession(sid, jobstore.SessionHeartbeat{State: jobstore.SessionEnded})
	assert.NoErr(t, err)
	_, err = e.st.IncrementSessionHumanPrompts(sid)
	assert.NoErr(t, err)
	assert.NoErr(t, e.st.AttachWorkSession(w.ID, sid, "human:a"))
}

func TestShortSessionsAreInvisibleToTheSteward(t *testing.T) {
	e := newEnv(t)
	now := time.Now()

	noise := e.item(t, "试试导出功能")
	e.shortSession(t, noise, "sess-noise0001")
	real := e.item(t, "真正的活")
	e.offlineSession(t, real, "sess-noise0002", 0)

	// Events: the short session ending is not worth a steward look.
	e.svc.scanEvents(now)
	ev, _ := e.st.PendingStewardEvents(10)
	assert.Eq(t, 1, len(ev))
	assert.True(t, strings.Contains(ev[0].Detail, "真正的活"))

	// The review does not pick the short item, not even when an event points straight at it.
	picked, _, err := e.svc.reviewItems(TriggerEvent, now.Unix(), []jobstore.StewardEvent{
		{Kind: jobstore.StewardEventSession, Ref: noise.ID + "|sess-noise0001|ended|1"},
	}, 10)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(picked))

	// Prime: the open-item list leaves it out, the real one stays.
	p, err := e.svc.BuildPrime(now)
	assert.NoErr(t, err)
	assert.True(t, strings.Contains(p.Text, "真正的活"))
	assert.False(t, strings.Contains(p.Text, "试试导出功能"))

	// Once it is no longer short (enough asks), it comes back.
	notShort(t, e.st, "sess-noise0001")
	picked, _, err = e.svc.reviewItems(TriggerEvent, now.Unix(), []jobstore.StewardEvent{
		{Kind: jobstore.StewardEventSession, Ref: noise.ID + "|sess-noise0001|ended|1"},
	}, 10)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(picked))
	assert.Eq(t, noise.ID, picked[0].ID)
}

// An item with a live (not short) session stays even when another of its sessions was short:
// the rule only drops items whose current sessions are ALL short.
func TestShortSessionDoesNotHideAnItemWithALiveSession(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	w := e.item(t, "两个会话")
	e.shortSession(t, w, "sess-noise0003")
	_, err := e.st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-noise0004", Agent: "claude", ProjectKey: "p", Cwd: "/ws", State: jobstore.SessionRunning})
	assert.NoErr(t, err)
	assert.NoErr(t, e.st.AttachWorkSession(w.ID, "sess-noise0004", "human:a"))

	picked, _, err := e.svc.reviewItems(TriggerEvent, now.Unix(), []jobstore.StewardEvent{
		{Kind: jobstore.StewardEventSession, Ref: w.ID + "|sess-noise0003|ended|1"},
	}, 10)
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(picked))
}
