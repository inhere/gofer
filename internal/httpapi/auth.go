package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
)

// bearerPrefix is the Authorization scheme prefix (case-insensitive match).
const bearerPrefix = "Bearer "

// authMiddleware guards the /v1 group. Behaviour (plan §7, §11):
//
//   - Effective token empty + allow_empty_token: requests pass through (the
//     operator explicitly opted out of auth; serve also allows startup).
//   - Effective token empty + NOT allowed: every request is rejected 401. The
//     serve command refuses to start in this state, so this is belt-and-braces
//     for direct New() callers (e.g. tests).
//   - Effective token set: the request must present
//     `Authorization: Bearer <token>` with an exact token match, else 401.
//
// The token is never written to logs or to the response body (plan §11).
func (s *Server) authMiddleware(c *rux.Context) {
	if s.token == "" {
		if s.allowEmptyToken {
			c.Next()
			return
		}
		writeError(c, http.StatusUnauthorized, "unauthorized", "server has no token configured")
		c.Abort()
		return
	}

	got, ok := bearerToken(c.Req.Header.Get("Authorization"))
	if !ok || got != s.token {
		writeError(c, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		c.Abort()
		return
	}
	c.Next()
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
