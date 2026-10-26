package mcpserver

// The language of this MCP server's own text (design 2026-10-27-i18n-design.md §3 /
// §4, P1d): the tool descriptions an agent reads at tools/list, and the answers it
// gets back. The text itself is in internal/i18n — the short-text catalog for the
// descriptions that carry language-specific wording, and the Go literals for the
// descriptions that are the same sentence in every language.
//
// Descriptions are fixed when the server is built (every tool is registered once), so
// a language change takes a new MCP session — which the agent starts anyway.

import "github.com/inhere/gofer/internal/i18n"

// languageReporter is a Backend that knows the deployment's language: the local
// backend reads it from the server config it was built from.
type languageReporter interface {
	Language() i18n.Lang
}

// backendLang is the language b's text is written in.
func backendLang(b Backend) i18n.Lang {
	if lr, ok := b.(languageReporter); ok {
		return i18n.Resolve(lr.Language())
	}
	// A backend that cannot tell — the thin client forwarding to a central serve: the
	// deployment's server.language does not reach this process (GET /v1/meta carries no
	// language yet). Use the chain the CLI uses: GOFER_LANG -> system language -> default.
	return i18n.FromEnv()
}

// text looks key up in b's language: the one way a description or an answer an agent
// reads gets its language-specific wording.
func text(b Backend, key string, args ...any) string {
	return i18n.T(backendLang(b), key, args...)
}
