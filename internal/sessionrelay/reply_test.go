package sessionrelay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func registerReplySession(t *testing.T, s *Service, sid, runner, peer, owner string) {
	t.Helper()
	_, err := s.Register(RegisterInput{SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: runner,
		PeerName: peer, PeerMessaging: true, Event: EventSessionStart, CallerID: owner})
	assert.NoErr(t, err)
}

func replies(t *testing.T, s *Service, sid string) []jobstore.SessionMessage {
	t.Helper()
	list, err := s.store.ReadSessionOutbox(sid)
	assert.NoErr(t, err)
	var out []jobstore.SessionMessage
	for _, m := range list {
		if m.Direction == jobstore.SessionMessageReply {
			out = append(out, m)
		}
	}
	return out
}

// A session's own SendMessage answer lands in its outbox as a reply to the latest
// web message, and only its owner may report it.
func TestRecordReplyFromSessionOwner(t *testing.T) {
	s := newSvc(t)
	f := &residentFake{}
	s.SetMessenger(f)
	registerReplySession(t, s, "sid-r", "server", "proj-a", "hook-alice")
	msg, err := s.SendMessage(context.Background(), "sid-r", "进度如何", "alice")
	assert.NoErr(t, err)

	_, _, err = s.RecordReply("sid-r", "答复", "uds:/x.sock", jobstore.SessionReplySourceSession, "")
	assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner), "anonymous caller refused")
	_, _, err = s.RecordReply("sid-r", "答复", "uds:/x.sock", jobstore.SessionReplySourceSession, "mallory")
	assert.True(t, errors.Is(err, ErrSessionHeartbeatOwner), "foreign caller refused")
	_, _, err = s.RecordReply("nope", "答复", "", jobstore.SessionReplySourceSession, "hook-alice")
	assert.True(t, errors.Is(err, ErrUnknownSession))

	m, created, err := s.RecordReply("sid-r", "  答复\n第二行  ", "uds:/x.sock", jobstore.SessionReplySourceSession, "hook-alice")
	assert.NoErr(t, err)
	assert.True(t, created)
	assert.Eq(t, "答复\n第二行", m.Text)
	assert.Eq(t, jobstore.SessionReplySourceSession, m.Source)
	assert.Eq(t, "uds:/x.sock", m.Peer)
	assert.Eq(t, msg.ID, m.ReplyTo)
	assert.Eq(t, 1, len(replies(t, s, "sid-r")))
}

// Both channels report the same reply: one record, attributed to the session.
func TestRecordReplyDedupesAcrossSources(t *testing.T) {
	s := newSvc(t)
	registerReplySession(t, s, "sid-d", "server", "proj-d", "hook-bob")
	registerReplySession(t, s, "sid-other", "builder", "proj-d", "hook-bob") // same name, another runner

	m, created, err := s.RecordMessengerReply("local", "proj-d", "uds:/m.sock", "同一条回复")
	assert.NoErr(t, err)
	assert.True(t, created)
	assert.Eq(t, "sid-d", m.SessionID)
	assert.Eq(t, jobstore.SessionReplySourceMessenger, m.Source)

	up, created, err := s.RecordReply("sid-d", "同一条回复", "uds:/m.sock", jobstore.SessionReplySourceSession, "hook-bob")
	assert.NoErr(t, err)
	assert.False(t, created)
	assert.Eq(t, m.ID, up.ID)
	got := replies(t, s, "sid-d")
	assert.Eq(t, 1, len(got))
	assert.Eq(t, jobstore.SessionReplySourceSession, got[0].Source, "session report upgrades the messenger one")

	_, created, err = s.RecordMessengerReply("server", "proj-d", "uds:/m.sock", "同一条回复")
	assert.NoErr(t, err)
	assert.False(t, created, "messenger after the hook is a duplicate")
	assert.Eq(t, 1, len(replies(t, s, "sid-d")))
	assert.Eq(t, 0, len(replies(t, s, "sid-other")))

	// Outside the window the same words are a new reply.
	s.nowFn = func() time.Time { return time.Now().Add(replyDedupeWindow + time.Minute) }
	_, created, err = s.RecordReply("sid-d", "同一条回复", "", jobstore.SessionReplySourceSession, "hook-bob")
	assert.NoErr(t, err)
	assert.True(t, created)
}

func TestRecordMessengerReplyNeedsOneLiveSession(t *testing.T) {
	s := newSvc(t)
	_, _, err := s.RecordMessengerReply("local", "ghost", "", "x")
	assert.True(t, errors.Is(err, ErrInvalidInput))
	registerReplySession(t, s, "sid-1", "server", "twin", "a")
	registerReplySession(t, s, "sid-2", "local", "twin", "a")
	_, _, err = s.RecordMessengerReply("local", "twin", "", "x")
	assert.True(t, errors.Is(err, ErrInvalidInput), "ambiguous name is not guessed")
}

func TestRecordReplyCapsText(t *testing.T) {
	s := newSvc(t)
	registerReplySession(t, s, "sid-c", "server", "p", "o")
	m, _, err := s.RecordReply("sid-c", strings.Repeat("长", maxReplyBytes), "", jobstore.SessionReplySourceSession, "o")
	assert.NoErr(t, err)
	assert.True(t, len(m.Text) <= maxReplyBytes)
	assert.True(t, strings.HasSuffix(m.Text, "[已截断]"))
}

// The messenger prompt keeps the delivered line recognisable by the target's hook.
func TestMessengerPromptCarriesWebPrefix(t *testing.T) {
	p := messengerPrompt("proj-a", "alice", "hi")
	assert.True(t, strings.Contains(p, "\n"+WebMessagePrefix+"，alice] hi"))
	assert.True(t, strings.Contains(p, "不要转述"))
}
