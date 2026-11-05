package sessionrelay

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// The peer address whitelist (design 2026-11-05 「地址校验」): only the two shapes Claude
// Code writes are accepted; everything else is "no address" and the session keeps the
// name path.
func TestValidPeerSocket(t *testing.T) {
	accepted := []string{
		"/run/user/1000/cc-socks/4242.sock",
		"/tmp/cc-socks/1.sock",
		"/home/a.b_c-d/x/cc-socks/99.sock",
		`\\.\pipe\LOCAL\cc-msg-0123456789abcdef0123456789abcdef`,
	}
	for _, in := range accepted {
		assert.Eq(t, in, validPeerSocket(in), "accepted: %q", in)
	}

	rejected := []string{
		"",
		"/var/run/docker.sock",
		"/run/user/1000/cc-socks/4242.sock ",
		" /run/user/1000/cc-socks/4242.sock",
		"/run/user/1000/cc-socks/4242.sock\n",
		`"/run/user/1000/cc-socks/4242.sock"`,
		"/run/user/1000/cc-socks/abc.sock",
		"run/user/1000/cc-socks/4242.sock",
		"/run/user/1000/../cc-socks/4242.sock",
		"/run/../cc-socks/4242.sock",
		`\\.\pipe\LOCAL\cc-msg-0123456789ABCDEF0123456789abcdef`,
		`\\.\pipe\LOCAL\cc-msg-0123`,
		`\\.\pipe\local\cc-msg-0123456789abcdef0123456789abcdef`,
		"/" + strings.Repeat("a/", 200) + "cc-socks/1.sock",
	}
	for _, in := range rejected {
		assert.Eq(t, "", validPeerSocket(in), "rejected: %q", in)
	}
}

// lockedBuffer collects slog output from this test while other tests of the package
// run in parallel.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// swapSlog points the process logger at w for the duration of one test and restores
// the previous default logger afterwards (G053: global state is put back).
func swapSlog(w *lockedBuffer) func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(prev) }
}

// A rejected address is dropped before it is stored, and the warning names the
// session only: the raw value is untrusted text and must not reach the log.
func TestRegisterForOwnerDropsInvalidPeerSocket(t *testing.T) {
	s := newSvc(t)
	const bad = "/var/run/docker.sock?token=secret"
	const good = "/run/user/1000/cc-socks/4242.sock"
	const sid = "peer-bad"

	buf := &lockedBuffer{}
	restore := swapSlog(buf)
	defer restore()

	a, _, err := s.RegisterForOwner(RegisterInput{SessionID: sid, Agent: "claude", Runner: "local",
		CallerID: "alice", PeerName: "proj-4d", PeerMessaging: true, PeerSocket: bad})
	assert.NoErr(t, err)
	assert.Eq(t, "", a.PeerSocket, "an invalid address is stored as no address")
	assert.Eq(t, "proj-4d", a.PeerName, "the name path is untouched")

	logged := buf.String()
	assert.True(t, strings.Contains(logged, sid), "the warning names the session: %s", logged)
	assert.False(t, strings.Contains(logged, bad), "the warning must not carry the raw address: %s", logged)

	// A valid address is stored verbatim.
	a, _, err = s.RegisterForOwner(RegisterInput{SessionID: sid, Agent: "claude", Runner: "local",
		CallerID: "alice", PeerName: "proj-4d", PeerMessaging: true, PeerSocket: good})
	assert.NoErr(t, err)
	assert.Eq(t, good, a.PeerSocket)

	// The beat path validates the same way: an unacceptable value becomes "no
	// address", which clears the stored one.
	badBeat := bad
	a, err = s.HeartbeatForOwner(sid, HeartbeatInput{Event: EventNotification, CallerID: "alice", PeerSocket: &badBeat})
	assert.NoErr(t, err)
	assert.Eq(t, "", a.PeerSocket)
	assert.False(t, strings.Contains(buf.String(), bad), "the beat warning must not carry the raw address either")
}
