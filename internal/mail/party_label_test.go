package mail

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// The secret scan's mail section (M8) labels one end of a letter with jobstore.MailPartyLabel —
// the store cannot import this package (G022), so the wording is spelled on both sides. This is
// the drift guard: the two must stay identical for every legal end of a letter.
func TestMailPartyLabelMatchesOriginDisplay(t *testing.T) {
	for _, tc := range []struct{ kind, id, caller string }{
		{jobstore.MailKindSession, "sess-a", "alice"},
		{jobstore.MailKindSession, "sess-a", ""},
		{jobstore.MailKindJob, "job-7", "alice"},
		{jobstore.MailKindJob, "job-7", ""},
		{jobstore.MailKindCaller, "", "alice"},
		{jobstore.MailKindCaller, "", ""},
	} {
		m := jobstore.Mail{FromKind: tc.kind, FromID: tc.id, FromCaller: tc.caller}
		want := OriginOf(m).Display()
		if got := jobstore.MailPartyLabel(tc.kind, tc.id, tc.caller); got != want {
			t.Errorf("MailPartyLabel(%q, %q, %q) = %q, want the mailbox's own label %q",
				tc.kind, tc.id, tc.caller, got, want)
		}
	}
}
