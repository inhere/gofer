package sessionrelay

import "testing"

// TestInjectTextKeepsSystemNoticesUnprefixed: path A types a person's web reply with
// InjectPrefix, but a gofer system notice keeps its own line - with the prefix the agent
// would read the machine notice as the person replying from the web.
func TestInjectTextKeepsSystemNoticesUnprefixed(t *testing.T) {
	if got := injectText("好的，继续"); got != InjectPrefix+"好的，继续" {
		t.Fatalf("web reply = %q, want the inject prefix", got)
	}
	notice := SystemNoticePrefix + " [gofer job 完成] job-1 status=done"
	if got := injectText(notice); got != notice {
		t.Fatalf("system notice = %q, want it unchanged", got)
	}
}
