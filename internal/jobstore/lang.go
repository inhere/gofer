package jobstore

import "github.com/inhere/gofer/internal/i18n"

// LanguageFor resolves the language of the text the store writes about a project (a work
// item's journal line, a note on a decision): the project's language, else the server's.
// The store has no config of its own, so the wiring supplies it (SetLanguageFor).
type LanguageFor func(project string) i18n.Lang

// SetLanguageFor installs the project language resolver (config.Config.LanguageFor).
// Without one the store writes i18n.Default (zh-CN), which is what tests that pin the
// text rely on. Safe to call while the store is in use: the wiring replaces it when the
// config reloads.
func (s *Store) SetLanguageFor(f LanguageFor) {
	if f == nil {
		s.languageFor.Store(nil)
		return
	}
	s.languageFor.Store(&f)
}

// textLang is the language of text stored for project ("" = the server's language).
func (s *Store) textLang(project string) i18n.Lang {
	if f := s.languageFor.Load(); f != nil {
		if l := (*f)(project); l != "" {
			return l
		}
	}
	return i18n.Default
}

// workLang is the language of text written about the work item id: its project's. It
// reads project_key on its own (the writer lock is held by the callers, so the item
// cannot change underneath it).
func (s *Store) workLang(id string) i18n.Lang {
	var project string
	_ = s.db.QueryRow(`SELECT project_key FROM work_items WHERE id = ?`, id).Scan(&project)
	return s.textLang(project)
}
