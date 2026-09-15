package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gookit/rux/v2"
)

// httpErrorMiddleware records rejected and server-error responses without
// logging query strings or request credentials. It also converts panics into a
// bounded 500 response so the same event describes the failure boundary.
func (s *Server) httpErrorMiddleware(c *rux.Context) {
	started := time.Now()
	panicked := false
	var panicValue any
	func() {
		defer func() {
			if v := recover(); v != nil {
				panicked, panicValue = true, v
			}
		}()
		c.Next()
	}()
	if panicked {
		http.Error(c.Resp, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
	status := c.StatusCode()
	if panicked || status >= 500 || status == http.StatusUnauthorized || status == http.StatusForbidden {
		attrs := []any{"event", "server.http_error", "component", "server", "method", c.Req.Method,
			"path", c.Req.URL.Path, "status", status, "remote", c.Req.RemoteAddr,
			"duration_ms", time.Since(started).Milliseconds()}
		if caller := callerFromCtx(c); caller != "" {
			attrs = append(attrs, "caller", caller)
		}
		if panicked {
			attrs = append(attrs, "error", fmt.Sprint(panicValue))
		}
		slog.Warn("server.http_error", attrs...)
	}
}
