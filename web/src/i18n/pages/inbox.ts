// 收件箱分栏页的文案（design 2026-10-28-inbox-triage-design.md §6a，INB2）：/inbox。
// key 前缀 `inbox.`（+ 导航项 `nav.inbox`）；页面字典优先于主字典，所以导航项放这里。
// 业务内容（条目标题、摘要、项目名）来自服务端，不翻译。
import type { PageDict } from './types'

export const inbox: PageDict = {
  zh: {
    'nav.inbox': '收件箱',

    'inbox.title': '收件箱',
    'inbox.subtitle': '{n} 条要处理的未读',
    'inbox.badgeTitle': '收件箱：{n} 条要处理的未读',
    'inbox.unavailable': '收件箱未启用，或当前凭据不能使用个人收件箱。',

    'inbox.col.action': '要处理',
    'inbox.col.fyi': '仅知会',
    'inbox.col.action.hint': '等你处理（与今天页、顶栏徽标同一个列表）：批准、回答、验收、卡住的计划、你提交而失败的任务、待确认的投递',
    'inbox.col.fyi.hint': '你本人提交的任务完成了，只是让你知道；agent / 会话 / job 凭据派出的任务不在这里；不计入侧栏徽标',
    'inbox.col.count': '未读 {unread} · 共 {total}',

    'inbox.opt.exec': '含 exec 待验收',
    'inbox.opt.cleared': '显示已清理',

    'inbox.act.refresh': '刷新',
    'inbox.act.open': '打开',
    'inbox.act.markSeen': '已读',
    'inbox.act.markAll': '全部已读',
    'inbox.act.clear': '清理',
    'inbox.act.clearHint': '只从你的收件箱隐藏，不会执行任何动作；来源有变化时会重新出现',
    'inbox.act.clearRead': '清理已读',
    'inbox.act.clearReadHint': '隐藏本栏已读的条目，未读的不动；不会执行任何动作',
    'inbox.act.restore': '恢复',

    'inbox.empty.action': '没有要你处理的事。',
    'inbox.empty.fyi': '没有新的知会。',

    'inbox.kind.interaction': '等你回答',
    'inbox.kind.decision': '待决策',
    'inbox.kind.relay': '会话来信',
    'inbox.kind.permission': '工具授权',
    'inbox.kind.review': '待验收',
    'inbox.kind.approval': '待批准',
    'inbox.kind.work': '工作项',
    'inbox.kind.suggestion': '建议',
    'inbox.kind.merge': '待合并',
    'inbox.kind.plan_blocked': '计划受阻',
    'inbox.kind.memory': '记忆整理',
    'inbox.kind.capture': '收集箱',
    'inbox.kind.mail_held': '信件待指派',
    'inbox.kind.worker_enroll': 'worker 申请',
    'inbox.kind.job.terminal': '任务结束',
    'inbox.kind.job.failed': '任务失败',
    'inbox.kind.other': '其他',

    'inbox.toast.seen': '已标为已读（{n}）',
    'inbox.toast.cleared': '已清理（{n}）',
    'inbox.toast.restored': '已恢复（{n}）',
    'inbox.toast.failed': '操作失败：{msg}',
  },
  en: {
    'nav.inbox': 'Inbox',

    'inbox.title': 'Inbox',
    'inbox.subtitle': '{n} unread to act on',
    'inbox.badgeTitle': 'Inbox: {n} unread to act on',
    'inbox.unavailable': 'The inbox is disabled, or this credential cannot use a personal inbox.',

    'inbox.col.action': 'To act on',
    'inbox.col.fyi': 'FYI',
    'inbox.col.action.hint': 'Waiting on you (the same list as the Today page and the top-bar badge): approvals, answers, reviews, stuck plans, your own failed jobs, held deliveries',
    'inbox.col.fyi.hint': 'Jobs you submitted yourself that finished; jobs started by an agent, a session or a job credential are not listed; not counted in the sidebar badge',
    'inbox.col.count': '{unread} unread · {total} total',

    'inbox.opt.exec': 'Include exec reviews',
    'inbox.opt.cleared': 'Show cleared',

    'inbox.act.refresh': 'Refresh',
    'inbox.act.open': 'Open',
    'inbox.act.markSeen': 'Mark read',
    'inbox.act.markAll': 'Mark all read',
    'inbox.act.clear': 'Clear',
    'inbox.act.clearHint': 'Only hides it from your inbox and performs no action; it comes back if the source changes',
    'inbox.act.clearRead': 'Clear read',
    'inbox.act.clearReadHint': 'Hide the read items in this column; unread ones stay. Performs no action',
    'inbox.act.restore': 'Restore',

    'inbox.empty.action': 'Nothing needs you right now.',
    'inbox.empty.fyi': 'No new updates.',

    'inbox.kind.interaction': 'Question',
    'inbox.kind.decision': 'Decision',
    'inbox.kind.relay': 'Session message',
    'inbox.kind.permission': 'Tool permission',
    'inbox.kind.review': 'Review',
    'inbox.kind.approval': 'Approval',
    'inbox.kind.work': 'Work item',
    'inbox.kind.suggestion': 'Suggestion',
    'inbox.kind.merge': 'Merge',
    'inbox.kind.plan_blocked': 'Plan blocked',
    'inbox.kind.memory': 'Memory tidy',
    'inbox.kind.capture': 'Capture box',
    'inbox.kind.mail_held': 'Held mail',
    'inbox.kind.worker_enroll': 'Worker request',
    'inbox.kind.job.terminal': 'Job finished',
    'inbox.kind.job.failed': 'Job failed',
    'inbox.kind.other': 'Other',

    'inbox.toast.seen': 'Marked read ({n})',
    'inbox.toast.cleared': 'Cleared ({n})',
    'inbox.toast.restored': 'Restored ({n})',
    'inbox.toast.failed': 'Failed: {msg}',
  },
}
