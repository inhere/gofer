package job

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// The zh-CN expectations in this file are the literals (and fmt formats) the code held
// BEFORE the text moved into the catalog (S3e): every zh-CN deployment already has these
// strings in its stored notes, comments and events, so they must not drift.

var storedHan = regexp.MustCompile(`\p{Han}`)

func TestStatusLabelZhCNVerbatim(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]string{
		StatusQueued: "排队中", StatusRunning: "运行中", StatusAwaitingInput: "等待输入",
		StatusPendingInteraction: "等待交互", StatusDone: "已完成", StatusFailed: "失败",
		StatusCancelled: "已取消", StatusTimeout: "超时", StatusRecovering: "重启恢复中",
		StatusNeedsReview: "等待验收", StatusRejected: "已拒绝", StatusWaitingDir: "等待目录锁",
		StatusAwaitingApproval: "等待批准", StatusPausedForUpdate: "升级等待（新任务暂停启动）",
		"": "未知状态", "something_new": "something_new",
	} {
		if got := StatusLabelIn(i18n.ZhCN, status); got != want {
			t.Errorf("StatusLabelIn(zh-CN, %q) = %q, want %q", status, got, want)
		}
		if got := StatusLabelIn(i18n.EN, status); storedHan.MatchString(got) || got == "" {
			t.Errorf("StatusLabelIn(en, %q) = %q", status, got)
		}
	}
	if got := StatusLabelIn(i18n.EN, "something_new"); got != "something_new" {
		t.Errorf("an unknown status must pass through, got %q", got)
	}
}

func TestCheckCommentBodyZhCNVerbatim(t *testing.T) {
	t.Parallel()
	stored := StoredCheck{Round: 2, CheckResult: CheckResult{Summary: "all good", Verdict: "pass", Items: []CheckItem{{Passed: false}, {Passed: true}, {Passed: false}}}}
	snap := JobResult{CheckResumeCount: 1}
	cases := []struct {
		name     string
		d        checkDecision
		resumed  string
		wantZh   string
		wantEnRe string
	}{
		{"error", checkDecision{err: "checker down", stored: stored}, "", fmt.Sprintf("交付核验未完成（保持原状态）：%s", "checker down"), `^Delivery check did not finish`},
		{"pass", checkDecision{stored: stored}, "", fmt.Sprintf("交付核验第 %d 轮：合格。%s", 2, "all good"), `^Delivery check round 2: passed\. all good$`},
		{"resumed", func() checkDecision { d := checkDecision{stored: stored, max: 3}; d.stored.Verdict = "fail"; return d }(), "job-9",
			fmt.Sprintf("交付核验第 %d 轮：不合格（%d 项未满足），已自动续接 %s（第 %d/%d 次）。%s", 2, 2, "job-9", 1, 3, "all good"), `continued automatically in job-9 \(attempt 1/3\)`},
		{"resume failed", func() checkDecision {
			d := checkDecision{stored: stored, action: checkActionResume, resumeErr: "no slot"}
			d.stored.Verdict = "fail"
			return d
		}(), "", fmt.Sprintf("交付核验第 %d 轮：不合格（%d 项未满足），自动续接未能提交（%s），转入待验收。%s", 2, 2, "no slot", "all good"), `could not be submitted \(no slot\)`},
		{"failed", func() checkDecision { d := checkDecision{stored: stored}; d.stored.Verdict = "fail"; return d }(), "",
			fmt.Sprintf("交付核验第 %d 轮：不合格（%d 项未满足），不再自动续接，转入待验收。%s", 2, 2, "all good"), `no more automatic continuation`},
	}
	for _, c := range cases {
		if got := checkCommentBody(i18n.ZhCN, snap, c.d, c.resumed); got != c.wantZh {
			t.Errorf("%s: zh-CN = %q, want %q", c.name, got, c.wantZh)
		}
		en := checkCommentBody(i18n.EN, snap, c.d, c.resumed)
		if storedHan.MatchString(en) || !regexp.MustCompile(c.wantEnRe).MatchString(en) {
			t.Errorf("%s: en = %q, want to match %s", c.name, en, c.wantEnRe)
		}
	}
	// An empty summary leaves no dangling space in either language.
	d := checkDecision{stored: StoredCheck{Round: 1, CheckResult: CheckResult{Verdict: "pass"}}}
	if got := checkCommentBody(i18n.ZhCN, snap, d, ""); got != "交付核验第 1 轮：合格。" {
		t.Errorf("zh-CN empty summary = %q", got)
	}
	if got := checkCommentBody(i18n.EN, snap, d, ""); got != "Delivery check round 1: passed." {
		t.Errorf("en empty summary = %q", got)
	}
}

func TestContinueReasonsZhCNVerbatim(t *testing.T) {
	t.Parallel()
	zh := func(name string, args ...any) string { return continueText(i18n.ZhCN, name, args...) }
	for name, c := range map[string]struct{ got, want string }{
		"note":                  {continueFallbackNote(i18n.ZhCN, "R"), "未接续：" + "R" + "，已开新会话"},
		"worktree_base":         {zh("worktree_base", "branch", "b1"), fmt.Sprintf("；新 worktree 以前序分支 %s 为基", "b1")},
		"claimed_by_other":      {i18n.T(i18n.ZhCN, continueClaimedByOther), "已被其他项续接"},
		"claimed_by_self":       {i18n.T(i18n.ZhCN, continueClaimedBySelf), "已由本项占用"},
		"pred_read_failed":      {zh("pred_read_failed", "error", "E"), "读取前序项失败：" + "E"},
		"pred_missing":          {zh("pred_missing"), "前序项不存在"},
		"pred_not_done":         {zh("pred_not_done", "status", "doing"), fmt.Sprintf("前序项状态为 %s，未完成", "doing")},
		"pred_job_read_failed":  {zh("pred_job_read_failed", "error", "E"), "读取前序 job 失败：" + "E"},
		"pred_no_job":           {zh("pred_no_job"), "前序项没有已完成且带会话的 job"},
		"exec_no_resume":        {zh("exec_no_resume"), "exec 不能续接会话"},
		"session_not_resumable": {zh("session_not_resumable", "error", "E"), "前序会话不可续接：" + "E"},
		"runner_differs":        {zh("runner_differs", "own", fmt.Sprintf("%q", "a"), "pred", fmt.Sprintf("%q", "b")), fmt.Sprintf("runner 不同或非本机（本项 %q，前序 %q）", "a", "b")},
		"cwd_differs":           {zh("cwd_differs", "own", fmt.Sprintf("%q", "a"), "pred", fmt.Sprintf("%q", "b")), fmt.Sprintf("本项 cwd %q 与前序 job 的 %q 不同", "a", "b")},
		"worktree_gone":         {zh("worktree_gone"), "前序 job 的 worktree 已不存在"},
		"needs_delivery":        {zh("needs_delivery"), "前序 job 需要自动交付"},
		"claim_failed":          {zh("claim_failed", "error", "E"), "占用续接位失败：" + "E"},
		"chain_limit":           {zh("chain_limit", "n", 3), fmt.Sprintf("已连续接续 %d 次", 3)},
		"template_failed":       {zh("template_failed", "error", "E"), "任务书渲染失败：" + "E"},
		"config_unsupported":    {zh("config_unsupported", "error", "E"), "配置不支持续接：" + "E"},
		"agent_no_resume":       {zh("agent_no_resume", "agent", fmt.Sprintf("%q", "x")), fmt.Sprintf("agent %q 没有可用的会话续接方式", "x")},
		"readonly_mismatch":     {zh("readonly_mismatch", "own", fmt.Sprint(true), "pred", fmt.Sprint(false)), fmt.Sprintf("只读设置不一致（本项 %t，前序 %t）", true, false)},
		"assemble_failed":       {zh("assemble_failed", "error", "E"), "续接请求组装失败：" + "E"},
	} {
		if c.got != c.want {
			t.Errorf("%s: zh-CN = %q, want %q", name, c.got, c.want)
		}
	}
	// Every English reason stands on its own.
	for _, key := range []string{"note", "worktree_base", "pred_missing", "exec_no_resume", "worktree_gone", "needs_delivery", "agent_no_resume"} {
		if en := continueText(i18n.EN, key, "branch", "b", "reason", "r", "agent", "a"); storedHan.MatchString(en) || en == "job.continue."+key {
			t.Errorf("en %s = %q", key, en)
		}
	}
	if got := continueFallbackNote(i18n.EN, "The predecessor item does not exist") + continueText(i18n.EN, "worktree_base", "branch", "b1"); storedHan.MatchString(got) {
		t.Errorf("en note = %q", got)
	}
}

func TestSubmitAndWorktreeErrorsZhCNVerbatim(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ got, want string }{
		"policy_pending":    {i18n.T(i18n.ZhCN, "job.errors.policy_pending", "worker", fmt.Sprintf("%q", "w1"), "rev", 7), fmt.Sprintf("worker %q 尚未应用策略（policy_pending, rev=%d）", "w1", 7)},
		"session_local":     {i18n.T(i18n.ZhCN, "job.errors.session_local_only"), "持续会话目前仅支持本机 runner"},
		"session_protocol":  {i18n.T(i18n.ZhCN, "job.errors.session_protocol", "worker", "w1", "version", 3), fmt.Sprintf("worker %s 协议 v%d 不支持持续会话，请升级", "w1", 3)},
		"worker_restarted":  {remoteSessionWorkerRestartError(i18n.ZhCN, "job-1").Error(), fmt.Sprintf("worker 重启，持续会话已结束；可用 gofer job resume %s 以 session/load 接续上下文", "job-1")},
		"commit_not_remote": {i18n.T(i18n.ZhCN, "job.errors.commit_not_remote", "sha", "abc", "remote", fmt.Sprintf("%q", "origin"), "reason", ": boom"), fmt.Sprintf("commit %s 不在远端 %q，先推送该提交，或改在 server 本机检查%s", "abc", "origin", ": boom")},
		"todo_review":       {i18n.T(i18n.ZhCN, "job.todo_note.needs_review", "job", "job-1"), "job-1" + " 待验收"},
		"todo_merged":       {strings.TrimSpace(i18n.T(i18n.ZhCN, "job.todo_note.merged", "branch", "br", "sha", "")), strings.TrimSpace("br" + " 合入 " + "")},
		"review_elided":     {markedTailLines(i18n.ZhCN, "a\nb\nc\nd", 2), fmt.Sprintf("…（前面 %d 行已省略）\n%s", 2, "c\nd")},
	} {
		if c.got != c.want {
			t.Errorf("%s: zh-CN = %q, want %q", name, c.got, c.want)
		}
	}
	for _, en := range []string{
		i18n.T(i18n.EN, "job.errors.policy_pending", "worker", "w", "rev", 1),
		i18n.T(i18n.EN, "job.errors.session_local_only"),
		i18n.T(i18n.EN, "job.errors.session_protocol", "worker", "w", "version", 1),
		remoteSessionWorkerRestartError(i18n.EN, "job-1").Error(),
		i18n.T(i18n.EN, "job.errors.commit_not_remote", "sha", "a", "remote", "r", "reason", ""),
		i18n.T(i18n.EN, "job.todo_note.needs_review", "job", "j"),
		i18n.T(i18n.EN, "job.todo_note.merged", "branch", "b", "sha", "s"),
		markedTailLines(i18n.EN, "a\nb\nc\nd", 2),
	} {
		if storedHan.MatchString(en) {
			t.Errorf("English text carries CJK: %q", en)
		}
	}
}
