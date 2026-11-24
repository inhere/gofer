package sessionrelay

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/msgprefix"
)

// A schedule's operator ("schedule:<id>") is gofer speaking: its line keeps the system prefix
// and is never wrapped as a web message; a person's operator still is.
func TestMessengerLineScheduleOperatorIsSystem(t *testing.T) {
	text := msgprefix.ScheduleIn(i18n.ZhCN, "weekly") + "\nreport please"
	line := messengerLine(i18n.ZhCN, "schedule:sch-1", text)
	if line != text || msgprefix.IsPerson(line) {
		t.Fatalf("line = %q", line)
	}
	bare := messengerLine(i18n.ZhCN, "schedule:sch-1", "no prefix")
	if !msgprefix.IsSystem(bare) {
		t.Fatalf("a schedule message without a prefix must still read as system: %q", bare)
	}
	if got := messengerLine(i18n.ZhCN, "alice", "hi"); !msgprefix.IsPerson(got) {
		t.Fatalf("a person's message must keep the web prefix: %q", got)
	}
}
