// 状态族（设计稿 §5）：job / 会话 / 待办 / 计划 / 节点 / 工作项的状态值全部映射到 7 个族。
//
// 界面只认族：形状 + 颜色一起编码（StatusGlyph 给形状，StatusPill 给形状 + 文字），
// 色弱和灰度显示也能分辨。族 → 颜色在 tokens.css：run / need / done / fail / time / idle
// 各有 --<族> 与 --<族>-soft；queue 用中性色 --ink-3（设计稿明确 queue 不带自己的颜色）。
//
// 新增状态值时在这里登记，并补 statusFamily.test.ts 的穷举用例——不要在各页面写 switch。
import type { JobStatus, PlanStatus, TodoStatus, WorkStatus, WorkflowStatus } from '../api/types'

export type StatusFamily = 'run' | 'need' | 'done' | 'fail' | 'time' | 'idle' | 'queue'

// 族的固定顺序，供图例、筛选芯片、测试遍历使用。
export const STATUS_FAMILIES: readonly StatusFamily[] = [
  'run',
  'need',
  'done',
  'fail',
  'time',
  'idle',
  'queue',
]

// 状态值 → 族。同一个值在不同实体上是同一族（design §5 的一张表）。
// 少数值天然多义：'pending'（待办未开始 vs 待应答的交互）按设计稿归 queue，
// 交互类调用方需要黄色「需要人」时显式传 family。
const FAMILY: Record<string, StatusFamily> = {
  // run：正在跑
  running: 'run',
  recovering: 'run',
  doing: 'run',
  working: 'run',
  active: 'run',
  in_progress: 'run',
  open: 'run',

  // need：需要人（审批、提问、待验收、等回复、受阻、节点申请）
  awaiting_input: 'need',
  pending_interaction: 'need',
  needs_review: 'need',
  awaiting_approval: 'need',
  waiting_reply: 'need',
  blocked: 'need',
  needs_me: 'need',
  needs_onsite: 'need',
  review: 'need',

  // done：完成
  done: 'done',
  accepted: 'done',
  passed: 'done',

  // fail：失败、拒绝、阻塞型结束
  failed: 'fail',
  rejected: 'fail',
  down: 'fail',
  expired: 'fail',

  // time：超时
  timeout: 'time',

  // idle：空闲、取消、离线、跳过、暂停、归档
  idle: 'idle',
  cancelled: 'idle',
  offline: 'idle',
  skipped: 'idle',
  paused: 'idle',
  paused_for_update: 'idle',
  archived: 'idle',
  parked: 'idle',
  dropped: 'idle',
  draft: 'idle',
  stopped: 'idle',
  unknown: 'idle',

  // queue：排队、等资源、待办未开始
  queued: 'queue',
  waiting_dir: 'queue',
  waiting_resource: 'queue',
  pending: 'queue',
  ready: 'queue',
  todo: 'queue',
}

// statusFamily：状态值 → 族。空值和没登记的新状态落到 idle（空心灰圆），
// 界面上不会因为后端加了状态而变成空白或误报「需要人」。
export function statusFamily(status: string | undefined | null): StatusFamily {
  if (!status) return 'idle'
  return FAMILY[status] ?? 'idle'
}

// 状态族的单元格类名（.k-run …）：CSS 里由它给出 --c（主色）与 --soft（底色）。
// 需要上色的地方都用这个类名 + var(--c)，不要再另建一份「状态 → 颜色」表：
// 颜色的唯一来源是 styles/status.css（旧 api/client 的 statusColor 已在 P6 删除）。
export function statusFamilyClass(family: StatusFamily): string {
  return `k-${family}`
}

// 已登记的映射表（测试用它做穷举，页面不要直接读）。
export const STATUS_FAMILY_MAP: Readonly<Record<string, StatusFamily>> = FAMILY

// 各实体的状态值清单：测试把这里的每个值都喂给 statusFamily，确保没有漏登记。
export const ENTITY_STATUSES = {
  job: [
    'queued',
    'running',
    'awaiting_input',
    'pending_interaction',
    'needs_review',
    'awaiting_approval',
    'waiting_dir',
    'paused_for_update',
    'recovering',
    'done',
    'failed',
    'cancelled',
    'timeout',
    'rejected',
  ] satisfies JobStatus[],
  plan: ['open', 'done', 'archived', 'blocked'] satisfies PlanStatus[],
  todo: ['pending', 'ready', 'doing', 'done', 'skipped'] satisfies TodoStatus[],
  workflow: ['running', 'done', 'failed', 'cancelled'] satisfies WorkflowStatus[],
  work: [
    'active',
    'needs_me',
    'waiting_resource',
    'needs_onsite',
    'review',
    'parked',
    'done',
    'dropped',
  ] satisfies WorkStatus[],
  // 会话（工作台）的三种：跑着 / 等人 / 空闲
  session: ['working', 'blocked', 'idle'],
} as const
