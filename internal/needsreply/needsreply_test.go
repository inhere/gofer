package needsreply

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		name, in, body string
		needs          bool
	}{
		{"last line marker", "which plan?\n[needs-reply]", "which plan?", true},
		{"marker with surrounding spaces", "which plan?\n  [needs-reply]  ", "which plan?", true},
		{"trailing newlines after marker", "which plan?\n[needs-reply]\n\n", "which plan?", true},
		{"blank lines before marker are dropped", "which plan?\n\n\n[needs-reply]", "which plan?", true},
		{"marker in the middle is prose", "[needs-reply]\nmore text", "[needs-reply]\nmore text", false},
		{"marker followed by text on its line", "ok\n[needs-reply] please", "ok\n[needs-reply] please", false},
		{"case differs", "ok\n[Needs-Reply]", "ok\n[Needs-Reply]", false},
		{"only the marker", "[needs-reply]", "", true},
		{"no marker", "all done", "all done", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, needs := Split(c.in)
			assert.Eq(t, c.body, body)
			assert.Eq(t, c.needs, needs)
		})
	}
}

func TestReminderNamesMarker(t *testing.T) {
	assert.StrContains(t, Reminder, Marker)
}
