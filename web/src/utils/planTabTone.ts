// 计划详情页签计数的取色（issue gofer-qw2g）：徽标边框按含义取状态族，颜色仍由
// styles/status.css 的 .k-<族> 给出——这里只决定「用哪个族」，不另建颜色表。
//
// 口径（issue 原话）：有失败 / 受阻用告警色，进行中用运行色，全部完成用成功色，否则中性。
// 「告警色」按设计稿 §5 拆成两族：失败 = fail（红），受阻 = need（黄，黄色只代表「需要人」）。
import type { Job, Todo } from '../api/types'
import type { StatusFamily } from './statusFamily'

// 失败类终态：与状态文案的失败口径一致（超时、被驳回也是没交付的失败）。
const FAILED_STATUSES: ReadonlySet<string> = new Set(['failed', 'timeout', 'rejected'])

function latestJobStatus(todo: Todo): string {
  const latest = todo.jobs && todo.jobs.length > 0 ? todo.jobs[0] : undefined
  return latest ? latest.status : ''
}

function allTodosClosed(todos: Todo[]): boolean {
  return todos.length > 0 && todos.every((todo) => todo.done || todo.status === 'done' || todo.status === 'skipped')
}

// 待办页签：受阻（链停在某个条目，要人处理）→ need；最近一次 job 失败 → fail；
// 还有条目在做 → run；全部完成（含跳过）→ done；其余中性 queue。
export function todoTabTone(todos: Todo[], blocked = ''): StatusFamily {
  if (blocked) return 'need'
  if (todos.some((todo) => FAILED_STATUSES.has(latestJobStatus(todo)))) return 'fail'
  if (todos.some((todo) => todo.status === 'doing')) return 'run'
  if (allTodosClosed(todos)) return 'done'
  return 'queue'
}

// 任务页签：failed 用服务端汇总（plan.failed_jobs / counts.failed），job 自带的失败状态兜底；
// 有 job 在跑 → run；全部 done → done；其余（排队 / 空）中性。
export function jobsTabTone(jobs: Job[], failed = 0): StatusFamily {
  if (failed > 0 || jobs.some((job) => FAILED_STATUSES.has(job.status))) return 'fail'
  if (jobs.some((job) => job.status === 'running' || job.status === 'recovering')) return 'run'
  if (jobs.length > 0 && jobs.every((job) => job.status === 'done')) return 'done'
  return 'queue'
}

// 决策页签：还有 OPEN 决策 = 等人回答（黄色只给「需要人」）。
export function decisionsTabTone(open: number): StatusFamily {
  return open > 0 ? 'need' : 'queue'
}
