package sessionrelay

import (
	"log/slog"
	"regexp"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// The session peer address (design 2026-11-05 「地址校验」). A Claude Code session's
// messaging endpoint is written verbatim into a messenger prompt, so the server — not
// the hook — decides what may be stored: only the two shapes Claude Code produces are
// accepted, and anything else is treated as "no address" (the session then keeps the
// name path, exactly as before the column existed). An address that passed this
// whitelist cannot point at another socket (docker.sock), and cannot carry quotes,
// whitespace or a newline that would turn the prompt into something else.

// maxPeerSocketLen bounds a stored address (a unix socket path plus a pid).
const maxPeerSocketLen = 256

var (
	// unixPeerSocket: absolute path whose segments are [A-Za-z0-9._-], ending in
	// /cc-socks/<pid>.sock — Claude Code's per-process socket.
	unixPeerSocket = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)*/cc-socks/[0-9]+\.sock$`)
	// windowsPeerSocket: the named pipe Claude Code listens on. The hex is
	// lower-case; a different case is a value Claude Code never writes.
	windowsPeerSocket = regexp.MustCompile(`^\\\\\.\\pipe\\LOCAL\\cc-msg-[0-9a-f]{32}$`)
)

// validPeerSocket answers raw when it is one of the accepted shapes, else "". It does
// not trim: a value with surrounding whitespace did not come from a session file, so
// it is not an address. The caller stores the returned value only.
func validPeerSocket(raw string) string {
	if raw == "" || len(raw) > maxPeerSocketLen {
		return ""
	}
	if windowsPeerSocket.MatchString(raw) {
		return raw
	}
	if !unixPeerSocket.MatchString(raw) {
		return ""
	}
	// `..` is inside the character class, so the shape alone would let a path walk
	// out of the directory it claims to be in.
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return ""
		}
	}
	return raw
}

// checkPeerSocket is the hook path's validation: an address that does not pass the
// whitelist is dropped, and the warning names the session only — the raw value never
// reaches the log (it is a capability, and a rejected one is by definition untrusted
// text from outside).
func checkPeerSocket(sid, raw string) string {
	if raw == "" {
		return ""
	}
	if valid := validPeerSocket(raw); valid != "" {
		return valid
	}
	slog.Warn("sessionrelay: dropped an invalid session peer address", "session_id", sid)
	return ""
}

// messengerTarget is where a messenger is told to send one session's text (design
// 2026-11-05 「提示词」): the session's name, its peer address when it reported one,
// and whether the prompt may fall back to the name.
//
// Fallback only means something together with an address: without one the name IS the
// target, so there is nothing to fall back from.
type messengerTarget struct {
	Name     string
	Socket   string
	Fallback bool
}

// messengerTargetFor decides the target for a session that is reached through the
// messenger. An address that no longer passes validPeerSocket (a row written before
// the server validated, or a value that changed shape since) counts as no address, so
// such a session keeps the name path exactly as it was before the column existed.
//
// The prompt may name the fallback only when no OTHER live session of the same runner
// carries the same name (design 「地址过期」): the human picked THIS process, and a
// name that is ambiguous would deliver into the wrong one. A row without a runner does
// not take part — it never said where it runs (plan R2) — and a lookup that fails
// counts as "a namesake exists" (no fallback), because guessing there delivers into the
// wrong session. The warning carries the session id only: the address is a capability
// and never reaches the log.
func (s *Service) messengerTargetFor(a jobstore.AgentSession) messengerTarget {
	t := messengerTarget{Name: strings.TrimSpace(a.PeerName), Socket: validPeerSocket(a.PeerSocket)}
	if t.Socket == "" || t.Name == "" {
		return t
	}
	others, err := s.store.ListLiveSessionsByPeerName(t.Name)
	if err != nil {
		slog.Warn("sessionrelay: list live sessions by peer name failed", "session_id", a.SessionID, "err", err)
		return t
	}
	t.Fallback = true
	for _, other := range others {
		if other.SessionID == a.SessionID || strings.TrimSpace(other.Runner) == "" || strings.TrimSpace(a.Runner) == "" {
			continue
		}
		if s.sameRunner(other.Runner, a.Runner) {
			t.Fallback = false
			break
		}
	}
	return t
}
