package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
)

// bearerPrefix is the Authorization scheme prefix (case-insensitive match).
const bearerPrefix = "Bearer "

// authMiddleware guards the /v1 group. Behaviour (plan §7, §11, C2):
//
//   - No tokens configured + allow_empty_token: requests pass through with an
//     empty caller id (the operator explicitly opted out of auth; serve also
//     allows startup).
//   - No tokens configured + NOT allowed: every request is rejected 401. The
//     serve command refuses to start in this state, so this is belt-and-braces
//     for direct New() callers (e.g. tests).
//   - Tokens configured: the request must present `Authorization: Bearer
//     <token>` matching a known caller (constant-time compare), else 401. The
//     matched caller id is stored in the rux context for handlers.
//
// The token is never written to logs or to the response body (plan §11).
func (s *Server) authMiddleware(c *rux.Context) {
	if len(s.callers) == 0 {
		if s.allowEmptyToken {
			c.Set(ctxCallerID, "")
			c.Next()
			return
		}
		writeError(c, http.StatusUnauthorized, "unauthorized", "server has no token configured")
		c.Abort()
		return
	}

	got, ok := bearerToken(c.Req.Header.Get("Authorization"))
	caller, matched := "", false
	if ok {
		caller, matched = s.lookupCaller(got)
	}
	if !matched {
		writeError(c, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		c.Abort()
		return
	}
	c.Set(ctxCallerID, caller)
	c.Next()
}

// lookupCaller constant-time compares the presented bearer token against every
// known caller token and returns the matching caller id. It scans ALL callers
// even after a match so the comparison cost does not leak which (or whether a)
// token matched. The first match wins (see buildCallers ordering).
func (s *Server) lookupCaller(token string) (string, bool) {
	caller, matched := "", false
	for _, ce := range s.callers {
		if subtle.ConstantTimeCompare([]byte(token), []byte(ce.token)) == 1 && !matched {
			caller, matched = ce.id, true
		}
	}
	return caller, matched
}

// bearerToken extracts the token from an `Authorization: Bearer <token>` header.
// The scheme match is case-insensitive; the token itself is returned verbatim.
// ok is false when the header is empty or not a Bearer credential.
func bearerToken(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	if len(header) < len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
