package commands

import "github.com/inhere/gofer/internal/i18n"

// cliLang is the ONE place a command decides which language its own text is in
// (design 2026-10-27-i18n-design.md §3): GOFER_LANG, then the client node's
// configured language, then the machine's language, then the default.
//
// It reads the environment on every call instead of caching, so a gcli command
// tree built after t.Setenv in a test, or after config.LoadDotenv in main, sees
// the language of that moment. Command descriptions and flag usages are therefore
// resolved while the tree is BUILT (NewApp), never in an init().
//
// Text gofer prints for an agent to read (repo prime, issue brief) does NOT use
// this: it follows the deployment's language (§3, i18n.FromEnvVar).
func cliLang() i18n.Lang {
	if l := i18n.FromEnvVar(); l != "" { // GOFER_LANG
		return l
	}
	if l := i18n.FromClientConfig(); l != "" { // client config: .env `language`
		return l
	}
	return i18n.FromSystem() // machine locale, else the default
}

// cliText returns the CLI text of key in cliLang(), its {name} placeholders filled
// from args (i18n.T semantics).
func cliText(key string, args ...any) string {
	return i18n.T(cliLang(), key, args...)
}
