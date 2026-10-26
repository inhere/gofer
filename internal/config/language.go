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

// Language is the deployment's text language: server.language, normalized, with
// i18n.Default applied when the config names none. BOTH the stored component
// and the effective value are canonical (en / zh-CN) — a save writes the
// canonical value, an in-memory config reads as one.
func (c *Config) Language() i18n.Lang {
	if c == nil {
		return i18n.Default
	}
	if l, ok := i18n.Normalize(c.Server.Language); ok {
		return l
	}
	return i18n.Default
}

// LanguageFor is the language of the text gofer generates for a job or session
// of project: the project's own language, else the server's.
func (c *Config) LanguageFor(project string) i18n.Lang {
	if c == nil {
		return i18n.Default
	}
	if l := c.Projects[project].EffectiveLanguage(); l != "" {
		return l
	}
	return c.Language()
}

// LanguageUnset reports that server.language names no language, so every text
// runs in the DEPRECATED default (zh-CN for now). The serve startup log and
// `config validate` say so, so the eventual flip to en is not a surprise.
func (c *Config) LanguageUnset() bool {
	return c == nil || strings.TrimSpace(c.Server.Language) == ""
}

// EffectiveLanguage is the project's own text language (i18n.Normalize applied to
// its `language`), or "" when it names none — the caller then uses the server's.
// An unrecognized value also reads as "": validate() rejects it at load, so this
// is only the belt-and-braces read of a config built in code.
func (p ProjectConfig) EffectiveLanguage() i18n.Lang {
	if l, ok := i18n.Normalize(p.Language); ok {
		return l
	}
	return ""
}

// canonicalizeLanguages stores every recognized language value in its canonical
// form (en / zh-CN), so what an in-memory config hands to a save is canonical
// too. An unrecognized value is left as written for validate to report.
func canonicalizeLanguages(cfg *Config) {
	if l, ok := i18n.Normalize(cfg.Server.Language); ok {
		cfg.Server.Language = string(l)
	}
	for key, p := range cfg.Projects {
		if l, ok := i18n.Normalize(p.Language); ok {
			p.Language = string(l)
			cfg.Projects[key] = p
		}
	}
}

// validateLanguage rejects a language value gofer has no text for, naming the
// values it does have. An empty value is legal: it means the DEPRECATED default
// (the startup log and `config validate` point it out instead of failing).
func validateLanguage(cfg *Config) error {
	if raw := strings.TrimSpace(cfg.Server.Language); raw != "" {
		if _, ok := i18n.Normalize(raw); !ok {
			return fmt.Errorf("server.language %q is not recognized (available: %s)", raw, i18n.List())
		}
	}
	for _, key := range slices.Sorted(maps.Keys(cfg.Projects)) {
		raw := strings.TrimSpace(cfg.Projects[key].Language)
		if raw == "" {
			continue
		}
		if _, ok := i18n.Normalize(raw); !ok {
			return fmt.Errorf("project %q: language %q is not recognized (available: %s)", key, raw, i18n.List())
		}
	}
	return nil
}
