import { describe, expect, it } from 'vitest'

// 「待我决策」合并后的接线（设计稿 §16-4）：决策队列 + worker 申请一个按钮、一个计数。
const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const needsMe = raw(import.meta.glob('./NeedsMeButton.vue', { eager: true, query: '?raw', import: 'default' }))
const shell = raw(import.meta.glob('./AppShell.vue', { eager: true, query: '?raw', import: 'default' }))
const alerts = raw(import.meta.glob('../utils/pendingAlert.ts', { eager: true, query: '?raw', import: 'default' }))

describe('NeedsMeButton (merged decision + worker-request bell)', () => {
  it('merges the decision queue with the pending worker requests', () => {
    expect(needsMe).toContain('needsMeTotal(decisionCount.value, enrollments.value)')
    expect(needsMe).toContain('await listEnrollments()')
    expect(needsMe).toContain('enrollments.value = resp.enabled ? resp.pending_count : 0')
    expect(needsMe).toContain('createLiveTopic(TOPIC_WORKER_ENROLLMENTS')
  })

  it('only subscribes the enrollment topic for worker.manage holders', () => {
    expect(needsMe).toContain("const allowed = computed(() => can('worker.manage'))")
    expect(needsMe).toContain('enrollLive.start()')
    expect(needsMe).toContain('enrollLive.stop()')
  })

  it('opens the global decision overlay', () => {
    expect(needsMe).toContain('overlayOpen.value = !overlayOpen.value')
    expect(needsMe).toContain(':aria-expanded="overlayOpen"')
  })

  it('keeps the resident top-bar subscriptions and the pending alert toast', () => {
    expect(needsMe).toContain("useLiveTopic('meta'")
    expect(needsMe).toContain("useLiveTopic('stats'")
    expect(needsMe).toContain("useLiveTopic('pending'")
    expect(needsMe).toContain('const r = pendingAlert(seen, toast.value, lists)')
    // 弹哪一条是 utils/pendingAlert 的纯逻辑，铃铛只接线
    expect(alerts).toContain('const kept = toast && items.some((it) => it.key === toast.key) ? toast : null')
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
    expect(shell).toContain('<CommandPalette v-model:open="paletteOpen" />')
  })

  it('remembers the collapsed sidebar and the node area locally', () => {
    expect(shell).toContain('readShellPrefs()')
    expect(shell).toContain('writeShellPrefs(prefs.value)')
    expect(shell).toContain('data-test="sidebar-toggle"')
    expect(shell).toContain('data-test="nodes-summary"')
  })

  it('renders the phone tab bar and the more sheet only at the tab-bar width', () => {
    expect(shell).toContain('v-if="hasTabBar(form)" class="tabs"')
    expect(shell).toContain('data-test="more-sheet"')
    expect(shell).toContain('data-test="tab-more"')
  })
})
