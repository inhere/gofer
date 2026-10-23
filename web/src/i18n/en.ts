// 英文界面文案。key 用英文语义命名（设计稿 §9.2），不拿中文原文当 key。
//
// key 命名规范（P0 冻结）：
//   nav.*      导航与页面名（nav.jobs = 任务）
//   page.*     页面内的标题/副标题
//   col.*      表格列头
//   status.*   状态文字；status.<值> 对应接口里的状态值，status.family.<族> 是族名
//   act.*      按钮与操作
//   empty.*    空态
//   err.*      错误与失败提示
//   toast.*    轻提示
//   confirm.*  二次确认
//   hint.*     说明性小字
//   field.*    表单字段名
//   help.*     帮助文本
//   页面专属：<page>.<area>.<name>，例如 plans.h.progress
// 插值只支持 {name}；业务内容（标题、消息、日志、路径、id、命令）不翻译。
import type { Dict } from './index'

export const en: Dict = {
  'nav.today': 'Today',
  'nav.workbench': 'Workbench',
  'nav.work': 'Work items',
  'nav.plans': 'Plans',
  'nav.jobs': 'Jobs',
  'nav.sessions': 'Sessions',
  'nav.mail': 'Mailbox',
  'nav.review': 'Review',
  'nav.issues': 'Issues',
  'nav.dashboard': 'Insights',
  'nav.workflows': 'Workflows',
  'nav.schedules': 'Schedules',
  'nav.runners': 'Runners',
  'nav.agents': 'Agents',
  'nav.projects': 'Projects',
  'nav.settings': 'Settings',
  'nav.more': 'More',
  'nav.group.core': 'Core',
  'nav.group.collab': 'Collaborate',
  'nav.group.auto': 'Automate',
  'nav.group.infra': 'Infrastructure',

  'act.newJob': 'New job',
  'act.newSession': 'New session',
  'act.cancel': 'Cancel',
  'act.save': 'Save',
  'act.retry': 'Retry',
  'act.refresh': 'Refresh',
  'act.close': 'Close',
  'act.copy': 'Copy',
  'act.filter': 'Filter',
  'act.all': 'All',
  'act.needsMe': 'Needs me',
  'act.logout': 'Sign out',
  'act.back': 'Back',
  'act.clearFilters': 'Clear filters',
  'act.moreFilters': 'More filters',
  'act.apply': 'Apply',

  'shell.nav': 'Main navigation',
  'shell.home': 'Back to Today',
  'shell.search': 'Search pages',
  'shell.readonly': 'Read-only',
  'shell.stale': 'The server was updated; click to reload',
  'shell.staleShort': 'New version',
  'shell.collapse': 'Collapse sidebar',
  'shell.expand': 'Expand sidebar',
  'shell.nodes.summary': '{on}/{total} nodes online',
  'shell.needsYou': '{n} need you',
  'shell.conn.connected': 'Connected',
  'shell.conn.reconnecting': 'Reconnecting',
  'shell.conn.disconnected': 'Offline (polling)',
  'shell.conn.paused': 'Paused',
  'shell.conn.offline': 'Offline',

  'palette.title': 'Command palette',
  'palette.placeholder': 'Jump to a page, or p:<project> to filter',
  'palette.project': 'Project {p} only',
  'palette.empty': 'No matching page',

  'status.family.run': 'Running',
  'status.family.need': 'Needs you',
  'status.family.done': 'Done',
  'status.family.fail': 'Failed',
  'status.family.time': 'Timed out',
  'status.family.idle': 'Idle',
  'status.family.queue': 'Queued',

  'status.queued': 'Queued',
  'status.running': 'Running',
  'status.recovering': 'Recovering',
  'status.awaiting_input': 'Awaiting input',
  'status.pending_interaction': 'Needs reply',
  'status.needs_review': 'Needs review',
  'status.awaiting_approval': 'Awaiting approval',
  'status.waiting_dir': 'Waiting for directory',
  'status.paused_for_update': 'Waiting for update',
  'status.done': 'Done',
  'status.failed': 'Failed',
  'status.cancelled': 'Cancelled',
  'status.timeout': 'Timed out',
  'status.rejected': 'Rejected',
  'status.open': 'Open',
  'status.archived': 'Archived',
  'status.blocked': 'Blocked',
  'status.pending': 'Pending',
  'status.ready': 'Ready',
  'status.doing': 'In progress',
  'status.skipped': 'Skipped',
  'status.active': 'Active',
  'status.needs_me': 'Needs me',
  'status.waiting_resource': 'Waiting for resource',
  'status.needs_onsite': 'Needs on-site',
  'status.review': 'Needs review',
  'status.parked': 'Parked',
  'status.dropped': 'Dropped',
  'status.working': 'Working',
  'status.idle': 'Idle',

  'empty.none': 'Nothing here',
  'err.generic': 'Something went wrong',
  'toast.saved': 'Saved',
  'confirm.delete': 'Delete this record?',
  'hint.loading': 'Loading…',
  'field.project': 'Project',
  'field.allProjects': 'All projects',
  'field.agent': 'Agent',
  'help.shortcuts': 'Press Ctrl+K to open the command palette',

  // Unified composer (design §8; page-specific copy lives in i18n/pages/<page>.ts)
  'composer.ariaLabel': 'Message',
  'composer.send': 'Send',
  'composer.sending': 'Sending…',
  'composer.attach': 'Attach',
  'composer.urgent': 'Urgent',
  'composer.urgentTitle': 'Mark as urgent: the recipient sees it first',
  'composer.modeLabel': 'Send mode',
  'composer.mode.send': 'Send',
  'composer.mode.sendTitle': 'Send now',
  'composer.mode.queue': 'Queue',
  'composer.mode.queueTitle': 'Send automatically when this turn ends',
  'composer.mode.interrupt': 'Interrupt & send',
  'composer.mode.interruptTitle': 'Interrupt the current turn, then send',
  'composer.queued': 'Queued {n}',
  'composer.clearQueue': 'Clear',
  'composer.hintShow': 'Show composer help',
  'composer.hintHide': 'Hide composer help',
}
