package i18n

import (
	"embed"
	"fmt"
	"strings"

	gookit "github.com/gookit/i18n"
)

// Short texts: internal/i18n/lang/<lang>/<domain>.ini, one file per domain
// (job.ini, mail.ini, cli.ini …). A key is "<domain>.<area>.<name>"; the INI
// section carries the prefix ("job.delivery.task" lives in [job.delivery] as
// task = …), because gookit/ini addresses values section-wise.
//
// Placeholders are named ({name}) and filled from the caller's key/value args, so
// a language may reorder a sentence freely (gookit's ReplaceMode semantics).
//
//go:embed lang
var langFS embed.FS

// translator is the loaded catalog. Init panics on a missing or unparsable
// file, which is what makes a broken catalog a STARTUP error rather than a
// wrong string at run time.
var translator = newTranslator()

func newTranslator() *gookit.I18n {
	tr := gookit.NewWithFS(langFS, "lang", string(EN), map[string]string{
		string(EN):   "English",
		string(ZhCN): "简体中文",
	})
	tr.LoadMode = gookit.DirMode
	tr.TransMode = gookit.ReplaceMode
	tr.FallbackLang = string(EN)
	return tr.Init()
}

// T returns the text of key in lang. args fill the {name} placeholders: either a
// map[string]any or a key/value sequence (T(lang, "k", "n", 3)).
//
// An unknown key returns the key itself, and a key missing in lang falls back to
// English — the key-set test keeps both catalogs equal.
func T(lang Lang, key string, args ...any) string {
	l := Resolve(lang)
	msg, ok := lookup(l, key)
	if !ok {
		msg, ok = lookup(EN, key)
	}
	if !ok {
		return key
	}
	return fill(msg, args...)
}

// HasKey reports whether lang's catalog carries key.
func HasKey(lang Lang, key string) bool {
	_, ok := lookup(Resolve(lang), key)
	return ok
}

// lookup finds key in one language's catalog. gookit's own dotted lookup does not
// reach a key under a [section], so the section prefix is resolved here: try the
// whole key, then every shorter prefix as the section.
func lookup(lang Lang, key string) (string, bool) {
	data := translator.Lang(string(lang))
	if data == nil {
		return "", false
	}
	if v, ok := data.GetValue(key); ok && v != "" {
		return v, true
	}
	sections := data.Data()
	for i := strings.LastIndexByte(key, '.'); i > 0; i = strings.LastIndexByte(key[:i], '.') {
		if sec, ok := sections[key[:i]]; ok {
			if v, ok := sec[key[i+1:]]; ok && v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// fill replaces the {name} placeholders of msg (gookit's ReplaceMode, applied
// here because the message was resolved by lookup rather than by gookit).
func fill(msg string, args ...any) string {
	if len(args) == 0 {
		return msg
	}
	var pairs []string
	add := func(k, v any) {
		pairs = append(pairs, "{"+fmt.Sprint(k)+"}", fmt.Sprint(v))
	}
	switch a := args[0].(type) {
	case map[string]any:
		for k, v := range a {
			add(k, v)
		}
	case map[string]string:
		for k, v := range a {
			add(k, v)
		}
	default:
		for i := 0; i+1 < len(args); i += 2 {
			add(args[i], args[i+1])
		}
	}
	return strings.NewReplacer(pairs...).Replace(msg)
}
