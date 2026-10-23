// 「工作项」页（P4）的验收测试：字典 key 全覆盖、卡片标记（停滞 / 到期 / 离线）、
// 以及页面结构（chip / 项目 chip / 抽屉外壳 / 统一输入框 / 手机断点）。
import { describe, expect, it, afterEach } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { WorkItem } from '../api/types'
import WorkCard from '../components/WorkCard.vue'
import { dicts } from '../i18n'
import { setLocale } from '../store/locale'
import { work as workDict } from '../i18n/pages/work'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const page = raw(import.meta.glob('./Work.vue', { eager: true, query: '?raw', import: 'default' }))
const card = raw(import.meta.glob('../components/WorkCard.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = raw(import.meta.glob('../components/WorkDrawer.vue', { eager: true, query: '?raw', import: 'default' }))
const board = raw(import.meta.glob('../utils/workBoard.ts', { eager: true, query: '?raw', import: 'default' }))

function item(over: Partial<WorkItem> = {}): WorkItem {
  return {
    id: 'w-abc', title: '订单导出', goal: '给订单页加 xlsx 导出', status: 'active', status_source: 'auto', source: 'auto',
    unsorted: false, rev: 3, created_at: 1, updated_at: 1, last_activity_at: 1000, due: false, session_offline: false,
    blocker_text: '', next_step: '', project_key: 'self', workspace: '/ws/shop/app',
    sessions: [{ session_id: 's1', role: 'current', agent: 'claude', runner: 'local', state: 'running', offline: false, last_seen_at: 990 }],
    session_ids: ['s1'], links: [], ...over,
  }
}

async function render(props: Record<string, unknown>): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(WorkCard as any, props) }))
}

afterEach(() => setLocale('zh-CN'))

describe('work page dictionary', () => {
  it('uses only keys the page dictionary defines, in both languages', () => {
    const sources = [page, card, drawer, board].join('\n')
    const used = new Set<string>()
    for (const m of sources.matchAll(/\bt\(\s*'([a-z0-9_.]+)'/g)) used.add(m[1])
    for (const m of sources.matchAll(/'((?:work|status)\.[a-z0-9_.]+)'/g)) used.add(m[1])
    const missingZh = [...used].filter((k) => !(k in workDict.zh) && !(k in dicts['zh-CN']))
    const missingEn = [...used].filter((k) => !(k in workDict.en) && !(k in dicts['en-US']))
    expect(missingZh).toEqual([])
    expect(missingEn).toEqual([])
  })

  it('keeps zh and en key sets identical', () => {
    expect(Object.keys(workDict.en).sort()).toEqual(Object.keys(workDict.zh).sort())
  })

  it('has no Chinese left in the English strings', () => {
    const han = /[\u4e00-\u9fff]/
    // 「（n 分钟后超时）」这类插值之外的括号不在此列：英文串里不该出现汉字
    expect(Object.entries(workDict.en).filter(([, v]) => han.test(v)).map(([k]) => k)).toEqual([])
  })
})

describe('work card markers', () => {
  it('marks a stalled item and explains it', async () => {
    const html = await render({ item: item({ last_activity_at: 1000 }), nowSec: 1000 + 4 * 86400 })
    expect(html).toContain('data-test="stalled-badge"')
    expect(html).toContain('停滞 4 天')
  })

  it('does not mark a closed, parked or waiting item as stalled', async () => {
    for (const over of [
      { status: 'done' as const },
      { status: 'parked' as const },
      { unsorted: true },
      { requests: [{ id: 'r', work_item_id: 'w-abc', session_id: 's1', kind: 'report' as const, state: 'sent' as const, by: 'human:me', created_at: 0 }] },
    ]) {
      const html = await render({ item: item({ last_activity_at: 1000, ...over }), nowSec: 1000 + 9 * 86400 })
      expect(html).not.toContain('data-test="stalled-badge"')
    }
  })

  it('renders the status as a P0 status pill and keeps the due / offline markers', async () => {
    const html = await render({ item: item({ due: true, session_offline: true }) })
    expect(html).toContain('data-testid="status-pill"')
    expect(html).toContain('data-test="due-badge"')
    expect(html).toContain('提醒到期')
    expect(html).toContain('data-test="offline-badge"')
  })

  it('speaks English when the locale is English (business content stays as written)', async () => {
    setLocale('en-US')
    const html = await render({ item: item({ due: true }), nowSec: 1000 })
    expect(html).toContain('订单导出') // title is content, not UI copy
    expect(html).toContain('Due')
    expect(html).toContain('Open session')
    expect(html).toContain('Tidy')
  })
})

describe('work page structure', () => {
  it('puts the title first, then a one-line summary and the filters', () => {
    const head = page.indexOf('class="page-head"')
    const filters = page.indexOf('class="filter-row"')
    expect(head).toBeGreaterThan(-1)
    expect(filters).toBeGreaterThan(head)
    expect(page).toContain("t('work.h.title')")
    expect(page).toContain("t('work.h.summary', { n: summaryCounts.open, needs: summaryCounts.needs_me })")
    expect(page).toContain('<ProjectFilter')
    expect(page).toContain('<FilterChips')
  })

  it('filters by status through ?status= and keeps the old capture deep link working', () => {
    expect(page).toContain('laneFromQuery(route.query)')
    expect(page).toContain('laneQuery(route.query, next)')
    expect(board).toContain("if (tab === 'capture') return CAPTURE_LANE_STATUS")
    // 同一个空值约定：再点一次已选中的 chip 回到「全部」
    expect(board).toContain("if (lane) next.status = lane")
    expect(board).toContain("if (typeof v === 'string' && v && k !== 'status' && k !== 'tab') next[k] = v")
  })

  it('flattens the status view into one list ordered by lane instead of grouped columns', () => {
    expect(page).toContain('data-test="status-view"')
    expect(page).toContain('sortByLane(')
    expect(page).not.toContain('work-cols')
    expect(page).not.toContain('groupByStatus')
    // 状态顺序只在一处定义，页面不另写一份
    expect(board).toContain('export const WORK_LANES')
    expect(board).toContain("'needs_me',\n  'review',\n  'active'")
  })

  it('colours the card left edge by status family and keeps the status text', () => {
    expect(card).toContain(':class="laneClass(lane)"')
    expect(card).toContain('border-left-color: var(--c, var(--line))')
    expect(card).toContain('<StatusPill')
    // 收集箱卡片没有状态 pill，但也要有状态文字（颜色不是唯一信息）
    expect(page).toContain('data-test="capture-lane"')
    expect(board).toContain('return statusFamily(lane)')
  })

  it('uses the unified button and dropdown primitives instead of native buttons', () => {
    // 卡片：主操作 primary，其余 secondary / ghost，标状态走统一下拉菜单
    expect(card).toContain("import Button from '../ui/Button.vue'")
    expect(card).toContain("import MenuSelect from '../ui/MenuSelect.vue'")
    expect(card).toContain('variant="primary"')
    expect(card).toContain('variant="secondary"')
    expect(card).toContain('variant="ghost"')
    expect(card).toContain('data-test="status-menu"')
    expect(card).not.toContain('status-select')
    expect(card).not.toMatch(/<select[\s>]/)
    // 页面：刷新 / 新建 / 合并建议 / 清除筛选 也走同一个按钮
    expect(page).toContain("import Button from '../ui/Button.vue'")
    expect(page).toContain('data-test="new-toggle"')
    expect(page).not.toMatch(/class="btn/)
  })

  it('leaves room at the bottom so the steward button does not cover the last card', () => {
    // 截图检查 P6-2：右下角浮动的「问管家」压住了最后一张卡的「详情」。
    expect(page).toContain('padding-bottom: 64px')
  })

  it('opens the detail in the P2 drawer, full width on a phone', () => {
    expect(drawer).toContain('width="clamp(640px, 52vw, 1040px)"')
    expect(drawer).toContain('flush')
    // 手机上 Drawer 铺满宽（两级页面的详情层）
    expect(drawer).toContain('@media (max-width: 760px)')
    expect(page).toContain('<WorkDrawer')
  })

  it('keeps every organising action reachable from the drawer', () => {
    for (const hook of [
      'data-test="mark-done"', 'data-test="mark-dropped"', 'data-test="park-btn"', 'data-test="remind-btn"',
      'data-test="ask-report"', 'data-test="ask-handoff"', 'data-test="drawer-summarize"', 'data-test="to-todo"',
      'data-test="merge-btn"', 'data-test="split-btn"', 'data-test="add-link"', 'data-test="todo-dlg"',
      'data-test="delete-item"', 'data-test="hand-back"',
    ]) {
      expect(drawer).toContain(hook)
    }
  })

  it('hands the journal note box to the shared Composer', () => {
    expect(drawer).toContain('<Composer')
    expect(drawer).toContain(':send-label="t(\'work.d.journalWrite\')"')
    expect(drawer).toContain('@send="addNote"')
    expect(drawer).toContain('data-test="note-composer"')
  })
})
