package sessionrelay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
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
	msg, err := s.SendMessage(context.Background(), "sid-r", "进度如何", "alice", "alice")
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
	_, _, err = s.RecordMessengerReply("local", "twin", "", " ")
	assert.True(t, errors.Is(err, ErrInvalidInput), "an empty body is refused before any lookup")
}

// A reply names the address the messenger sent the text to, and the address — not a
// shared name — says which session answered (design 「撞名时发消息」 step 4): the human
// picked THAT process, so its words must not show up under the other one.
func TestRecordMessengerReplyAttributesByAddress(t *testing.T) {
	const addr = "/run/user/1000/cc-socks/4242.sock"

	t.Run("an address wins over an ambiguous name", func(t *testing.T) {
		s := newSvc(t)
		// Two live sessions answer to "twin"; only sid-a reported the address.
		registerPeerSession(t, s, "sid-a", "server", "twin", addr)
		registerPeerSession(t, s, "sid-b", "local", "twin", "")

		m, created, err := s.RecordMessengerReply("local", "twin", "uds:"+addr, "答")
		assert.NoErr(t, err)
		assert.True(t, created)
		assert.Eq(t, "sid-a", m.SessionID)
		assert.Eq(t, 0, len(replies(t, s, "sid-b")), "the namesake must stay untouched")
	})

	t.Run("an address without a name is enough", func(t *testing.T) {
		s := newSvc(t)
		registerPeerSession(t, s, "sid-a", "local", "twin", addr)

		_, _, err := s.RecordMessengerReply("local", "", "uds:"+addr, "答")
		assert.NoErr(t, err)
		assert.Eq(t, 1, len(replies(t, s, "sid-a")))
	})

	t.Run("an unknown address falls back to the name", func(t *testing.T) {
		s := newSvc(t)
		registerPeerSession(t, s, "sid-a", "local", "solo", addr)

		m, created, err := s.RecordMessengerReply("local", "solo", "uds:/run/user/1000/cc-socks/9.sock", "答")
		assert.NoErr(t, err)
		assert.True(t, created)
		assert.Eq(t, "sid-a", m.SessionID, "the name still finds the only live session of that name")
	})

	t.Run("an address on another runner does not attribute", func(t *testing.T) {
		s := newSvc(t)
		// The messenger reports for "local", but the row holding the address belongs to
		// another runner (an old row of a worker): its words must not be attached here.
		registerPeerSession(t, s, "sid-w", "builder", "twin", addr)
		registerPeerSession(t, s, "sid-a", "local", "twin", "")

		m, created, err := s.RecordMessengerReply("local", "twin", "uds:"+addr, "答")
		assert.NoErr(t, err)
		assert.True(t, created)
		assert.Eq(t, "sid-a", m.SessionID)
		assert.Eq(t, 0, len(replies(t, s, "sid-w")))
	})

	t.Run("an invalid address falls back to the name", func(t *testing.T) {
		s := newSvc(t)
		registerPeerSession(t, s, "sid-a", "server", "twin", addr)

		m, created, err := s.RecordMessengerReply("local", "twin", "uds:/var/run/docker.sock", "答")
		assert.NoErr(t, err)
		assert.True(t, created)
		assert.Eq(t, "sid-a", m.SessionID)
	})
}

func TestRecordReplyCapsText(t *testing.T) {
	s := newSvc(t)
	registerReplySession(t, s, "sid-c", "server", "p", "o")
	m, _, err := s.RecordReply("sid-c", strings.Repeat("长", maxReplyBytes), "", jobstore.SessionReplySourceSession, "o")
	assert.NoErr(t, err)
	assert.True(t, len(m.Text) <= maxReplyBytes)
	assert.True(t, strings.HasSuffix(m.Text, "[已截断]"))

	// The marker is stored with the reply: it follows the session's project language.
	s.SetLanguageFor(func(string) i18n.Lang { return i18n.EN })
	registerReplySession(t, s, "sid-e", "server", "p2", "o")
	m, _, err = s.RecordReply("sid-e", strings.Repeat("x", maxReplyBytes+10), "", jobstore.SessionReplySourceSession, "o")
	assert.NoErr(t, err)
	assert.True(t, strings.HasSuffix(m.Text, "\n[truncated]"))
}

// The messenger prompt keeps the delivered line recognisable by the target's hook.
func TestMessengerPromptCarriesWebPrefix(t *testing.T) {
	p := messengerPrompt(i18n.ZhCN, messengerTarget{Name: "proj-a"}, "alice", "hi")
	assert.True(t, strings.Contains(p, "\n"+WebMessagePrefix+"，alice] hi"))
	assert.True(t, strings.Contains(p, "不要转述"))
}
