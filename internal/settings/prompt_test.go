package settings

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestChoosePrompt(t *testing.T) {
	assert.Eq(t, "default", ChoosePrompt("default", ""))
	assert.Eq(t, "default", ChoosePrompt("default", "  \n\t "))
	assert.Eq(t, "mine", ChoosePrompt("default", "mine"))
	// A replacement is kept verbatim (no trimming): the text is the prompt.
	assert.Eq(t, " mine ", ChoosePrompt("default", " mine "))
}

func TestComposePrompt(t *testing.T) {
	assert.Eq(t, "base", ComposePrompt("base", ""))
	assert.Eq(t, "base", ComposePrompt("base", "  \n"))
	assert.Eq(t, "base\n\n"+ExtraHeading+"\nmore", ComposePrompt("base", "\nmore\n"))
	// A trailing newline of the body does not leave a blank line under it.
	assert.Eq(t, "base\n\n"+ExtraHeading+"\nmore", ComposePrompt("base\n", "more"))
	assert.True(t, strings.HasSuffix(ComposePrompt("base", "more"), "more"))
}
