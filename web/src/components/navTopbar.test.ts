// 顶栏瘦身的接线断言：结构（主导航 6 项 + 「更多」+ 设置、抽屉的完整分组）与信箱按钮的
// 位置、数据源都在这里钉住；纯逻辑在 utils/nav.ts 与 utils/mail.ts 里单测
// （utils/nav.test.ts、utils/mailHeld.test.ts）。
import { describe, expect, it } from 'vitest'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const app = raw(import.meta.glob('../App.vue', { eager: true, query: '?raw', import: 'default' }))
const navMore = raw(import.meta.glob('./NavMore.vue', { eager: true, query: '?raw', import: 'default' }))
const mailBtn = raw(import.meta.glob('./MailButton.vue', { eager: true, query: '?raw', import: 'default' }))
const nav = raw(import.meta.glob('../utils/nav.ts', { eager: true, query: '?raw', import: 'default' }))
// 导航文案的唯一出处（导航、页面标题共用一份）。
const uiText = raw(import.meta.glob('../utils/uiText.ts', { eager: true, query: '?raw', import: 'default' }))

// 顶栏那一行导航（<nav class="nav mono"> … </nav>）的源码片段。
const topbarNav = app.slice(app.indexOf('<nav class="nav mono"'), app.indexOf('</nav>'))

describe('topbar navigation', () => {
  it('renders only the six primary items on the row, from navPrimary', () => {
    expect(app).toContain("import { drawerNavGroups, navPrimary } from './utils/nav'")
    expect(topbarNav).toContain('v-for="item in navPrimary"')
    for (const [key, label] of [
      ['today', '今天'],
      ['workbench', '工作台'],
      ['work', '工作项'],
      ['plans', '计划'],
      ['jobs', '任务'],
      ['sessions', '会话'],
    ]) {
      expect(uiText).toContain(`${key}: '${label}'`)
      expect(nav).toContain(`label: NAV.${key}`)
    }
    // 分组标题只在抽屉里（顶栏一行不再有「工作 / 配置」小标题）。
    expect(topbarNav).not.toContain('class="glabel"')
  })

  it('keeps the 更多 dropdown and 设置 on the row, 更多 before 设置, 信箱 off the row', () => {
    expect(topbarNav).toContain('<NavMore />')
    expect(topbarNav).toContain('class="nav-link nav-settings"')
    expect(topbarNav.indexOf('<NavMore />')).toBeLessThan(topbarNav.indexOf('nav-settings'))
    expect(topbarNav).not.toContain('信箱')
    expect(topbarNav).not.toContain("'/mail'")
  })

  it('gives the narrow-screen drawer the full grouped list', () => {
    expect(app).toContain('v-for="group in drawerNavGroups"')
    expect(app).not.toContain('v-for="group in navGroups"')
  })

  it('drops the navigation horizontal-scroll fallback (the row fits again)', () => {
    expect(app).not.toContain('@media (min-width: 769px) and (max-width: 1100px)')
    const navRule = app.slice(app.indexOf('.nav {'), app.indexOf('.nav-link {'))
    expect(navRule).not.toContain('overflow-x')
    // 900px 以下仍然是抽屉。
    expect(app).toContain('@media (max-width: 900px)')
    expect(app).toContain('.drawer-nav--open')
  })
})

describe('the 更多 dropdown', () => {
  it('is a disclosure: aria-haspopup / aria-expanded and a real button', () => {
    expect(navMore).toContain('aria-haspopup="true"')
    expect(navMore).toContain(':aria-expanded="open"')
    expect(navMore).toContain('type="button"')
    expect(navMore).toContain('data-test="nav-more-btn"')
  })

  it('closes on Escape and on a click outside, and follows the route', () => {
    expect(navMore).toContain("event.key !== 'Escape'")
    expect(navMore).toContain("document.addEventListener('pointerdown', onPointerDown)")
    expect(navMore).toContain('rootRef.value?.contains(target)')
    // 点链接就走（RouterLink）→ 路由一变就收起。
    expect(navMore).toContain('() => route.path')
  })

  it('lists the grouped pages as links and lights up for the current one', () => {
    expect(navMore).toContain('isNavMoreActive(route.path)')
    expect(navMore).toContain('v-for="group in navMoreGroups"')
    // 横向：一个分组一行，组标题在行首，链接横排在 .nav-more-links 里。
    expect(navMore).toContain('class="nav-more-row"')
    expect(navMore).toContain('<p class="nav-more-title">{{ group.label }}</p>')
    expect(navMore).toContain('class="nav-more-links"')
    expect(navMore).toContain(':to="item.to"')
    expect(navMore).toContain('active-class="nav-more-item--active"')
  })

  it('uses the console look (tokens, no hard-coded colours)', () => {
    expect(navMore).toContain('var(--phosphor)')
    expect(navMore).toContain('var(--panel)')
    expect(navMore).not.toMatch(/#[0-9a-fA-F]{3,6}\b/)
  })
})

describe('the mailbox button', () => {
  it('sits next to 随手记 on the right and is an envelope link to /mail', () => {
    expect(app).toContain("import MailButton from './components/MailButton.vue'")
    expect(app).toMatch(/<QuickCapture \/>\s*<MailButton \/>/)
    expect(mailBtn).toContain('<RouterLink')
    expect(mailBtn).toContain(':to="MAIL_PAGE_LINK"')
    expect(mailBtn).toContain('title="信箱"')
  })

  it('badges the held letters and hides the badge at zero', () => {
    expect(mailBtn).toContain('listMail(mailHeldQuery())')
    expect(mailBtn).toContain('v-if="count > 0"')
    expect(mailBtn).toContain('{{ mailBadgeLabel(count) }}')
    expect(mailBtn).toContain('data-test="mail-badge"')
  })

  it('refreshes on a low-frequency poll, with no permission gate', () => {
    expect(mailBtn).toContain('createPoller(refresh, REFRESH_MS)')
    expect(mailBtn).toContain('onMounted(() => poller.start())')
    expect(mailBtn).toContain('onUnmounted(() => poller.stop())')
    // 读信件对所有调用方都允许（含只读 token）：不按权限显隐。
    expect(mailBtn).not.toContain('can(')
  })

  it('uses the console look (tokens, no hard-coded colours)', () => {
    expect(mailBtn).toContain('var(--line)')
    expect(mailBtn).toContain('var(--phosphor)')
    expect(mailBtn).not.toMatch(/#[0-9a-fA-F]{3,6}\b/)
  })
})
