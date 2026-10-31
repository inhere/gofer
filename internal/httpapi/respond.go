package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/i18n"
)

// errorBody is the uniform error response shape (plan §7). It deliberately does
// NOT use the company {status,code,message} envelope.
type errorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
	// Code is an optional stable identifier for errors a client branches on. A
	// keyed error (writeErrorKey) always carries one: the console looks the text up
	// as err.<code> in its own dictionary and falls back to Detail.
	Code string `json:"code,omitempty"`
	// Params fills the {name} placeholders of the err.<code> text, so a client can
	// render the sentence in its own language. Only keyed errors have it.
	Params map[string]any `json:"params,omitempty"`
}

// apiError is an error response decided below the handler: a helper that returns
// it leaves the writing (and the request language it was built with) to the caller.
type apiError struct {
	Status int
	// Msg is the short `error` phrase (machine-ish; scripts match on it).
	Msg string
	// Code and Params are the keyed form (see errorBody); Detail is the text already
	// rendered in the request language.
	Code   string
	Params map[string]any
	Detail string
}

// newAPIError builds a keyed error: the detail is the catalog text err.<code> in
// lang, args (name, value pairs) fill its placeholders and travel as Params.
func newAPIError(lang i18n.Lang, status int, msg, code string, args ...any) *apiError {
	e := &apiError{Status: status, Msg: msg, Code: code, Detail: i18n.T(lang, errKeyPrefix+code, args...)}
	for i := 0; i+1 < len(args); i += 2 {
		if name, ok := args[i].(string); ok {
			if e.Params == nil {
				e.Params = map[string]any{}
			}
			e.Params[name] = args[i+1]
		}
	}
	return e
}

// errKeyPrefix is the catalog domain of the texts keyed errors carry: the Go side is
// internal/i18n/lang/*/err.ini, the console's copy is web/src/i18n/pages/errors.ts.
const errKeyPrefix = "err."

// writeAPIError writes e.
func writeAPIError(c *rux.Context, e *apiError) {
	c.JSON(e.Status, errorBody{Error: e.Msg, Detail: e.Detail, Code: e.Code, Params: e.Params})
}

// writeErrorKey is writeError for an error with a stable machine code: the detail is
// the text err.<code> in the request language (i18n.FromContext, set by
// requestLangMiddleware), args fill its {name} placeholders. msg stays the fixed
// short phrase scripts match on. The code and the args ride in the body, so the
// console can render the sentence from its own dictionary.
func (s *Server) writeErrorKey(c *rux.Context, status int, msg, code string, args ...any) {
	writeAPIError(c, newAPIError(s.requestLang(c), status, msg, code, args...))
}

// errText is the catalog text err.<name> in the request language, for the parts of
// a response that are not the detail (a Chinese-era `error` phrase, a warning, a
// label).
func (s *Server) errText(c *rux.Context, name string, args ...any) string {
	return i18n.T(s.requestLang(c), errKeyPrefix+name, args...)
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
