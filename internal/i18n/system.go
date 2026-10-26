package i18n

import (
	"os"
	"strings"
)

// EnvLanguageVar is the environment variable that overrides the client-side
// language (design §3: GOFER_LANG → client config → system language → en).
const EnvLanguageVar = "GOFER_LANG"

// FromEnv reads the language of THIS process's command line output:
// GOFER_LANG, then the system language, then Default.
func FromEnv() Lang {
	if l, ok := Normalize(os.Getenv(EnvLanguageVar)); ok {
		return l
	}
	return Resolve(FromSystem())
}

// FromSystem reads the machine's language: LANG / LC_ALL / LC_MESSAGES and, on
// Windows, the user's preferred UI language. It returns "" when none of them
// names a language gofer has text for, so the caller falls back to its next
// source.
func FromSystem() Lang {
	return Resolve(systemLanguages()...)
}

// envLanguages are the POSIX locale variables, highest priority first.
func envLanguages() []Lang {
	out := make([]Lang, 0, 3)
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l, ok := Normalize(strings.TrimSpace(os.Getenv(key))); ok {
			out = append(out, l)
		}
	}
	return out
}
