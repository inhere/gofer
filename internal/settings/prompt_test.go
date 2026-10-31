package settings

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
)

func TestChoosePrompt(t *testing.T) {
	assert.Eq(t, "default", ChoosePrompt("default", ""))
	assert.Eq(t, "default", ChoosePrompt("default", "  \n\t "))
	assert.Eq(t, "mine", ChoosePrompt("default", "mine"))
	// A replacement is kept verbatim (no trimming): the text is the prompt.
	assert.Eq(t, " mine ", ChoosePrompt("default", " mine "))
}

func TestComposePrompt(t *testing.T) {
	assert.Eq(t, "base", ComposePrompt(i18n.ZhCN, "base", ""))
	assert.Eq(t, "base", ComposePrompt(i18n.ZhCN, "base", "  \n"))
	assert.Eq(t, "base\n\n"+ExtraHeadingIn(i18n.ZhCN)+"\nmore", ComposePrompt(i18n.ZhCN, "base", "\nmore\n"))
	// A trailing newline of the body does not leave a blank line under it.
	assert.Eq(t, "base\n\n"+ExtraHeadingIn(i18n.ZhCN)+"\nmore", ComposePrompt(i18n.ZhCN, "base\n", "more"))
	assert.True(t, strings.HasSuffix(ComposePrompt(i18n.ZhCN, "base", "more"), "more"))
}

// TestExtraHeadingFollowsLanguage pins the zh-CN heading to the literal it was before it
// moved into the catalog, and checks the English one carries no CJK.
func TestExtraHeadingFollowsLanguage(t *testing.T) {
	assert.Eq(t, "## 使用者补充", ExtraHeadingIn(i18n.ZhCN))
	en := ExtraHeadingIn(i18n.EN)
	assert.True(t, strings.HasPrefix(en, "## ") && en != ExtraHeadingIn(i18n.ZhCN))
	for _, r := range en {
		assert.False(t, r >= 0x4e00 && r <= 0x9fff, "CJK in the English heading")
	}
	assert.Eq(t, "base\n\n"+en+"\nmore", ComposePrompt(i18n.EN, "base", "more"))
}
