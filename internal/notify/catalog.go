package notify

import "slices"

// Notification event catalog (gofer-x9mb): the ONE machine-readable list of events a
// notification channel can subscribe to, with a group and a zh / en description each.
// The console renders its event pickers from this, so an operator reads "会话等你回复"
// instead of typing "session.awaiting_reply" and never has to guess which key exists.
//
// Two rules keep it honest:
//   - the DEFAULT subscription of each channel is NOT restated here. It is computed from
//     DefaultTriggerEvents / DefaultDesktopEvents (see EventCatalog), so the definition of
//     "subscribed when events is empty" stays in match.go alone;
//   - every key of those two sets MUST have a catalog entry (TestEventCatalogCoversDefaults),
//     so adding a default event without describing it fails the build.
//
// The keys are string literals because the producing packages (internal/job, the runners)
// import this one — the reverse would be an import cycle (G022). The catalog therefore
// lists the events a deployment can actually subscribe to, not the whole job-event
// vocabulary: an event that exists only as a timeline row is not notification material.

// EventInfo is one catalog entry: the trigger key, the group it belongs to, the
// description in both console languages, and whether each channel subscribes to it when
// its own `events` list is empty.
type EventInfo struct {
	Key   string `json:"key"`
	Group string `json:"group"`
	Zh    string `json:"zh"`
	En    string `json:"en"`
	// Default reports membership of DefaultTriggerEvents: a webhook (or event hook,
	// which shares the set) with no events filter receives this event.
	Default bool `json:"default"`
	// DesktopDefault reports membership of DefaultDesktopEvents: the desktop channel
	// with no events filter pops this event.
	DesktopDefault bool `json:"desktop_default"`
}

// EventGroup is one group of the catalog (the picker's section heading).
type EventGroup struct {
	Key string `json:"key"`
	Zh  string `json:"zh"`
	En  string `json:"en"`
}

// EventGroups is the ordered group list the console shows. A catalog entry whose group is
// missing here is a bug the coverage test catches.
var EventGroups = []EventGroup{
	{Key: "job", Zh: "任务", En: "Jobs"},
	{Key: "interaction", Zh: "交互与审批", En: "Interactions and approvals"},
	{Key: "session", Zh: "持续会话", En: "Sessions"},
	{Key: "plan", Zh: "计划与工作流", En: "Plans"},
	{Key: "work", Zh: "工作项", En: "Work items"},
	{Key: "system", Zh: "系统", En: "System"},
}

// catalogEvents is the catalog without the default flags (they are derived on read). The
// order is the picker's order inside each group.
var catalogEvents = []EventInfo{
	// job
	{Key: "job.terminal", Group: "job",
		Zh: "任务结束（完成 / 失败 / 取消），人需要知道结果",
		En: "A job finished (done / failed / cancelled)"},
	{Key: "job.needs_review", Group: "job",
		Zh: "任务跑完在等人工验收（--review / require_review 的任务）",
		En: "A job finished and waits for human review (--review / require_review)"},
	{Key: "job.reviewed", Group: "job",
		Zh: "有人接受或驳回了待验收的任务",
		En: "Someone accepted or rejected a job waiting for review"},
	{Key: "job.awaiting_approval", Group: "job",
		Zh: "任务已提交但在等你批准（--hold）",
		En: "A job is held for your approval (--hold)"},
	{Key: "job.hold_approved", Group: "job",
		Zh: "待批任务被批准，已开始执行",
		En: "A held job was approved and started"},
	{Key: "job.hold_rejected", Group: "job",
		Zh: "待批任务被拒绝",
		En: "A held job was rejected"},
	{Key: "job.hold_expired", Group: "job",
		Zh: "待批任务过了批准时限，已自动取消",
		En: "A held job passed its approval deadline and was cancelled"},
	{Key: "job.stalled", Group: "job",
		Zh: "任务长时间没有输出被看门狗结束",
		En: "A job produced no output and was killed by the stall watchdog"},
	{Key: "job.budget_exceeded", Group: "job",
		Zh: "任务越过预算上限（tokens / 费用 / 回合）被终止",
		En: "A job crossed its budget limit (tokens / cost / turns) and was killed"},
	{Key: "job.retry_scheduled", Group: "job",
		Zh: "失败的任务排了一次重试",
		En: "A failed job was scheduled for a retry"},
	{Key: "job.retry_started", Group: "job",
		Zh: "重试已作为新任务启动",
		En: "A retry started as a new job"},
	{Key: "job.retry_exhausted", Group: "job",
		Zh: "重试次数用完了，不会再有下一次",
		En: "The retry budget is spent; nothing will run this again"},
	{Key: "job.delivery_failed", Group: "job",
		Zh: "任务的分支无法合并 / 推送 / 开 PR，需要人工处理",
		En: "A finished job's branch could not be merged / pushed / turned into a PR"},
	{Key: "job.delivered", Group: "job",
		Zh: "任务的分支已交付（合并 / 推送 / PR）",
		En: "A finished job's branch was delivered (merge / push / PR)"},
	{Key: "job.verify_finished", Group: "job",
		Zh: "任务的验证步骤结束（通过 / 失败）",
		En: "A job's verify step finished (passed / failed)"},
	{Key: "job.uncommitted", Group: "job",
		Zh: "任务结束后工作区还有未提交的改动",
		En: "A finished job left uncommitted changes in the workspace"},
	{Key: "job.fell_back", Group: "job",
		Zh: "任务失败后由下一个候选 agent 接手",
		En: "A failed job was taken over by the next candidate agent"},
	{Key: "job.auto_resumed", Group: "job",
		Zh: "任务被自动续跑（新的续跑任务已提交）",
		En: "A job was resumed automatically (a continuation was submitted)"},
	{Key: "job.agent_substituted", Group: "job",
		Zh: "任务派发前换成了另一个可用 agent",
		En: "A job ran on a substitute agent (the requested one was unavailable)"},
	{Key: "job.wakeup_failed", Group: "job",
		Zh: "唤醒条件到了但续跑没能启动",
		En: "A wakeup fired but its continuation could not be started"},
	{Key: "job.ceiling_denied", Group: "job",
		Zh: "自动续跑 / 接手 / 唤醒被调用方上限拒绝",
		En: "An automatic follow-up was refused by the caller's ceiling"},
	{Key: "job.dir_wait_timeout", Group: "job",
		Zh: "等目录锁超过上限，任务没跑就被判失败",
		En: "A job waited for the directory lock past the limit and was failed"},

	// interaction
	{Key: "interaction.created", Group: "interaction",
		Zh: "agent 有问题或权限请求等你回答",
		En: "An agent asked a question or requested a permission"},
	{Key: "interaction.answered", Group: "interaction",
		Zh: "有人回答了交互（收尾用，一般不需要订阅）",
		En: "Someone answered an interaction (closes the loop; usually not needed)"},
	{Key: "interaction.punted", Group: "interaction",
		Zh: "交互被转交给别的调用方",
		En: "An interaction was punted to another caller"},

	// session
	{Key: "session.awaiting_reply", Group: "session",
		Zh: "持续会话在等你回复",
		En: "A continuous session is waiting for your reply"},
	{Key: "session.waiting", Group: "session",
		Zh: "中继会话停下来开了新一轮，等人在网页上回复",
		En: "A relayed session opened a turn and waits for a reply in the console"},
	{Key: "session.attention", Group: "session",
		Zh: "会话在终端里等确认（权限或选择），要回到键盘",
		En: "A session waits for confirmation in its terminal (permission or choice)"},
	{Key: "session.nudge_paused", Group: "session",
		Zh: "会话催办连续投递失败后自动暂停",
		En: "A session nudge paused itself after repeated delivery failures"},

	// plan
	{Key: "plan.blocked", Group: "plan",
		Zh: "链上的任务失败，计划卡住等人处理",
		En: "A chain job failed and parked the plan on its item"},
	{Key: "plan.completed", Group: "plan",
		Zh: "计划的所有条目都完成或跳过了",
		En: "Every item of a plan is done or skipped"},
	{Key: "plan.todo_unassigned", Group: "plan",
		Zh: "计划条目到期但没有指派执行人",
		En: "A plan item came due with nobody assigned to run it"},
	{Key: "plan.leader_exhausted", Group: "plan",
		Zh: "计划的 leader 轮次用完了，不再自动推进",
		En: "A plan spent its leader-round budget and stopped advancing"},
	{Key: "plan.todo_dispatch_failed", Group: "plan",
		Zh: "计划条目派发被拒绝，没有任务启动",
		En: "A plan item's dispatch was refused; nothing started"},
	{Key: "plan.advance_paused", Group: "plan",
		Zh: "计划暂停时到了推进时机，条目原地不动",
		En: "A plan advance was due while the plan is paused"},

	// work
	{Key: EventWorkRemind, Group: "work",
		Zh: "工作项到了提醒时间",
		En: "A work item's reminder time arrived"},
	{Key: EventWorkDigest, Group: "work",
		Zh: "每日工作项摘要（work.digest_time，默认 09:00）",
		En: "The daily work-item digest (work.digest_time, default 09:00)"},
	{Key: EventWorkNeedsMe, Group: "work",
		Zh: "工作项进入「等我」（需开启 work.needs_me_notify）",
		En: "A work item entered \"needs me\" (needs work.needs_me_notify)"},

	// system
	{Key: EventUpdateAvailable, Group: "system",
		Zh: "发现 gofer 新版本（每个版本只发一次）",
		En: "A newer gofer release was found (once per version)"},
	{Key: "config.updated", Group: "system",
		Zh: "有人通过网页 / 接口改了配置（审计用）",
		En: "Someone changed the config through the console / API (audit)"},
}

// EventCatalog returns the catalog with each entry's Default / DesktopDefault flags filled
// from the two default sets, i.e. from the same values the matchers use. The slice is a
// copy: a caller cannot mutate the package's catalog.
func EventCatalog() []EventInfo {
	out := make([]EventInfo, 0, len(catalogEvents))
	for _, e := range catalogEvents {
		e.Default = slices.Contains(DefaultTriggerEvents, e.Key)
		e.DesktopDefault = slices.Contains(DefaultDesktopEvents, e.Key)
		out = append(out, e)
	}
	return out
}
