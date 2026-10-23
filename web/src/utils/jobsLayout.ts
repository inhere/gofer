// 任务页（/jobs 列表 + /jobs/:id 详情）的布局与默认页签契约（设计稿 §7.1、§16）。
// 纯函数：布局判断与「按状态定默认页签」都在这里，组件与测试共用。
//
// 布局规则（§16 用户追加要求）：
// - 主内容区宽度 ≥ 1280px 才可以列表 + 详情并排，且详情面板不得窄于 760px；
// - 宽度不够（或用户选了「单栏」）时列表占满，点开进完整的 /jobs/:id；
// - 手机（≤760）主内容区永远到不了 1280，自然只有两级页面。
export const SPLIT_MIN_CONTENT_WIDTH = 1280
export const DETAIL_MIN_WIDTH = 760
// 并排时留给列表的宽度；1280 - 520 = 760，正好是详情的最小宽度。
export const LIST_MIN_WIDTH = 520
export const SPLIT_STORAGE_KEY = 'gofer.jobsSplit'

export type JobsLayoutPreference = 'split' | 'single'

function storageOr(safe?: Storage | null): Storage | null {
  if (safe !== undefined) return safe
  try {
    return typeof window !== 'undefined' && window.localStorage ? window.localStorage : null
  } catch {
    return null
  }
}

// 「单栏 / 并排」选择记在本地（§16：不影响别人、也不该进 URL）。
export function readSplitPreference(storage?: Storage | null): JobsLayoutPreference {
  try {
    return storageOr(storage)?.getItem(SPLIT_STORAGE_KEY) === 'single' ? 'single' : 'split'
  } catch {
    return 'split'
  }
}

export function saveSplitPreference(value: JobsLayoutPreference, storage?: Storage | null): void {
  try {
    storageOr(storage)?.setItem(SPLIT_STORAGE_KEY, value)
  } catch {
    // 存不下就算了：只是布局偏好，退回默认「并排」
  }
}

// 主内容区宽度够不够并排（详情面板 ≥ DETAIL_MIN_WIDTH）。
export function splitAllowed(contentWidth: number): boolean {
  if (!Number.isFinite(contentWidth)) return false
  return contentWidth >= SPLIT_MIN_CONTENT_WIDTH && contentWidth - LIST_MIN_WIDTH >= DETAIL_MIN_WIDTH
}

// 最终是不是并排：宽度够 + 用户没选「单栏」。
export function splitLayout(contentWidth: number, preference: JobsLayoutPreference = 'split'): boolean {
  return preference !== 'single' && splitAllowed(contentWidth)
}

// 并排时详情面板分到的宽度（只用于测试与布局断言；CSS 侧靠 grid 的 minmax 兜底）。
export function detailPaneWidth(contentWidth: number): number {
  return Math.max(0, contentWidth - LIST_MIN_WIDTH)
}

// 页签值（§7.1 任务详情的页签清单），顺序即页签顺序。
export const JOB_TAB = {
  overview: 'overview',
  review: 'review',
  log: 'log',
  changes: 'changes',
  messages: 'messages',
  artifacts: 'artifacts',
  events: 'events',
} as const

export type JobTabValue = (typeof JOB_TAB)[keyof typeof JOB_TAB]

export const JOB_TAB_ORDER: JobTabValue[] = [
  JOB_TAB.overview,
  JOB_TAB.review,
  JOB_TAB.log,
  JOB_TAB.changes,
  JOB_TAB.messages,
  JOB_TAB.artifacts,
  JOB_TAB.events,
]

// 默认页签按状态（§16-5）：needs_review → 验收，failed → 日志，其余 → 概览。
export function jobTabForStatus(status?: string | null): JobTabValue {
  if (status === 'needs_review') return JOB_TAB.review
  if (status === 'failed') return JOB_TAB.log
  return JOB_TAB.overview
}
