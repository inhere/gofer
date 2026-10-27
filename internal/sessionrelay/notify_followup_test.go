package sessionrelay

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

func TestFormatJobEndedNoticeFollowUpHint(t *testing.T) {
	for _, tc := range []struct { lang i18n.Lang; want string }{
		{i18n.EN, "There are 2 queued follow-ups still unexecuted"},
		{i18n.ZhCN, "还有 2 条未执行的追加需求"},
	} {
		text := FormatJobEndedNotice(tc.lang, JobEndedNotice{JobID: "job-f", Status: "failed", FollowUpPending: 2})
		if !strings.Contains(text, tc.want) || !strings.Contains(text, "gofer job follow-up job-f --list") {
			t.Errorf("%s notice = %q", tc.lang, text)
		}
	}
	text := FormatJobEndedNotice(i18n.EN, JobEndedNotice{JobID: "job-d", Status: "done", FollowUpPending: 2})
	if !strings.Contains(text, "There are 2 queued follow-ups") { t.Fatalf("done notice should retain the hint: %q", text) }
}
