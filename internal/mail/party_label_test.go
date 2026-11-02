package mail

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
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
		for _, lang := range i18n.Supported() {
			want := OriginOf(m).DisplayLang(lang)
			if got := jobstore.MailPartyLabelIn(lang, tc.kind, tc.id, tc.caller); got != want {
				t.Errorf("MailPartyLabelIn(%s, %q, %q, %q) = %q, want the mailbox's own label %q",
					lang, tc.kind, tc.id, tc.caller, got, want)
			}
		}
	}
}
