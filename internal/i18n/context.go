package i18n

import "context"

// langKey is the context key of a request's text language.
type langKey struct{}

// WithLang returns ctx carrying lang as the language of the text generated for
// this request (design 2026-10-27-i18n-design.md §3: Accept-Language, else
// server.language). lang is normalized; a value nobody understands leaves ctx
// unchanged, so FromContext keeps answering "none".
func WithLang(ctx context.Context, lang Lang) context.Context {
	l, ok := Normalize(string(lang))
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, langKey{}, l)
}

// FromContext is the request language WithLang stored, or "" when ctx carries
// none (a background job, a notification, a timer — callers then use the
// project's or the server's language). Business code takes the language from
// its ctx with this and never imports the HTTP layer.
func FromContext(ctx context.Context) Lang {
	if ctx == nil {
		return ""
	}
	l, _ := ctx.Value(langKey{}).(Lang)
	return l
}

// FromContextOr is FromContext with a fallback for a ctx that carries none.
func FromContextOr(ctx context.Context, fallback Lang) Lang {
	if l := FromContext(ctx); l != "" {
		return l
	}
	return fallback
}
