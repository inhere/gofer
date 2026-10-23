// P4 会话页（issue gofer-hez1）：三组会话改成页签、项目 / 状态筛选、统一 Composer 与
// NewSessionSheet、会话抽屉外壳换成 ui/Drawer。这里钉住结构与接线，样式交给截图审阅。
import { describe, expect, it } from 'vitest'

function raw(pattern: Record<string, unknown>): string {
  return Object.values(pattern)[0] as string
}

const page = raw(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = raw(import.meta.glob('../components/SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))
const thread = raw(import.meta.glob('../components/workbench/ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))
const dict = raw(import.meta.glob('../i18n/pages/sessions.ts', { eager: true, query: '?raw', import: 'default' }))

describe('sessions page P4', () => {
  it('turns the three groups into tabs with counts', () => {
    expect(page).toContain('<Tabs :tabs="tabItems"')
    expect(page).toContain("value: 'agent'")
    expect(page).toContain("value: 'acp'")
    expect(page).toContain("value: 'pty'")
    expect(page).toContain('badge: waitingCount.value > 0')
    expect(page).toContain("v-if=\"active === 'agent'\"")
    expect(page).toContain("v-else-if=\"active === 'acp'\"")
  })

  it('filters by project through the URL and by status chips', () => {
    expect(page).toContain("useProjectFilter('/sessions')")
    expect(page).toContain('<ProjectFilter')
    expect(page).toContain('<FilterChips v-model="statusFilter" :items="statusChips"')
    expect(page).toContain('v-for="s in filteredAgent"')
    expect(page).toContain('v-for="item in filteredAcp"')
    expect(page).toContain('v-for="s in filteredPty"')
  })

  it('opens the new-session sheet and keeps the created-session routing', () => {
    expect(page).toContain("import NewSessionSheet from '../ui/NewSessionSheet.vue'")
    expect(page).toContain('<NewSessionSheet v-model="sheetOpen"')
    expect(page).toContain('@created="onSessionCreated"')
    expect(page).toContain('?attach=1')
  })

  it('keeps every agent / ACP / terminal card field and action', () => {
    for (const hook of [
      'data-test="agent-cards"',
      'data-test="acp-cards"',
      'data-test="pty-cards"',
      'data-test="find-box"',
      'data-test="row-open"',
      'data-test="row-wake"',
      'data-test="row-grant"',
      'data-test="refused-owner-transfer"',
      'data-test="acp-link-pick"',
      'data-test="pty-link-pick"',
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
