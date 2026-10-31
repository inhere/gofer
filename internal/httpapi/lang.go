package httpapi

import (
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/i18n"
)

// requestLangMiddleware resolves the language of the text this request's answer
// carries (design 2026-10-27-i18n-design.md §3, "server-generated text shown in
// the web"): the first language of the Accept-Language header gofer understands
// (q order, i18n.FromAcceptLanguage), else server.language (its auto resolved by
// config). The result rides the request context (i18n.WithLang), so business code
// reads it with i18n.FromContext and never imports this package.
func (s *Server) requestLangMiddleware(c *rux.Context) {
	lang := i18n.FromAcceptLanguage(c.Req.Header.Get("Accept-Language"))
	if lang == "" {
		lang = s.textLanguage("")
	}
	c.Req = c.Req.WithContext(i18n.WithLang(c.Req.Context(), lang))
	c.Next()
}

// requestLang is the text language of the request in c: what requestLangMiddleware
// stored, else the server's language (a handler reached without the middleware).
func (s *Server) requestLang(c *rux.Context) i18n.Lang {
	return i18n.FromContextOr(c.Req.Context(), s.textLanguage(""))
}
