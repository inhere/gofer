package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"
)

// errorBody is the uniform error response shape (plan §7). It deliberately does
// NOT use the company {status,code,message} envelope.
type errorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

// writeError encodes an errorBody at the given HTTP status. msg is the short
// machine-ish summary; detail carries the human-readable specifics.
func writeError(c *rux.Context, status int, msg, detail string) {
	c.JSON(status, errorBody{Error: msg, Detail: detail})
}

// writeRateLimited encodes the E17 over-rate response (429, design §7.3). The
// caller is named in the detail (no secret/token — SR403) so an operator can see
// which identity tripped the limit. The Retry-After header is set by the caller
// (rateLimitMiddleware) before this.
func writeRateLimited(c *rux.Context, caller string) {
	writeError(c, http.StatusTooManyRequests, "rate limited", "caller "+caller+" exceeded its submit rate; retry shortly")
}
