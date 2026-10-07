package util

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestParseVersionOutputRemovesTerminalColors(t *testing.T) {
	got, err := ParseVersionOutput("Version: \x1b[0;36mt5-new\x1b[0m\n")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "t5-new", got)
	if _, err := ParseVersionOutput(" \n"); err == nil {
		t.Fatal("empty version accepted")
	}
}
