package i18n

import (
	"os"
	"strings"
)

// EnvLanguageVar is the environment variable that overrides the client-side
// language (design §3: GOFER_LANG → client config → system language → en).
const EnvLanguageVar = "GOFER_LANG"

// EnvClientLangVar is the environment key of the CLIENT NODE's language setting
// (design §3). A client node holds no config file — its only config is the .env
// starter — so the `language` line there reaches this process as this variable
// (config.LoadDotenv upper-cases the keys of a .env file, and an exported value
// wins over the file). LANGUAGE is also glibc's "message language" list, which is
// the same preference: the chain in commands' cliLang reads this level below
// GOFER_LANG and above LANG / LC_ALL.
const EnvClientLangVar = "LANGUAGE"

// FromClientConfig reads the client node's configured language, "" when it names
// none or names one gofer has no text for (so the caller falls back to the
// machine's language).
func FromClientConfig() Lang {
	if l, ok := Normalize(os.Getenv(EnvClientLangVar)); ok {
		return l
	}
	return ""
}

// FromEnv reads the language of THIS process's command line output:
// GOFER_LANG, then the system language, then Default.
func FromEnv() Lang {
	if l, ok := Normalize(os.Getenv(EnvLanguageVar)); ok {
		return l
	}
	return Resolve(FromSystem())
}

// FromEnvVar reads only an explicit GOFER_LANG, "" when it is unset or unrecognized.
//
// Text gofer writes for an agent (a hook's injected context, a thin MCP client's
// tool descriptions) follows the deployment's language, not the machine's: a hook on
// an English-locale machine must not switch a zh-CN deployment's text to English just
// because the server has not named a language yet. Such callers use this and fall
// back to Default; FromEnv (with the system language) is for the CLI's own output.
func FromEnvVar() Lang {
	if l, ok := Normalize(os.Getenv(EnvLanguageVar)); ok {
		return l
	}
	return ""
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
