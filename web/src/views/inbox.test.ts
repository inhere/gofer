import { beforeEach, describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { InboxItem } from '../api/inbox'
import { dicts, t } from '../i18n'
import { inboxData, inboxError, inboxUnavailable } from '../store/inbox'
import { INBOX_KINDS, normalizeInbox } from '../utils/inbox'
import { SHELL_NAV_GROUPS } from '../utils/shell'
import Inbox from './Inbox.vue'

const raw = (glob: Record<string, unknown>) => Object.values(glob)[0] as string
const inboxSrc = raw(import.meta.glob('./Inbox.vue', { eager: true, query: '?raw', import: 'default' }))
const shellSrc = raw(import.meta.glob('../ui/AppShell.vue', { eager: true, query: '?raw', import: 'default' }))
const appSrc = raw(import.meta.glob('../App.vue', { eager: true, query: '?raw', import: 'default' }))
const routerSrc = raw(import.meta.glob('../router.routes.ts', { eager: true, query: '?raw', import: 'default' }))

function item(key: string, column: 'action' | 'fyi', state: InboxItem['state'], over: Partial<InboxItem> = {}): InboxItem {
  return {
    key,
    column,
    kind: column === 'action' ? 'review' : 'job.terminal',
    title: `title-${key}`,
    summary: `summary-${key}`,
    source_revision: 'r1',
    state,
    actions: [],
    refs: { job_id: key },
    activity_at: 1_760_000_000,
    ...over,
  }
}

// openingTag：取带指定 data-test 的开始标签文本（属性顺序不固定）。
function openingTag(html: string, test: string): string {
  const m = new RegExp(`<[^>]*data-test="${test}"[^>]*>`).exec(html)
  expect(m, `没有找到 ${test}`).not.toBeNull()
  return m![0]
}

async function render(path = '/inbox'): Promise<string> {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  const app = createSSRApp({ render: () => h(Inbox) })
  app.use(router)
  await router.push(path)
  return renderToString(app)
}

beforeEach(() => {
  inboxError.value = ''
  inboxUnavailable.value = false
  inboxData.value = normalizeInbox({
    columns: {
      action: [item('a1', 'action', 'unread'), item('a2', 'action', 'seen'), item('a3', 'action', 'unread', { kind: 'job.failed' })],
      fyi: [item('f1', 'fyi', 'unread'), item('f2', 'fyi', 'seen')],
    },
  })
})

describe('Inbox page', () => {
  it('renders the two columns with their own unread / total counts', async () => {
    const html = await render()
    expect(html).toContain('data-test="inbox-col-action"')
    expect(html).toContain('data-test="inbox-col-fyi"')
    expect(html).toContain(t('inbox.col.action'))
    expect(html).toContain(t('inbox.col.fyi'))
    expect(html).toMatch(new RegExp(`data-test="inbox-count-action"[^>]*>\\s*${t('inbox.col.count', { unread: 2, total: 3 })}`))
    expect(html).toMatch(new RegExp(`data-test="inbox-count-fyi"[^>]*>\\s*${t('inbox.col.count', { unread: 1, total: 2 })}`))
    expect(html.match(/data-test="inbox-item"/g)?.length).toBe(5)
    expect(openingTag(html, 'inbox-mark-all-action')).not.toContain('disabled')
    expect(openingTag(html, 'inbox-clear-read-fyi')).not.toContain('disabled')
  })

  it('puts items in the column the server chose', async () => {
    const html = await render()
    const fyiAt = html.indexOf('data-test="inbox-col-fyi"')
    expect(html.indexOf('title-a1')).toBeLessThan(fyiAt)
    expect(html.indexOf('title-f1')).toBeGreaterThan(fyiAt)
  })

  it('shows the header badge count from the action column only', async () => {
    const html = await render()
    expect(html).toContain(t('inbox.subtitle', { n: 2 }))
  })

  it('shows read / clear buttons per state and a restore button for cleared items', async () => {
    inboxData.value = normalizeInbox({ columns: { action: [item('a1', 'action', 'unread'), item('a2', 'action', 'cleared')], fyi: [] } })
    const html = await render()
    expect(html.match(/data-test="inbox-mark"/g)?.length).toBe(1)
    expect(html.match(/data-test="inbox-clear"/g)?.length).toBe(1)
    expect(html.match(/data-test="inbox-restore"/g)?.length).toBe(1)
  })

  it('disables the bulk buttons when there is nothing to do', async () => {
    inboxData.value = normalizeInbox({ columns: { action: [item('a1', 'action', 'cleared')], fyi: [] } })
    const html = await render()
    expect(openingTag(html, 'inbox-mark-all-action')).toContain('disabled')
    expect(openingTag(html, 'inbox-clear-read-action')).toContain('disabled')
    // fyi 栏是空的，同样不可点
    expect(openingTag(html, 'inbox-mark-all-fyi')).toContain('disabled')
  })

  it('shows per-column empty states', async () => {
    inboxData.value = normalizeInbox({ columns: { action: [], fyi: [item('f1', 'fyi', 'unread')] } })
    const html = await render()
    expect(html).toContain('data-test="inbox-empty-action"')
    expect(html).toContain(t('inbox.empty.action'))
    expect(html).not.toContain('data-test="inbox-empty-fyi"')
  })

  it('renders the kind through StatusPill, with failures in the fail family', async () => {
    const html = await render()
    expect(html).toContain('data-testid="status-pill"')
    expect(html).toContain(t('inbox.kind.job.failed'))
    expect(html).toContain(t('inbox.kind.review'))
    expect(html).toContain(t('inbox.kind.job.terminal'))
  })

  it('links items to their source', async () => {
    const html = await render()
    expect(html).toContain('href="/jobs/a1"')
  })

  it('offers phone tabs for the two columns and defaults to action', async () => {
    const html = await render()
    expect(html).toContain('data-test="inbox-tabs"')
    expect(html).toMatch(/data-value="action"/)
    expect(html).toMatch(/data-value="fyi"/)
    expect(html).toContain('data-active="action"')
    // 非当前栏在手机上隐藏（CSS 类），桌面两栏并排
    expect(html).toMatch(/class="[^"]*inb-col--off[^"]*" data-column="fyi"/)
    expect(html).not.toMatch(/class="[^"]*inb-col--off[^"]*" data-column="action"/)
  })

  it('opens the fyi tab from ?tab=fyi', async () => {
    const html = await render('/inbox?tab=fyi')
    expect(html).toContain('data-active="fyi"')
    expect(html).toMatch(/class="[^"]*inb-col--off[^"]*" data-column="action"/)
  })

  it('shows the unavailable note instead of the error when the inbox is off', async () => {
    inboxUnavailable.value = true
    inboxError.value = 'inbox disabled'
    const html = await render()
    expect(html).toContain('data-test="inbox-unavailable"')
    expect(html).not.toContain('data-test="inbox-error"')
  })

  it('uses the shared primitives and sends the documented requests', () => {
    expect(inboxSrc).toContain("import StatusPill from '../ui/StatusPill.vue'")
    expect(inboxSrc).toContain("import Toast from '../ui/Toast.vue'")
    expect(inboxSrc).toContain("import Switch from '../ui/Switch.vue'")
    expect(inboxSrc).toContain("import Tabs from '../ui/Tabs.vue'")
    // 全部已读按栏批量（带 column）；清理已读只清本栏 seen 的 key
    expect(inboxSrc).toContain("run('seen', { column: col }")
    expect(inboxSrc).toContain('seenKeys(itemsOf(col))')
    expect(inboxSrc).toContain("run('clear', { keys, column: col }")
    expect(inboxSrc).toContain("run('restore', { keys: [it.key] }")
  })
})

describe('inbox wiring', () => {
  it('registers the route, the nav item and the live subscription', () => {
    expect(routerSrc).toContain("path: '/inbox'")
    const all = SHELL_NAV_GROUPS.flatMap((g) => g.items)
    expect(all.find((i) => i.to === '/inbox')?.key).toBe('nav.inbox')
    expect(appSrc).toContain('startInbox()')
    expect(appSrc).toContain('stopInbox()')
  })

  it('puts only the action-column unread on the nav badge', () => {
    expect(shellSrc).toContain("item.to === '/inbox' && inboxBadgeCount > 0")
    expect(shellSrc).toContain('{{ inboxBadgeCount }}')
    expect(shellSrc).not.toContain('unread.fyi')
  })
})

describe('inbox i18n', () => {
  const keys = (loc: 'zh-CN' | 'en-US') => Object.keys(dicts[loc]).filter((k) => k.startsWith('inbox.') || k === 'nav.inbox').sort()

  it('has the same inbox keys in Chinese and English, none empty', () => {
    expect(keys('en-US')).toEqual(keys('zh-CN'))
    expect(keys('zh-CN').length).toBeGreaterThan(30)
    for (const loc of ['zh-CN', 'en-US'] as const) {
      for (const k of keys(loc)) expect(dicts[loc][k].trim(), `${loc} ${k}`).not.toBe('')
    }
  })

  it('covers every known item kind plus the fallback, in both languages', () => {
    for (const loc of ['zh-CN', 'en-US'] as const) {
      for (const kind of [...INBOX_KINDS, 'other']) expect(dicts[loc][`inbox.kind.${kind}`], `${loc} ${kind}`).toBeTruthy()
      for (const col of ['action', 'fyi']) {
        for (const suffix of ['', '.hint']) expect(dicts[loc][`inbox.col.${col}${suffix}`]).toBeTruthy()
        expect(dicts[loc][`inbox.empty.${col}`]).toBeTruthy()
      }
    }
  })
})
