import { afterEach, describe, expect, it, vi } from 'vitest'

// 用户管理页（设置 → 用户）的两半：api 层打的请求，以及 utils/callers.ts 的纯判定。
// 页面本身只取数与渲染，所以这里不需要挂载组件；token 的处理按源码口径钉住（不落地、关掉即清）。
const calls: Array<{ url: string; init?: RequestInit }> = []
vi.mock('../../api/client', () => ({
  request: vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init })
    return { callers: [], count: 0, entries: [], caller: { id: 'ci' }, token: 'gct_x' }
  }),
}))

import {
  addCaller,
  listCallerAudit,
  listCallers,
  rotateCaller,
  setCallerCeiling,
  setCallerLimits,
} from '../../api/callers'
import {
  addBlocker,
  asAuthCeiling,
  canDisable,
  canEnable,
  canRotate,
  ceilingBlocker,
  ceilingFromForm,
  ceilingSummaryLines,
  ceilingToForm,
  emptyCeilingForm,
  isFileCaller,
  limitBlocker,
  quotaText,
  splitPatterns,
} from '../../utils/callers'
import type { CallerView } from '../../api/types'

afterEach(() => {
  calls.length = 0
})

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const page = raw(import.meta.glob('./Callers.vue', { eager: true, query: '?raw', import: 'default' }))

function view(over: Partial<CallerView> = {}): CallerView {
  return { id: 'ci', source: 'db', state: 'active', token_set: true, ...over }
}

describe('用户管理接口', () => {
  it('列出用户与审计走对应路由', async () => {
    await listCallers()
    expect(calls[0].url).toBe('/v1/callers')
    await listCallerAudit(20)
    expect(calls[1].url).toBe('/v1/callers/audit?limit=20')
    await listCallerAudit()
    expect(calls[2].url).toBe('/v1/callers/audit')
  })

  it('新增把上限与「不设上限」二选一地带出去，id 走 URL 时不转义出问题', async () => {
    await addCaller({ id: 'ci', max_concurrent_jobs: 2, rate_limit: 1.5, rate_burst: 3, no_ceiling: true })
    expect(calls[0].url).toBe('/v1/callers')
    expect(calls[0].init?.method).toBe('POST')
    expect(JSON.parse(String(calls[0].init?.body))).toMatchObject({ id: 'ci', no_ceiling: true, max_concurrent_jobs: 2 })
    await rotateCaller('a/b')
    expect(calls[1].url).toBe('/v1/callers/a%2Fb/rotate')
    expect(calls[1].init?.method).toBe('POST')
  })

  it('改上限与改配额是 PUT，body 里只有该改的东西', async () => {
    await setCallerCeiling('ci', { ceiling: { projects: ['app-*'], read_only: true } })
    expect(calls[0].init?.method).toBe('PUT')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ ceiling: { projects: ['app-*'], read_only: true } })
    await setCallerLimits('ci', { max_concurrent_jobs: 4, rate_limit: 0, rate_burst: 0 })
    expect(calls[1].url).toBe('/v1/callers/ci/limits')
    expect(JSON.parse(String(calls[1].init?.body))).toEqual({ max_concurrent_jobs: 4, rate_limit: 0, rate_burst: 0 })
  })
})

describe('上限表单', () => {
  it('拆 glob 列表：逗号、空格、换行都算分隔符', () => {
    expect(splitPatterns('app-*, docs\nw-1  w-2,,')).toEqual(['app-*', 'docs', 'w-1', 'w-2'])
    expect(splitPatterns('   ')).toEqual([])
  })

  it('什么都没填时返回 null（空上限块不是「不限」，服务端会 400）', () => {
    expect(ceilingFromForm(emptyCeilingForm())).toBeNull()
    expect(ceilingFromForm({ ...emptyCeilingForm(), projects: 'app-*' })).toEqual({ projects: ['app-*'] })
    // 只勾开关也算「填了一项」。
    expect(ceilingFromForm({ ...emptyCeilingForm(), readOnly: true })).toEqual({ read_only: true })
  })

  it('表单与上限视图能互相还原', () => {
    const form = ceilingToForm({ projects: ['a', 'b'], runners: ['server'], allow_exec: true })
    expect(form.projects).toBe('a, b')
    expect(form.runners).toBe('server')
    expect(form.allowExec).toBe(true)
    expect(form.globalMemory).toBe(false)
    expect(ceilingFromForm(form)).toEqual({ projects: ['a', 'b'], runners: ['server'], allow_exec: true })
  })

  it('三个开关缺省按严格默认折给摘要（allow_exec / global_memory 关，非只读）', () => {
    expect(asAuthCeiling({})).toMatchObject({ allow_exec: false, global_memory: false, read_only: false })
    expect(ceilingSummaryLines(null)).toEqual([])
    expect(ceilingSummaryLines({ projects: ['app-*'] }).length).toBeGreaterThan(0)
  })
})

describe('提交前校验', () => {
  it('新增要求 id，选了「受限」至少填一项，选「不设上限」则不用填', () => {
    const form = emptyCeilingForm()
    expect(addBlocker({ id: '', mode: 'none', form })).not.toBe('')
    expect(addBlocker({ id: 'ci', mode: 'limited', form })).not.toBe('')
    expect(addBlocker({ id: 'ci', mode: 'none', form })).toBe('')
    expect(addBlocker({ id: 'ci', mode: 'limited', form: { ...form, agents: 'codex' } })).toBe('')
  })

  it('改上限走同一套规则', () => {
    expect(ceilingBlocker('limited', emptyCeilingForm())).not.toBe('')
    expect(ceilingBlocker('none', emptyCeilingForm())).toBe('')
  })

  it('配额必须是非负数（限流可带小数，整数位不能是 NaN / 负）', () => {
    expect(limitBlocker('', '', '')).toBe('')
    expect(limitBlocker('4', '1.5', '10')).toBe('')
    expect(limitBlocker('-1', '', '')).not.toBe('')
    expect(limitBlocker('', 'abc', '')).not.toBe('')
    expect(limitBlocker('', '', '2.5')).not.toBe('')
  })
})

describe('行的可用动作', () => {
  it('文件来源的用户不能轮换 / 停用 / 启用', () => {
    const v = view({ source: 'file' })
    expect(isFileCaller(v)).toBe(true)
    expect(canRotate(v)).toBe(false)
    expect(canDisable(v)).toBe(false)
    expect(canEnable(v)).toBe(false)
  })

  it('数据库用户按状态决定停用还是启用', () => {
    expect(canRotate(view())).toBe(true)
    expect(canDisable(view({ state: 'active' }))).toBe(true)
    expect(canEnable(view({ state: 'active' }))).toBe(false)
    expect(canEnable(view({ state: 'disabled' }))).toBe(true)
    expect(canEnable(view({ state: 'superseded' }))).toBe(true)
    expect(canDisable(view({ state: 'disabled' }))).toBe(false)
  })

  it('配额写成一行的紧凑形式（0 = 用默认）', () => {
    expect(quotaText(view({ max_concurrent_jobs: 2, rate_limit: 1.5, rate_burst: 4 }))).toBe('2 / 1.5 / 4')
    expect(quotaText(view())).toBe('- / - / -')
  })
})

describe('token 只在页面上显示一次', () => {
  it('页面不把 token 写进任何浏览器存储', () => {
    expect(page).not.toContain('localStorage')
    expect(page).not.toContain('sessionStorage')
    expect(page).not.toContain('document.cookie')
  })

  it('token 提示块只在 reveal 有值时渲染，关闭按钮把它清空', () => {
    expect(page).toContain('<div v-if="reveal" class="reveal" data-testid="caller-token">')
    expect(page).toMatch(/function closeReveal\(\): void \{\s*reveal\.value = null/)
    expect(page).toContain('data-testid="caller-token-close"')
    // 只有新增与轮换会把 token 放进 reveal（其余动作的响应没有 token）。
    expect(page.match(/reveal\.value = \{ id:/g)?.length).toBe(2)
  })
})
