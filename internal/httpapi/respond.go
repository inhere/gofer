package httpapi

import "github.com/gookit/rux/v2"

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
