package team

import (
	"slices"
	"testing"
)

// gofer-x6v6: an `@name` is a mention only where a person would mean one.
func TestMentionedNames(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       []string
	}{
		{"plain", "please ask @alice about it", []string{"alice"}},
		{"start of body", "@alice go", []string{"alice"}},
		{"two", "@alice and @bob (not @carol.) done", []string{"alice", "bob", "carol"}},
		{"sentence end", "thanks @alice.", []string{"alice"}},
		{"cjk name", "交给 @小明 处理", []string{"小明"}},
		{"e-mail is not a mention", "mail user@alice.example.com or a.b+tag@bob.io", nil},
		{"e-mail then mention", "mail user@host then @dave", []string{"dave"}},
		{"inline code", "run `git log @alice` first", nil},
		{"inline code then mention", "run `x @alice` and ping @bob", []string{"bob"}},
		{"fenced block", "see:\n```\nssh @alice\n@bob\n```\nthen @carol", []string{"carol"}},
		{"unterminated fence", "ok @alice\n```\n@bob never closed", []string{"alice"}},
		{"glued", "x@alice", nil},
	} {
		if got := mentionedNames(c.body); !slices.Equal(got, c.want) {
			t.Errorf("%s: mentionedNames(%q) = %v, want %v", c.name, c.body, got, c.want)
		}
	}
}
