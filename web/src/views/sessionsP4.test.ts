// P4 会话页（issue gofer-hez1）：三组会话改成页签、项目 / 状态筛选、统一 Composer 与
// NewSessionSheet、会话抽屉外壳换成 ui/Drawer。这里钉住结构与接线，样式交给截图审阅。
import { describe, expect, it } from 'vitest'

function raw(pattern: Record<string, unknown>): string {
  return Object.values(pattern)[0] as string
}

const page = raw(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))
const workbenchPage = raw(import.meta.glob('./Workbench.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = raw(import.meta.glob('../components/SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))
const thread = raw(import.meta.glob('../components/workbench/ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))
const dict = raw(import.meta.glob('../i18n/pages/sessions.ts', { eager: true, query: '?raw', import: 'default' }))

describe('sessions page P4', () => {
  it('turns the three sources into one capped tab set with server counts', () => {
    expect(page).toContain('<Tabs :tabs="tabItems"')
    expect(page).toContain('historyTabs(counts.value')
    expect(page).toContain("acp: t('sessions.tab.acp')")
    expect(page).toContain("pty: t('sessions.tab.pty')")
    expect(page).toContain("relay: t('sessions.tab.relay')")
    // 条数来自接口 counts（与列表同一过滤、不受 source 与分页影响）。
    expect(page).toContain("'': t('sessions.history.all')")
    expect(page).toContain('counts: reset')
  })

  it('filters by project through the URL and by agent / runner / status', () => {
    expect(page).toContain("useProjectFilter('/sessions')")
    expect(page).toContain('<ProjectFilter')
    expect(page).toContain('v-model="agentFilter"')
    expect(page).toContain('v-model="runnerFilter"')
    expect(page).toContain('v-model="statusFilter"')
    expect(page).toContain('v-model="searchInput"')
    expect(page).toContain('v-for="thread in threads"')
  })

  it('leaves session creation to the workbench sidebar (no duplicate entry here)', () => {
    expect(page).not.toContain('NewSessionSheet')
    expect(page).not.toContain('data-test="new-session"')
    expect(workbenchPage).toContain("import NewSessionSheet from '../ui/NewSessionSheet.vue'")
    expect(workbenchPage).toContain('<NewSessionSheet v-model="newSessionOpen"')
    expect(workbenchPage).toContain('@created="onSessionCreated"')
    expect(workbenchPage).toContain('?attach=1')
  })

  it('keeps every history row field and action', () => {
    for (const hook of [
      'data-test="history-cards"',
      'data-test="find-box"',
      'data-test="history-open"',
      'data-test="history-continue"',
      'data-test="history-detail"',
      'data-test="history-link-pick"',
      'data-test="history-work-link"',
      'data-test="history-attach"',
    ]) {
      expect(page + drawer).toContain(hook)
    }
    expect(drawer).toContain('data-test="drawer-send-held"')
  })

  it('keeps the drawer content but swaps the shell for ui/Drawer', () => {
    expect(drawer).toContain("import Drawer from '../ui/Drawer.vue'")
    expect(drawer).toContain('<Drawer v-model="drawerOpen"')
    expect(drawer).toContain('flush')
    expect(drawer).not.toContain('class="drawer-overlay"')
  })

  it('uses the shared Composer in the drawer and the workbench thread', () => {
    expect(drawer).toContain("from '../ui/Composer.vue'")
    expect(drawer).toContain('<Composer')
    expect(thread).toContain("from '../../ui/Composer.vue'")
    expect(thread).toContain('<Composer')
    expect(drawer).toContain('hint-collapsible')
    expect(thread).toContain(":modes=\"['send', 'queue', 'interrupt']\"")
    expect(drawer).toContain('@send="send"')
  })

  it('keeps the delivery mode (auto / hold / force) and send-held action', () => {
    expect(drawer).toContain('INJECT_MODES')
    expect(drawer).toContain('sendHeldNow')
    expect(drawer).toContain('data-test="drawer-send-held"')
  })

  it('keeps page copy in the sessions dictionary', () => {
    for (const key of ['sessions.title', 'sessions.tab.agent', 'sessions.status.all', 'sessions.new.title']) {
      expect(dict).toContain(`'${key}'`)
    }
  })
})
