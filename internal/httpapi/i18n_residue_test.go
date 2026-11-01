package httpapi

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestHandlerResidueTextFollowsLanguage pins the display texts that moved from the httpapi
// handlers into the catalog in S3f: zh-CN is verbatim what the handlers wrote before, English
// carries no CJK.
func TestHandlerResidueTextFollowsLanguage(t *testing.T) {
	t.Parallel()
	handed := jobstore.AgentSession{State: jobstore.SessionHandedOff, HandedOffJobID: "job-9"}
	at := time.Date(2026, 10, 31, 8, 5, 0, 0, time.Local)
	handedAt := handed
	handedAt.HandedOffAt = at.Unix()
	failed := jobstore.JobRecord{ID: "job-1", Error: "boom"}
	cases := []struct {
		name, zh, en string
		got          func(i18n.Lang) string
	}{
		{"failed inbox card", "job-1 失败", "job-1 failed", func(l i18n.Lang) string { return terminalCard(l, failed, "job.failed").Title }},
		{"handed-off notice (now)", "该会话已于 刚刚 在 web 接管（job job-9），继续请在 web 终端或 --resume；本终端的中继已停用",
			"This session was taken over on the web at just now (job job-9); continue in the web terminal or with --resume. The relay of this terminal is now off",
			func(l i18n.Lang) string { return handedOffNotice(l, handed) }},
		{"handed-off notice (time)", "该会话已于 2026-10-31 08:05 在 web 接管（job job-9），继续请在 web 终端或 --resume；本终端的中继已停用",
			"This session was taken over on the web at 2026-10-31 08:05 (job job-9); continue in the web terminal or with --resume. The relay of this terminal is now off",
			func(l i18n.Lang) string { return handedOffNotice(l, handedAt) }},
		{"dispatch to session", "已送出到会话 s1（信件 #7）", "Sent to session s1 (letter #7)", func(l i18n.Lang) string { return i18n.T(l, "work.journal.dispatch_session", "sid", "s1", "mail", 7) }},
		{"dispatch to job", "已关联 job j1（running）", "Linked job j1 (running)", func(l i18n.Lang) string {
			return i18n.T(l, "work.journal.dispatch_job", "job", "j1", "status", "running")
		}},
		{"archived from capture", "从收集箱归档（状态改为 done）", "Archived from the capture box (status set to done)", func(l i18n.Lang) string { return i18n.T(l, "work.journal.archived_from_capture", "status", "done") }},
		{"inject job gone", "job 记录已不存在", "The job record no longer exists", func(l i18n.Lang) string { return i18n.T(l, "httpapi.inject_job_gone") }},
		{"runner offline alert", "runner w1 离线", "runner w1 is offline", func(l i18n.Lang) string { return i18n.T(l, "today.status.runner_offline", "name", "w1") }},
	}
	for _, tc := range cases {
		if got := tc.got(i18n.ZhCN); got != tc.zh {
			t.Errorf("%s zh = %q, want %q", tc.name, got, tc.zh)
		}
		if got := tc.got(i18n.EN); got != tc.en || authzViewHan.MatchString(got) {
			t.Errorf("%s en = %q, want %q", tc.name, got, tc.en)
		}
	}
}
