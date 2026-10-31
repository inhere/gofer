package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
)

// language.go is the one resolution of the configured text language (design
// 2026-10-27-i18n-design.md §3): server.language, overridden per project, both
// read through i18n.Normalize so a config value is never a raw alias.

// Language is the deployment's text language: server.language, normalized. An
// unset value or "auto" is resolved by i18n.Auto() — the server machine's system
// language, else en. BOTH the stored component and the effective value are
// canonical (en / zh-CN, or "auto" in the stored form) — a save writes the
// canonical value, an in-memory config reads as one.
func (c *Config) Language() i18n.Lang {
	if c == nil {
		return i18n.Auto()
	}
	if l, ok := i18n.Normalize(c.Server.Language); ok {
		return l
	}
	return i18n.Auto()
}

// LanguageFor is the language of the text gofer generates for a job or session
// of project: the project's own language (an explicit "auto" there reads the
// machine's), else the server's.
func (c *Config) LanguageFor(project string) i18n.Lang {
	if c == nil {
		return i18n.Auto()
	}
	if l := c.Projects[project].EffectiveLanguage(); l != "" {
		return l
	}
	return c.Language()
}

// LanguageUnset reports that server.language names no language, so the language
// is chosen automatically (same as "auto"). The serve startup log and
// `config validate` say so, with the language that was picked.
func (c *Config) LanguageUnset() bool {
	return c == nil || strings.TrimSpace(c.Server.Language) == ""
}

// LanguageSource says where the server's effective language came from: "config"
// (pinned to en / zh-CN), "auto" (server.language: auto) or "unset" (nothing
// configured, which reads as auto). Auto and unset follow the system language.
func (c *Config) LanguageSource() string {
	switch {
	case c == nil || c.LanguageUnset():
		return "unset"
	case strings.EqualFold(strings.TrimSpace(c.Server.Language), i18n.AutoValue):
		return "auto"
	default:
		return "config"
	}
}

// EffectiveLanguage is the project's own text language (i18n.Normalize applied to
// its `language`; an explicit "auto" is the machine's language), or "" when it
// names none — the caller then uses the server's. An unrecognized value also
// reads as "": validate() rejects it at load, so this is only the belt-and-braces
// read of a config built in code.
func (p ProjectConfig) EffectiveLanguage() i18n.Lang {
	if strings.EqualFold(strings.TrimSpace(p.Language), i18n.AutoValue) {
		return i18n.Auto()
	}
	if l, ok := i18n.Normalize(p.Language); ok {
		return l
	}
	return ""
}

// canonicalizeLanguages stores every recognized language value in its canonical
// form (en / zh-CN), so what an in-memory config hands to a save is canonical
// too. An unrecognized value is left as written for validate to report.
func canonicalizeLanguages(cfg *Config) {
	cfg.Server.Language = canonicalLanguage(cfg.Server.Language)
	for key, p := range cfg.Projects {
		if v := canonicalLanguage(p.Language); v != p.Language {
			p.Language = v
			cfg.Projects[key] = p
		}
	}
}

// canonicalLanguage is a language value in its stored form: "auto" for any
// spelling of it, the canonical tag for a recognized language, else unchanged.
func canonicalLanguage(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), i18n.AutoValue) {
		return i18n.AutoValue
	}
	if l, ok := i18n.Normalize(raw); ok {
		return string(l)
	}
	return raw
}

// validateLanguage rejects a language value gofer has no text for, naming the
// values it does have. An empty value and "auto" are legal: both pick the
// system language (the startup log and `config validate` say which).
func validateLanguage(cfg *Config) error {
	if raw := strings.TrimSpace(cfg.Server.Language); raw != "" {
		if _, ok := i18n.Normalize(raw); !ok && !i18n.IsAuto(raw) {
			return fmt.Errorf("server.language %q is not recognized (available: %s, %s)", raw, i18n.AutoValue, i18n.List())
		}
	}
	for _, key := range slices.Sorted(maps.Keys(cfg.Projects)) {
		raw := strings.TrimSpace(cfg.Projects[key].Language)
		if raw == "" {
			continue
		}
		if _, ok := i18n.Normalize(raw); !ok && !i18n.IsAuto(raw) {
			return fmt.Errorf("project %q: language %q is not recognized (available: %s, %s)", key, raw, i18n.AutoValue, i18n.List())
		}
	}
	return nil
}
