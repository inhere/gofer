package sessionrelay

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// The zh-CN expectations are the literals (and fmt formats) the code held BEFORE the text
// moved into the catalog (S3e): stored on messages in every zh-CN deployment already.

var reasonHan = regexp.MustCompile(`\p{Han}`)

func TestDeliveryReasonsZhCNVerbatim(t *testing.T) {
	t.Parallel()
	tr := func(name string, args ...any) string { return i18n.T(i18n.ZhCN, "sessionrelay.reason."+name, args...) }
	m := jobstore.SessionMessage{JobID: "job-1"}
	for name, c := range map[string]struct{ got, want string }{
		"images_old":       {tr("images_old"), "目标会话进程版本过旧，不支持图片，请升级后重启该会话"},
		"no_messenger":     {tr("no_messenger"), "传话人执行器未配置"},
		"no_runner":        {tr("no_runner"), "会话未登记执行机"},
		"no_project":       {tr("no_project"), "该会话所在目录不属于任何已配置项目，无法派发传话人；请把目录加入项目，或在会话所在 runner 上配置项目"},
		"no_address":       {tr("no_address"), "会话没有可用的 Claude SendMessage 地址"},
		"messenger_state":  {tr("messenger_job_state", "job", "j", "status", "failed", "code", 2), fmt.Sprintf("传话人 job %s 状态=%s exit=%d", "j", "failed", 2)},
		"unfinished":       {tr("messenger_unfinished", "job", "j"), fmt.Sprintf("传话人 job %s 超时未完成", "j")},
		"timeout":          {tr("messenger_timeout", "job", "j"), fmt.Sprintf("传话人 job %s 超时", "j")},
		"timeout_error":    {tr("messenger_timeout_error", "timeout", "T", "error", "E"), "T" + "：" + "E"},
		"timeout_exit":     {tr("messenger_timeout_exit", "timeout", "T", "code", 3), fmt.Sprintf("%s，结束时 exit=%d", "T", 3)},
		"not_started":      {notStarted(i18n.ZhCN, InjectResult{JobID: "j", Status: "queued"}).Error(), "sessionrelay: undeliverable (not_delivered:queued): " + fmt.Sprintf("job %s 未启动（%s），已取消：未送达", "j", "queued")},
		"not_started_none": {notStarted(i18n.ZhCN, InjectResult{JobID: "j"}).Error(), "sessionrelay: undeliverable (not_delivered:): " + fmt.Sprintf("job %s 未启动（%s），已取消：未送达", "j", "未开始")},
		"not_running":      {deliveryNotRunningReason(i18n.ZhCN, m), ReasonNotRunning + ": job " + m.JobID + " 报告会话进程已不在运行（未送达，可重试）"},
		"job_gone":         {deliveryFailureReason(i18n.ZhCN, m, InjectState{}), fmt.Sprintf("job %s 已不在（服务重启或已清理），无法确认是否送达", m.JobID)},
		"job_state":        {deliveryFailureReason(i18n.ZhCN, m, InjectState{Known: true, Status: "failed", ExitCode: 2}), fmt.Sprintf("job %s 状态 %s exit=%d", m.JobID, "failed", 2) + "（无输出）"},
		"job_state_out":    {deliveryFailureReason(i18n.ZhCN, m, InjectState{Known: true, Status: "", ExitCode: 1, Output: " boom "}), fmt.Sprintf("job %s 状态 %s exit=%d", m.JobID, "unknown", 1) + "：" + "boom"},
		"nudge_paused":     {tr("nudge_paused", "n", 3, "error", "E"), fmt.Sprintf("%d 次连续送达失败：%s", 3, "E")},
		"reply_truncated":  {tr("reply_truncated"), "[已截断]"},
	} {
		if c.got != c.want {
			t.Errorf("%s: zh-CN = %q, want %q", name, c.got, c.want)
		}
	}
}

func TestDeliveryReasonsEnglishHaveNoCJK(t *testing.T) {
	t.Parallel()
	m := jobstore.SessionMessage{JobID: "job-1"}
	long := capReply(i18n.EN, strings.Repeat("x", maxReplyBytes+10))
	for _, text := range []string{
		notStarted(i18n.EN, InjectResult{JobID: "j", Status: "queued"}).Error(),
		notStarted(i18n.EN, InjectResult{JobID: "j"}).Error(),
		deliveryNotRunningReason(i18n.EN, m),
		deliveryFailureReason(i18n.EN, m, InjectState{}),
		deliveryFailureReason(i18n.EN, m, InjectState{Known: true, Status: "failed", ExitCode: 2}),
		deliveryFailureReason(i18n.EN, m, InjectState{Known: true, ExitCode: 1, Output: "boom"}),
		long[len(long)-20:],
		i18n.T(i18n.EN, "sessionrelay.reason.nudge_paused", "n", 3, "error", "E"),
		i18n.T(i18n.EN, "sessionrelay.reason.no_project"),
		i18n.T(i18n.EN, "sessionrelay.reason.messenger_timeout_exit", "timeout", "T", "code", 1),
	} {
		if reasonHan.MatchString(text) || text == "" {
			t.Errorf("English reason %q", text)
		}
	}
	if !strings.HasSuffix(long, "[truncated]") {
		t.Errorf("English truncation marker = %q", long[len(long)-20:])
	}
}
