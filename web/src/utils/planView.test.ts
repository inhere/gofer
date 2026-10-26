// 计划详情的「看板 / 列表」视图记忆（utils/planView.ts，issue gofer-5inz）：
// URL 的 ?view= 优先 → 本浏览器上次的选择（按 plan，其次全局「最近一次」）→ 看板。
// 存储用内存实现注入，不依赖浏览器（测试环境是 node，没有 localStorage）。
import { describe, expect, it } from 'vitest'
import {
  PLAN_VIEW_DEFAULT,
  PLAN_VIEW_GLOBAL_KEY,
  planViewStorageKey,
  readPlanView,
  rememberPlanView,
  resolvePlanView,
  viewStorage,
  type ViewStorage,
} from './planView'

function memoryStorage(init: Record<string, string> = {}): ViewStorage & { data: Record<string, string> } {
  const data = { ...init }
  return {
    data,
    getItem: (key: string) => (key in data ? data[key] : null),
    setItem: (key: string, value: string) => {
      data[key] = value
    },
  }
}

describe('resolvePlanView', () => {
  it('lets an explicit ?view= win over the remembered choice', () => {
    const storage = memoryStorage({ [planViewStorageKey('p1')]: 'board' })
    expect(resolvePlanView('list', 'p1', storage)).toBe('list')
    const other = memoryStorage({ [planViewStorageKey('p1')]: 'list' })
    // 显式 board 也算数：URL 上写了就以 URL 为准（链接可分享）。
    expect(resolvePlanView('board', 'p1', other)).toBe('board')
  })

  it('falls back to this plan, then to the global last choice, then to board', () => {
    const both = memoryStorage({
      [planViewStorageKey('p1')]: 'board',
      [PLAN_VIEW_GLOBAL_KEY]: 'list',
    })
    expect(resolvePlanView(undefined, 'p1', both)).toBe('board')
    // 没看过的 plan：沿用全局「最近一次」，刷新 / 再打开不会跳回看板。
    expect(resolvePlanView(undefined, 'p2', both)).toBe('list')
    expect(resolvePlanView(undefined, 'p3', memoryStorage())).toBe(PLAN_VIEW_DEFAULT)
  })

  it('ignores junk in the URL and in storage', () => {
    const storage = memoryStorage({ [planViewStorageKey('p1')]: 'grid', [PLAN_VIEW_GLOBAL_KEY]: 'kanban' })
    expect(resolvePlanView('table', 'p1', storage)).toBe('board')
    expect(resolvePlanView(['list', 'board'], 'p1', storage)).toBe('board')
    expect(resolvePlanView(undefined, 'p1', storage)).toBe('board')
  })

  it('never throws when the browser has no usable storage', () => {
    expect(resolvePlanView(undefined, 'p1', null)).toBe('board')
    expect(readPlanView('p1', null)).toBe('')
    expect(() => rememberPlanView('p1', 'list', null)).not.toThrow()
    // node 里没有 localStorage：取存储返回 null，判定照常给默认值。
    expect(viewStorage()).toBeNull()
    expect(resolvePlanView(undefined, 'p1')).toBe('board')
  })
})

describe('rememberPlanView / readPlanView', () => {
  it('remembers the choice per plan and as the global last choice', () => {
    const storage = memoryStorage()
    rememberPlanView('p1', 'list', storage)
    expect(storage.data[planViewStorageKey('p1')]).toBe('list')
    expect(storage.data[PLAN_VIEW_GLOBAL_KEY]).toBe('list')
    // 新 plan 没有自己的记录：读全局那份。
    expect(readPlanView('p2', storage)).toBe('list')
  })

  it('keys the per-plan memory off the frozen global key', () => {
    expect(PLAN_VIEW_GLOBAL_KEY).toBe('gofer.plans.view')
    expect(planViewStorageKey('gofer-abc')).toBe('gofer.plans.view:gofer-abc')
  })

  it('swallows quota errors instead of breaking the switch', () => {
    const broken: ViewStorage = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('quota')
      },
    }
    expect(readPlanView('p1', broken)).toBe('')
    expect(() => rememberPlanView('p1', 'board', broken)).not.toThrow()
    expect(resolvePlanView(undefined, 'p1', broken)).toBe('board')
  })
})
