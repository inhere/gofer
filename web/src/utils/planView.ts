// 计划详情「看板 / 列表」视图的记忆（issue gofer-5inz）。
//
// 判定顺序（唯一入口 resolvePlanView）：
//   1. URL 的 ?view=（可分享、前进后退可复原）——显式的 board / list 都算数；
//   2. 本浏览器上次的选择：按 plan 记一份，另存一份全局「最近一次」，从没看过的 plan 沿用；
//   3. 都没有 = 看板（与 ?view= 的默认同口径，默认值不写进 URL）。
//
// 存储读写一律兜错：SSR / 组件测试 / 隐私模式里没有可用的 localStorage，读不到就当没记过，
// 写失败也不影响这一次切换（只是下次不记得），绝不因为记忆抛错而阻断页面。
export type PlanView = 'board' | 'list'

// 全局「最近一次」的键；按 plan 的记忆是 `<全局键>:<plan id>`。
export const PLAN_VIEW_GLOBAL_KEY = 'gofer.plans.view'
// 默认视图：与 ?view= 契约一致（默认值不写进 URL）。
export const PLAN_VIEW_DEFAULT: PlanView = 'board'

const PLAN_VIEW_VALUES: readonly string[] = ['board', 'list']

export function isPlanView(value: unknown): value is PlanView {
  return typeof value === 'string' && PLAN_VIEW_VALUES.includes(value)
}

export function planViewStorageKey(planId: string): string {
  return `${PLAN_VIEW_GLOBAL_KEY}:${planId}`
}

// ViewStorage 只要读写的两件事：测试传内存实现，浏览器里就是 localStorage。
export type ViewStorage = Pick<Storage, 'getItem' | 'setItem'>

// viewStorage 取浏览器 localStorage；拿不到（SSR、隐私模式、被禁用）返回 null。
export function viewStorage(): ViewStorage | null {
  try {
    const storage = (globalThis as { localStorage?: ViewStorage }).localStorage
    return storage ?? null
  } catch {
    return null
  }
}

function readStored(storage: ViewStorage | null, key: string): PlanView | '' {
  if (!storage) {
    return ''
  }
  try {
    const raw = storage.getItem(key)
    return isPlanView(raw) ? raw : ''
  } catch {
    return ''
  }
}

// readPlanView：本浏览器对这个 plan 的记忆，没有就看全局的「最近一次」，都没有返回 ""。
export function readPlanView(planId: string, storage: ViewStorage | null = viewStorage()): PlanView | '' {
  return readStored(storage, planViewStorageKey(planId)) || readStored(storage, PLAN_VIEW_GLOBAL_KEY)
}

// rememberPlanView：切视图时按 plan + 全局各记一份。
export function rememberPlanView(
  planId: string,
  next: PlanView,
  storage: ViewStorage | null = viewStorage(),
): void {
  if (!storage) {
    return
  }
  for (const key of [planViewStorageKey(planId), PLAN_VIEW_GLOBAL_KEY]) {
    try {
      storage.setItem(key, next)
    } catch {
      // 配额满 / 存储被禁用：不记就是了，切换本身照常生效。
    }
  }
}

// resolvePlanView：视图的唯一判定入口；queryView 是 route.query.view 的原始值。
export function resolvePlanView(
  queryView: unknown,
  planId: string,
  storage: ViewStorage | null = viewStorage(),
): PlanView {
  if (isPlanView(queryView)) {
    return queryView
  }
  return readPlanView(planId, storage) || PLAN_VIEW_DEFAULT
}
