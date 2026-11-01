package mail

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestResidueTextFollowsLanguage pins the three texts that moved into the catalog in S3f
// (offline-only candidate label, sender label with caller, the withdrawn-letter note): zh-CN
// is verbatim what the code wrote before, English carries no CJK.
func TestResidueTextFollowsLanguage(t *testing.T) {
	t.Parallel()
	cand := Candidate{ID: "s1", Offline: true}
	org := Origin{Kind: jobstore.MailKindSession, ID: "sess-a", Caller: "alice"}
	cases := []struct {
		name, zh, en string
		got          func(i18n.Lang) string
	}{
		{"candidate offline only", "会话 s1（可能不在线）", "session s1 (possibly offline)", func(l i18n.Lang) string { return cand.DisplayLang(l) }},
		{"origin with caller", "会话 sess-a（调用方 alice）", "session sess-a (caller alice)", func(l i18n.Lang) string { return org.DisplayLang(l) }},
		{"withdrawn note", "信件 #7 已撤回，条目随之丢弃", "Letter #7 was withdrawn, so its item was dropped", func(l i18n.Lang) string { return i18n.T(l, "mail.capture.withdrawn", "id", 7) }},
	}
	for _, tc := range cases {
		if got := tc.got(i18n.ZhCN); got != tc.zh {
			t.Errorf("%s zh = %q, want %q", tc.name, got, tc.zh)
		}
		if got := tc.got(i18n.EN); got != tc.en || mailTextCJK.MatchString(got) {
			t.Errorf("%s en = %q, want %q", tc.name, got, tc.en)
		}
	}
}
