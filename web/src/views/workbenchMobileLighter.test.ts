import { describe, expect, it } from 'vitest'

const workbench = Object.values(import.meta.glob('./Workbench.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const sidebar = Object.values(import.meta.glob('../components/workbench/WorkbenchSidebar.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const drawer = Object.values(import.meta.glob('../components/SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const toast = Object.values(import.meta.glob('../components/InteractionToast.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// 手机版工作台「更轻」的接线断言（纯逻辑在同名的 utils 单测里）。
describe('mobile workbench first paint', () => {
  it('fetches the thread list only once on entry', () => {
    const pollerBlock = workbench.slice(
      workbench.indexOf('const poller = {'),
      workbench.indexOf('function isEmptyLayoutBody'),
    )
    // 首拉在 onMounted 里 await 完（布局初始化要用到 threads），poller.start() 只在首拉失败时补一次。
    expect(pollerBlock).toContain('if (!threadsLoaded) void loadThreads()')
    expect(pollerBlock.match(/loadThreads\(\)/g)).toHaveLength(1)
    expect(workbench.indexOf('await loadThreads()')).toBeGreaterThan(-1)
    expect(workbench.indexOf('await loadThreads()')).toBeLessThan(workbench.indexOf('poller.start()'))
    expect(workbench).toContain('threadsLoaded = true')
  })

  it('asks for summary threads after entry and keeps the open ones complete', () => {
    const load = workbench.slice(workbench.indexOf('async function loadThreads()'), workbench.indexOf('// 选中 / 窗格里换了会话'))
    // 入口那一拉要完整的（还不知道布局里开了哪些会话），之后才用 summary + full。
    expect(load).toContain("detail: summary ? 'summary' : undefined")
    expect(load).toContain('full: summary ? full : undefined')
    expect(load).toContain('const summary = threadsLoaded')
    expect(load).toContain("fullCovered = summary ? new Set(full) : 'all'")
    // 乱序响应：晚到的旧响应不覆盖新的。
    expect(load).toContain('if (seq !== loadSeq) return')
    // 选中 / 窗格里的会话都要完整：聚焦的 + 当前标签页所有窗格。
    expect(workbench).toContain('for (const path of panePaths(root))')
    expect(workbench).toContain('if (selectedID.value) ids.add(selectedID.value)')
  })

  it('refetches once when a thread that only has a summary gets opened', () => {
    const watchBlock = workbench.slice(workbench.indexOf('// 选中 / 窗格里换了会话'), workbench.indexOf('const liveJobs'))
    // 入口那次完整拉取已覆盖所有会话：不补拉；已覆盖的会话（关窗格等）也不补拉。
    expect(watchBlock).toContain("fullCovered === 'all'")
    expect(watchBlock).toContain('every((id) => covered.has(id))')
    // 复用 loadThreads（一次请求），并做防抖。
    expect(watchBlock.match(/loadThreads\(\)/g)).toHaveLength(1)
    expect(watchBlock).toContain('window.setTimeout')
    expect(workbench).toContain('if (fullRefetchTimer != null) window.clearTimeout(fullRefetchTimer)')
  })

  it('does not render the collapsed 已结束 rows at all', () => {
    expect(sidebar).toContain('<template v-if="collapsedEnded.has(endedKey(project.project_key))">')
    expect(sidebar).toContain('v-for="thread in endedThreads(project)"')
    // 曾经是 v-show：折叠时照样建 DOM，上百条历史会话一次进场就全渲染。
    expect(sidebar).not.toContain('v-show="collapsedEnded.has(')
  })
})

describe('mobile session header', () => {
  it('collapses the topic, scope and relay notes behind one summary line', () => {
    expect(drawer).toContain('data-test="session-head-toggle"')
    expect(drawer).toContain('sessionHeaderSummary(topicDisplay(s).text, s.scope ?? [])')
    expect(drawer).toContain('drawer-panel--head-open')
    expect(drawer).toContain('.drawer-panel:not(.drawer-panel--head-open) .head-collapsible,')
    expect(drawer).toContain('.drawer-panel:not(.drawer-panel--head-open) .meta-wrap { display: none; }')
    // 主题 / 范围 / 中继状态说明在折叠体里。
    expect(drawer).toContain('class="topic-wrap head-collapsible"')
    expect(drawer).toContain('class="relay-demoted mono head-collapsible"')
  })

  it('keeps the wake / take-over actions outside the collapsed header', () => {
    // 手机上最常用的一组操作：始终可见，不能被折叠条收进去。
    expect(drawer).toContain('class="wake-bar mono" data-test="wake-bar"')
    expect(drawer).not.toContain('wake-bar mono head-collapsible')
    expect(drawer).toContain('class="takeover-confirm wake-confirm mono" data-test="wake-confirm"')
    expect(drawer).not.toContain('wake-confirm mono head-collapsible')
    // 接管 / 新开 / 唤醒 三个按钮都在常驻的 wake-bar 里。
    expect(drawer).toContain('data-test="wake-btn"')
    expect(drawer).toContain('data-test="from-session-btn"')
  })

  it('hides the composer hints behind a ? toggle and keeps the input usable', () => {
    expect(drawer).toContain('data-test="composer-hint-toggle"')
    expect(drawer).toContain('drawer-panel--hint-open')
    expect(drawer).toContain('.drawer-panel:not(.drawer-panel--hint-open) .hint { display: none; }')
    expect(drawer).toContain('@media (max-width: 900px)')
  })
})

describe('mobile interaction toast', () => {
  it('auto-dismisses after a few seconds and stays one line', () => {
    expect(toast).toContain('isMobileWidth(window.innerWidth)')
    expect(toast).toContain('toastDwellMs(mobile)')
    expect(toast).toContain("emit('close')")
    expect(toast).toMatch(/@media \(max-width: 640px\) \{[\s\S]*\.tx \{[\s\S]*font-size: 11px/)
  })
})
