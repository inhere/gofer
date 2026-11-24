import { describe, expect, it } from 'vitest'

// 「待我决策」的接线：「等我」列表一个按钮、一个计数（与收件箱徽标同源）。
const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const needsMe = raw(import.meta.glob('./NeedsMeButton.vue', { eager: true, query: '?raw', import: 'default' }))
const shell = raw(import.meta.glob('./AppShell.vue', { eager: true, query: '?raw', import: 'default' }))
const popCard = raw(import.meta.glob('./WaitingPopCard.vue', { eager: true, query: '?raw', import: 'default' }))

describe('NeedsMeButton (waiting-on-me badge)', () => {
  it('counts the unread of the one waiting list, same as the inbox badge', () => {
    expect(needsMe).toContain('needsMeBadge(inboxData.value?.total_unread, decisionCount.value)')
  })

  it('no longer adds worker requests: they are not one of the six waiting categories', () => {
    expect(needsMe).not.toContain('listEnrollments')
    expect(needsMe).not.toContain('enrollments')
  })

  it('opens the global decision overlay', () => {
    expect(needsMe).toContain('overlayOpen.value = !overlayOpen.value')
    expect(needsMe).toContain(':aria-expanded="overlayOpen"')
  })

  it('keeps the resident top-bar subscriptions and leaves the popping to the waiting pop card', () => {
    expect(needsMe).toContain("useLiveTopic('meta'")
    expect(needsMe).toContain("useLiveTopic('stats'")
    // T5：原来的 pendingAlert 提示条已由顶部弹卡接替（按钮的计数与列表都还在）
    expect(needsMe).not.toContain("from '../utils/pendingAlert'")
    expect(needsMe).not.toContain('<Toast')
    expect(shell).toContain('<WaitingPopCard />')
  })
})

describe('WaitingPopCard (top waiting pop, T5)', () => {
  it('pops one session item at a time, with +N for the rest and the four buttons', () => {
    expect(popCard).toContain('data-test="waiting-pop"')
    expect(popCard).toContain('data-test="waiting-pop-extra"')
    for (const id of ['waiting-pop-reply', 'waiting-pop-approve', 'waiting-pop-open', 'waiting-pop-snooze', 'waiting-pop-close']) {
      expect(popCard).toContain(`data-test="${id}"`)
    }
    // 队列 / 关闭写 seen / 答复批准稍后走今天页的提交路径都在 composable 里
    const pop = raw(import.meta.glob('../composables/useWaitingPop.ts', { eager: true, query: '?raw', import: 'default' }))
    expect(pop).toContain("markInbox('seen', { keys: [key] })")
    expect(pop).toContain('actOnCard')
    expect(pop).toContain('snoozeCard')
    expect(pop).toContain('waiting.onEnter(')
  })
})

describe('AppShell top bar', () => {
  it('has one needs-me button and no duplicate bell or gear menu', () => {
    expect(shell).toContain('<NeedsMeButton />')
    expect(shell).not.toContain('EscalationBell')
    expect(shell).not.toContain('EnrollmentBell')
    expect(shell).not.toContain('TopbarMenu')
    expect(shell).not.toContain('rail-toggle')
  })

  it('exposes the search / command-palette entry and the theme / language switches', () => {
    expect(shell).toContain('data-test="palette-open"')
    expect(shell).toContain('<ThemeToggle />')
    expect(shell).toContain('<LangToggle />')
    expect(shell).toContain('onMetaInvalidated(loadNavHidden)')
    expect(shell).toContain('<CommandPalette v-model:open="paletteOpen" />')
  })

  it('remembers the collapsed sidebar and the node area locally', () => {
    expect(shell).toContain('readShellPrefs()')
    expect(shell).toContain('writeShellPrefs(prefs.value)')
    expect(shell).toContain('data-test="sidebar-toggle"')
    expect(shell).toContain('data-test="nodes-summary"')
  })

  it('counts node online-ness with the shared runner rule (gofer-hez1)', () => {
    // worker 的在线值是 connected；只判 up 会让侧栏的「节点 N/M 在线」把已连接 worker 当离线。
    expect(shell).toContain('online: runnerOnline(r.status)')
    expect(shell).not.toContain("r.status === 'up'")
  })

  it('renders the phone tab bar and the more sheet only at the tab-bar width', () => {
    expect(shell).toContain('v-if="hasTabBar(form)" class="tabs"')
    expect(shell).toContain('data-test="more-sheet"')
    expect(shell).toContain('data-test="tab-more"')
  })
})
