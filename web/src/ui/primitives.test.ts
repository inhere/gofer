// P2 列表基元的渲染与纯逻辑测试（设计稿 §8、§10、§13）：chip、项目筛选、页签深链、
// 抽屉外壳、返回链接、计划总览行。交互（点击 / Esc / 焦点）走纯函数与接线断言：
// 项目里没有 DOM 测试环境（只有 SSR），所以把可判定的部分都做成纯函数。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import FilterChips from './FilterChips.vue'
import FilterDrawer from './FilterDrawer.vue'
import ProjectFilter from './ProjectFilter.vue'
import Sheet from './Sheet.vue'
import Drawer from './Drawer.vue'
import Tabs from './Tabs.vue'
import BackLink from './BackLink.vue'
import PlanOverviewRow from './PlanOverviewRow.vue'
import { isChipSelected, nextChipValue } from './filterChips'
import { queryWithTab, tabFromQuery, type TabItem } from './tabs'
import { backTarget, readListReturn, saveListReturn, clearListReturn } from './listReturn'
import { defaultProject, readProjectQuery, readLastProject, rememberProject } from './projectFilter'
import { focusables } from './overlay'
import { locale } from '../store/locale'
import type { Plan } from '../api/types'

function fakeStorage(): Storage {
  const map = new Map<string, string>()
  return {
    get length() {
      return map.size
    },
    clear: () => map.clear(),
    getItem: (k: string) => map.get(k) ?? null,
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    removeItem: (k: string) => void map.delete(k),
    setItem: (k: string, v: string) => void map.set(k, v),
  } as Storage
}

async function render(
  comp: unknown,
  props: Record<string, unknown>,
  opts: { route?: string; slots?: Record<string, () => unknown> } = {},
): Promise<{ html: string; teleports: Record<string, string> }> {
  const router: Router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(comp as never, props, opts.slots as never) })
  app.use(router)
  await router.push(opts.route ?? '/jobs')
  await router.isReady()
  const ctx: { teleports?: Record<string, string> } = {}
  const html = await renderToString(app, ctx as never)
  return { html, teleports: ctx.teleports ?? {} }
}

describe('FilterChips 选中逻辑', () => {
  it('single select toggles off when the active chip is clicked again', () => {
    expect(nextChipValue('running', '')).toBe('running')
    expect(nextChipValue('running', 'queued')).toBe('running')
    expect(nextChipValue('running', 'running')).toBe('')
    expect(isChipSelected('running', 'running')).toBe(true)
    expect(isChipSelected('running', '')).toBe(false)
  })

  it('multi select accumulates and removes values', () => {
    expect(nextChipValue('open', ['open', 'blocked'], true)).toEqual(['blocked'])
    expect(nextChipValue('done', ['open'], true)).toEqual(['open', 'done'])
    expect(nextChipValue('done', '', true)).toEqual(['done'])
    expect(isChipSelected('open', ['open', 'done'])).toBe(true)
  })
})

describe('FilterChips', () => {
  const items = [
    { value: '', label: '全部' },
    { value: 'running', label: '运行中', count: 3 },
    { value: 'failed', label: '失败', count: 0 },
  ]

  it('marks the selected chip pressed and shows counts', async () => {
    const { html } = await render(FilterChips, { items, modelValue: 'running' })
    expect(html).toContain('role="group"')
    expect(html).toContain('aria-pressed="true"')
    expect(html).toContain('data-value="running"')
    expect(html).toContain('chip-count')
    expect(html).toContain('>3</span>')
    // 「全部」不带计数徽标
    expect(html.match(/class="chip-count"/g)?.length).toBe(2)
  })

  it('keeps a horizontally scrollable row on phones (styles, not layout JS)', () => {
    const source = Object.values(
      import.meta.glob('./FilterChips.vue', { eager: true, query: '?raw', import: 'default' }),
    )[0] as string
    expect(source).toContain('overflow-x: auto')
    expect(source).toContain('@media (max-width: 760px)')
    expect(source).toContain('flex-wrap: nowrap')
  })
})

describe('ProjectFilter', () => {
  it('shows 「全部项目」 when nothing is filtered, the key otherwise', async () => {
    expect((await render(ProjectFilter, { modelValue: '', options: [] })).html).toContain('全部项目')
    const html = (await render(ProjectFilter, { modelValue: 'p1', options: ['p1', 'p2'] })).html
    expect(html).toContain('p1')
    expect(html).toContain('aria-haspopup="listbox"')
    expect(html).toContain('aria-expanded="false"')
  })

  it('reads the filter from the URL and remembers the last project separately', () => {
    const storage = fakeStorage()
    expect(readProjectQuery({ project: 'p1' })).toBe('p1')
    expect(readProjectQuery({ project: ['p1', 'p2'] })).toBe('')
    expect(readProjectQuery(undefined)).toBe('')
    // 新建表单默认值：URL 筛选优先，其次上次用过的项目
    expect(defaultProject({ project: 'p1' }, storage)).toBe('p1')
    expect(defaultProject({}, storage)).toBe('')
    rememberProject('p2', storage)
    expect(readLastProject(storage)).toBe('p2')
    expect(defaultProject({}, storage)).toBe('p2')
    expect(defaultProject({ project: 'p1' }, storage)).toBe('p1')
    // 存储不可用时静默降级
    expect(readLastProject(null)).toBe('')
    expect(() => rememberProject('p3', null)).not.toThrow()
  })
})

describe('FilterDrawer', () => {
  it('counts active dimensions on the trigger and stays closed by default', async () => {
    const { html, teleports } = await render(FilterDrawer, { activeCount: 2 })
    expect(html).toContain('data-test="filter-drawer-trigger"')
    expect(html).toContain('更多筛选（2）')
    expect(html).toContain('aria-haspopup="dialog"')
    expect(html).toContain('aria-expanded="false"')
    // 关闭时不渲染抽屉内容（Teleport 里什么都没有）
    expect(JSON.stringify(teleports)).not.toContain('sheet-close')
  })
})

describe('Sheet / Drawer 外壳', () => {
  it('renders nothing while closed', async () => {
    expect((await render(Sheet, { modelValue: false })).teleports.body ?? '').not.toContain('role="dialog"')
    expect((await render(Drawer, { modelValue: false, label: 'x' })).teleports.body ?? '').not.toContain(
      'role="dialog"',
    )
  })

  it('renders a modal dialog with a handle, a close button and the labelled title', async () => {
    const { teleports } = await render(Sheet, { modelValue: true, label: '筛选' })
    const body = teleports.body ?? ''
    expect(body).toContain('role="dialog"')
    expect(body).toContain('aria-modal="true"')
    expect(body).toContain('data-test="sheet-handle"')
    expect(body).toContain('data-test="sheet-close"')
    expect(body).toContain('aria-label="筛选"')
  })

  it('renders a side drawer dialog when open', async () => {
    const { teleports } = await render(Drawer, { modelValue: true, label: '本地终端', side: 'left' })
    const body = teleports.body ?? ''
    expect(body).toContain('role="dialog"')
    expect(body).toContain('dw-panel--left')
    expect(body).toContain('aria-label="本地终端"')
  })

  it('wires Esc / Tab / focus restore through the shared overlay behaviour', async () => {
    const sheet = Object.values(
      import.meta.glob('./Sheet.vue', { eager: true, query: '?raw', import: 'default' }),
    )[0] as string
    const drawer = Object.values(
      import.meta.glob('./Drawer.vue', { eager: true, query: '?raw', import: 'default' }),
    )[0] as string
    for (const source of [sheet, drawer]) {
      expect(source).toContain('useOverlayBehavior(open, { panel })')
      expect(source).toContain('@keydown="onKeydown"')
      expect(source).toContain('@click="open = false"')
      expect(source).toContain('role="dialog"')
    }
    // 焦点循环在没有面板时也不崩
    expect(focusables(null)).toEqual([])
  })
})

describe('Tabs 深链', () => {
  const tabs: TabItem[] = [
    { value: 'overview', label: '概览' },
    { value: 'logs', label: '日志', count: 4 },
    { value: 'review', label: '验收', badge: true },
  ]

  it('takes the active tab from ?tab= and gives unknown values the default', () => {
    expect(tabFromQuery({ tab: 'logs' }, tabs)).toBe('logs')
    expect(tabFromQuery({ tab: 'nope' }, tabs)).toBe('overview')
    expect(tabFromQuery({}, tabs)).toBe('overview')
    expect(tabFromQuery({ tab: 'review' }, tabs, 'tab', 'logs')).toBe('review')
    expect(tabFromQuery({}, tabs, 'tab', 'logs')).toBe('logs')
  })

  it('keeps other query parameters and omits the default tab from the URL', () => {
    expect(queryWithTab({ project: 'p1', tab: 'logs' }, 'overview', tabs)).toEqual({ project: 'p1' })
    expect(queryWithTab({ project: 'p1' }, 'logs', tabs)).toEqual({ project: 'p1', tab: 'logs' })
    expect(queryWithTab({ empty: '', tab: 'logs' }, 'review', tabs)).toEqual({ tab: 'review' })
  })

  it('renders a tablist with roving tabindex and deep-links the active panel', async () => {
    const { html } = await render(Tabs, { tabs }, { route: '/jobs/x?tab=logs' })
    expect(html).toContain('role="tablist"')
    expect(html).toContain('role="tabpanel"')
    expect(html).toContain('aria-selected="true"')
    // 只有选中的页签在 Tab 序列里
    expect(html.match(/tabindex="0"/g)?.length).toBe(1)
    expect(html).toContain('id="tab-tab-logs"')
    expect(html).toContain('id="panel-tab-logs"')
    // 计数与「需要人」角标
    expect(html).toContain('>4</span>')
    expect(html).toContain('tab-badge')
  })

  it('supports arrow / Home / End key handling in the component', () => {
    const source = Object.values(
      import.meta.glob('./Tabs.vue', { eager: true, query: '?raw', import: 'default' }),
    )[0] as string
    for (const key of ["'ArrowRight'", "'ArrowLeft'", "'Home'", "'End'"]) {
      expect(source).toContain(key)
    }
    expect(source).toContain('@keydown="onKeydown($event, index)"')
  })
})

describe('BackLink / listReturn', () => {
  it('saves, reads and clears the list position per list id', () => {
    const storage = fakeStorage()
    expect(readListReturn('jobs', storage)).toBeNull()
    saveListReturn('jobs', { url: '/jobs?project=p1&status=running', scrollY: 420 }, storage)
    expect(readListReturn('jobs', storage)).toEqual({ url: '/jobs?project=p1&status=running', scrollY: 420 })
    expect(backTarget('jobs', '/jobs', storage)).toBe('/jobs?project=p1&status=running')
    clearListReturn('jobs', storage)
    expect(readListReturn('jobs', storage)).toBeNull()
    // 坏值不影响页面
    storage.setItem('gofer.return.plans', 'not json')
    expect(readListReturn('plans', storage)).toBeNull()
    expect(backTarget('plans', '/plans', storage)).toBe('/plans')
  })

  it('falls back to the given list route when nothing was remembered', async () => {
    const { html } = await render(BackLink, { list: 'jobs', to: '/jobs' })
    expect(html).toContain('data-test="back-link"')
    expect(html).toContain('data-target="/jobs"')
    expect(html).toContain('返回')
  })
})

describe('PlanOverviewRow（计划总览一行）', () => {
  function plan(patch: Partial<Plan>): Plan {
    return {
      plan_id: 'gofer-hez1-redesign',
      status: 'open',
      created_at: 1_700_000_000,
      updated_at: 1_700_000_100,
      ...patch,
    }
  }

  it('renders progress, blockers, main session and last activity', async () => {
    const before = locale.value
    locale.value = 'zh-CN'
    try {
      const { html } = await render(
        PlanOverviewRow,
        {
          plan: plan({
            title: '改版 P2',
            project: 'gofer',
            tags: ['ui'],
            supervisor_session_id: 'job-0123456789abcdef',
            blocked_todo: 't-9',
            todo_counts: { total: 6, pending: 2, doing: 1, done: 2, skipped: 1 },
            counts: { total: 3, queued: 0, running: 1, done: 1, failed: 1 },
          }),
        },
        { route: '/plans' },
      )
      expect(html).toContain('data-test="plan-row"')
      expect(html).toContain('改版 P2')
      expect(html).toContain('gofer-hez1-redesign')
      expect(html).toContain('data-test="progress-bar"')
      // 卡点：卡住的 todo + 失败的 job + 进行中的 todo
      expect(html).toContain('卡在 t-9')
      expect(html).toContain('1 个 job 失败')
      expect(html).toContain('1 进行中')
      // 主会话只显示尾部（完整 id 在 title 里）
      expect(html).toContain('…456789abcdef')
      expect(html).toContain('data-test="plan-supervisor"')
      // 最近活动 = 相对时间（列头在 Plans.vue，行里只给“多前”）
      expect(html).toContain('prow-activity')
    } finally {
      locale.value = before
    }
  })

  it('says 无卡点 and shows a dash when there is no blocker or session', async () => {
    const before = locale.value
    locale.value = 'zh-CN'
    try {
      const { html } = await render(PlanOverviewRow, { plan: plan({}) }, { route: '/plans' })
      expect(html).toContain('无卡点')
      expect(html).toContain('—')
    } finally {
      locale.value = before
    }
  })
})
