package sessionrelay

import (
	"testing"

	"github.com/inhere/gofer/internal/msgprefix"
)

// TestInjectTextPrefixesAPersonsReply: a person's web reply carries the web-reply
// prefix with the speaker's name, while a gofer system message keeps its own line —
// with a web prefix the agent would read the machine notice as the person writing.
func TestInjectTextPrefixesAPersonsReply(t *testing.T) {
	if got, want := injectText("好的，继续", "alice"), "[来自 web 回复，alice] 好的，继续"; got != want {
		t.Fatalf("web reply = %q, want %q", got, want)
	}
	notice := msgprefix.Format(msgprefix.JobDone, "", "job-1 status=done")
	if got := injectText(notice, "alice"); got != notice {
		t.Fatalf("system notice = %q, want it unchanged", got)
	}
	// A legacy system notice is machine text too (DEPRECATED(v0.185): remove in v0.188).
	legacy := "[gofer 通知] [gofer job 完成] job-1 status=done"
	if got := injectText(legacy, "alice"); got != legacy {
		t.Fatalf("legacy system notice = %q, want it unchanged", got)
	}
}
