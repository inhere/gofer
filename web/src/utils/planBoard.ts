// 计划看板（WEB-10）的纯逻辑：分列、可拖转移、进度、依赖。与 PlanBoard.vue 分离，
// 便于用 node 直接跑断言（本文件刻意不 import 任何模块，只描述它需要的最小结构）。
//
// 服务端事实（Q2 已齐）：
//  - todo.status 生命周期 pending → ready → doing → done|skipped；ready 且有 assignee
//    且无活跃 job 时，服务端在这一次 PATCH 里立刻起 job 并把 todo 转 doing。
//  - todo.jobs（最多 10 条，新→旧）是挂接在该 todo 上的 job；jobs[0] = 最近一次。
//  - todo.after 是同 plan 内它等待的 todo id；auto=false 表示链不许自动启动它。

export type BoardColumn = 'pending' | 'ready' | 'doing' | 'needs_review' | 'done'

// 拖拽目标：五列 + 一个「跳过」落点（skipped 在 done 列里，但它是一次独立的 PATCH）。
export type BoardTarget = BoardColumn | 'skipped'

export interface BoardJob {
  id: string
  status: string
}

// 看板只需要 Todo 的这几个字段；Todo 多出来的字段结构上兼容（Vue 侧直接传 plan.todos）。
export interface BoardTodo {
  todo_id: string
  title: string
  status: string
  assignee?: string
  after?: string[]
  auto?: boolean
  jobs?: readonly BoardJob[]
}

// 列顺序 = 展示顺序；doing/needs_review 两列由服务端驱动，不作为拖拽目标。
export const BOARD_COLUMNS: readonly BoardColumn[] = [
  'pending', 'ready', 'doing', 'needs_review', 'done',
] as const

// 非终态的 job 状态（与 PlanDetail.todoLiveJob 同一集合）：ready → pending 的撤回要求
// 「没有活跃 job」，否则服务端仍在跑、撤不回去。
const LIVE_JOB_STATUSES: Record<string, true> = {
  queued: true,
  running: true,
  pending_interaction: true,
  recovering: true,
  waiting_dir: true,
  needs_review: true,
}

// 最近一次 job 的状态（jobs 新→旧，故取第一条）；没有 job 返回 ""。
export function latestJobStatus(t: BoardTodo): string {
  return t.jobs && t.jobs.length > 0 ? t.jobs[0].status : ''
}

// 归列：pending/ready/done 直落；skipped 归 done 列（灰、列尾）；doing 且最近一次 job
// 在 needs_review（agent 干完等人裁决）→ needs_review 列——这一列不新增 todo 状态，
// 纯看板视图，服务端仍是 doing。
export function columnOf(t: BoardTodo): BoardColumn {
  switch (t.status) {
    case 'ready':
      return 'ready'
    case 'doing':
      return latestJobStatus(t) === 'needs_review' ? 'needs_review' : 'doing'
    case 'done':
    case 'skipped':
      return 'done'
    default:
      return 'pending'
  }
}

// 该列接受哪些落点（= 落到该列时 PATCH 的 status）。doing/needs_review 不可拖入；
// done 列有两个落点：完成与跳过。
export function dropTargets(col: BoardColumn): BoardTarget[] {
  if (col === 'done') {
    return ['done', 'skipped']
  }
  if (col === 'doing' || col === 'needs_review') {
    return []
  }
  return [col]
}

// 允许的转移（看板拖拽的唯一判据，Vue 侧只负责按它渲染禁用态）：
//   pending → ready（派发） / pending|ready|doing|needs_review → done|skipped（人工标记）
//   ready → pending（撤回，仅当没有活跃 job）
// 其余一律拒绝：doing/needs_review 不可拖入（服务端驱动），done 列不可拖出。
export function allowedMove(from: BoardColumn, to: BoardTarget, todo: BoardTodo): boolean {
  if (to === 'doing' || to === 'needs_review') {
    return false
  }
  if (to === 'pending') {
    return from === 'ready' && LIVE_JOB_STATUSES[latestJobStatus(todo)] !== true
  }
  if (to === 'ready') {
    return from === 'pending'
  }
  if (to === 'done' || to === 'skipped') {
    return from !== 'done'
  }
  return false
}

// 列内卡片：done 列把 skipped 排到列尾（其余保持服务端次序，稳定不重排）。
export function columnTodos<T extends BoardTodo>(todos: readonly T[], col: BoardColumn): T[] {
  const list = todos.filter((t) => columnOf(t) === col)
  if (col !== 'done') {
    return list
  }
  return [
    ...list.filter((t) => t.status !== 'skipped'),
    ...list.filter((t) => t.status === 'skipped'),
  ]
}

export interface BoardProgress {
  done: number
  total: number
  percent: number | null
}

// 头部进度：done + skipped 计完成；没有待办时 percent 为 null（"没得量" ≠ 0%）。
export function boardProgress(todos: readonly BoardTodo[]): BoardProgress {
  const total = todos.length
  const done = todos.filter((t) => t.status === 'done' || t.status === 'skipped').length
  return { done, total, percent: total === 0 ? null : Math.round((done * 100) / total) }
}

// 依赖是否全部满足：after 里的每一项都已 done/skipped。空 after（根节点）恒为 true；
// after 指向本 plan 里不存在的 id 时按未满足处理（灰角标，比假装满足安全）。
export function depsSatisfied(t: BoardTodo, all: readonly BoardTodo[]): boolean {
  const after = t.after ?? []
  return after.every((id) => {
    const dep = all.find((x) => x.todo_id === id)
    return !!dep && (dep.status === 'done' || dep.status === 'skipped')
  })
}

// auto=false 才显示「手动」标签（Vue 侧直接判 t.auto === false）：老服务端不发 auto
// （undefined）时按服务端默认 auto=1 处理，故不能用 falsy 判断。
