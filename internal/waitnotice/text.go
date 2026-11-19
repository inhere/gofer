package waitnotice

import (
	"strings"

	"github.com/inhere/gofer/internal/i18n"
)

// Title is the one line every channel shows for n: "<name> is waiting for your answer".
// It is built from the name and the category only; nothing a turn said can reach it.
func Title(lang i18n.Lang, n Notice) string {
	key := "waitnotice.title.other"
	switch n.Category {
	case "answer", "approval", "review":
		key = "waitnotice.title." + n.Category
	}
	title := i18n.T(lang, key, "label", label(n.Subject))
	if n.Reminder {
		title = i18n.T(lang, "waitnotice.title.reminder", "title", title)
	}
	return title
}

// label is what the person knows the subject by.
func label(s Subject) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	_, ref, _ := strings.Cut(s.Key, ":")
	if len(ref) > 8 {
		ref = ref[:8]
	}
	return ref
}
