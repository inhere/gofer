package httpapi

import (
	"strings"
	"testing"
)

// The messenger prompt now opens with the session's address (design 2026-11-05): the
// stored message text must still be the forwarded line alone, cut after the FIRST "] "
// — the web prefix — and never a piece of the messenger's own instruction. A Windows
// pipe address carries no "] ", so it cannot move that marker.
func TestMessengerOriginalMessageCutsTheAddressPrompt(t *testing.T) {
	pipe := `\\.\pipe\LOCAL\cc-msg-0123456789abcdef0123456789abcdef`
	prompt := "please forward this to the session \"proj-4d\" and write with uds:" + pipe +
		" exactly; fall back only when the tool returns success:false\n[from web, alice] the original text"
	command := []string{"claude", "-p", prompt, "--allowedTools", "SendMessage,ListAgents"}
	if got := messengerOriginalMessage(command); got != "the original text" {
		t.Fatalf("original message = %q, want the forwarded text alone", got)
	}
	// Without the web prefix (a wake) the whole prompt is the message, as before.
	wake := []string{"claude", "-p", "<gofer-mail-wake count=\"1\"/>", "--name", "gofer-web"}
	if got := messengerOriginalMessage(wake); got != "<gofer-mail-wake count=\"1\"/>" {
		t.Fatalf("wake message = %q", got)
	}
	if got := messengerOriginalMessage([]string{"claude"}); got != "" {
		t.Fatalf("command without a prompt = %q", got)
	}
	if strings.Contains(prompt, "\\u") {
		t.Fatalf("the address must not be escaped: %q", prompt)
	}
}
