package sessionrelay

import (
	"log/slog"
	"regexp"
	"strings"
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
