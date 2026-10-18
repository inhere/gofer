package hookrelay

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
)

func TestMailPromptsAreHarness(t *testing.T) {
	block := "  \n<gofer-mail id=\"m1\" from=\"a\">\nhello\n</gofer-mail>\n"
	for _, pr := range []string{block, RenderMailWake(3), `<gofer-mail-wake count="1"/>`} {
		assert.True(t, IsHarnessPrompt(pr), pr)
	}
	assert.Eq(t, "<gofer-mail-wake count=\"2\"/>\n"+MailWakeNotice, RenderMailWake(2))

	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOn, State: "idle"}
	var log strings.Builder
	for _, pr := range []string{block, RenderMailWake(1)} {
		_, err := Run(f, payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "UserPromptSubmit", "cwd": "/w/repo", "prompt": pr}), fastOpts(&log))
		assert.NoErr(t, err)
		last := f.beats[len(f.beats)-1]
		assert.True(t, last.Injected)
		assert.Eq(t, "", last.Title)
	}
	assert.Eq(t, 0, f.humanEvents)
	assert.Eq(t, client.RelayModeOn, f.sessions["s1"].RelayMode)
}

func TestMailTagTextMidSentenceIsHuman(t *testing.T) {
	for _, pr := range []string{
		"why does <gofer-mail id=\"x\">hi</gofer-mail> show up?",
		"see <gofer-mail-wake count=\"1\"/> in the log",
		"<gofer-mailx>y</gofer-mailx>",
		"<gofer-mail id=\"x\">unterminated",
	} {
		// the last two start with an XML-ish tag, which is harness by the general
		// rule; only the mid-sentence ones must stay human.
		if strings.HasPrefix(pr, "<") {
			continue
		}
		assert.False(t, IsHarnessPrompt(pr), pr)
	}
	assert.False(t, isMailInjection("<gofer-mailx>y</gofer-mailx>"))
	assert.False(t, isMailInjection("<gofer-mail id=\"x\">unterminated"))
	assert.True(t, IsHarnessPrompt("<task-notification>done</task-notification>"))
	assert.False(t, IsHarnessPrompt("please continue"))
}
