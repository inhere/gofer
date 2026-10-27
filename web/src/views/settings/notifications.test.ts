// 设置 · 通知（gofer-x9mb）：IM 机器人 + 桌面通知。
//
// 项目里没有 DOM 测试环境（只有 SSR），onMounted 在 SSR 下不跑，页面渲染不出接口数据，
// 所以按仓库既有做法用源码断言把接线钉住：事件目录来自后端、机器人增删改都写回同一份
// webhook 表、测试投递走 POST /v1/notify/test、桌面通知开关按补丁写回，且「配置」页不再
// 编辑同一份 notification。后端契约由 internal/httpapi 的 handler 测试覆盖。
import { describe, expect, it } from 'vitest'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const page = raw(import.meta.glob('./Notifications.vue', { eager: true, query: '?raw', import: 'default' }))
const config = raw(import.meta.glob('../Config.vue', { eager: true, query: '?raw', import: 'default' }))
const api = raw(import.meta.glob('../../api/notify.ts', { eager: true, query: '?raw', import: 'default' }))

describe('notification settings page', () => {
  it('has the three channels in one page', () => {
    expect(page).toContain("t('settings.notify.deviceTitle')")
    expect(page).toContain("t('settings.notify.imTitle')")
    expect(page).toContain("t('settings.notify.desktopTitle')")
    expect(page).toContain('data-testid="bot-add"')
    expect(page).toContain('data-testid="desktop-enabled"')
  })

  it('renders its event pickers from the backend catalog', () => {
    expect(page).toContain('listNotifyEvents()')
    expect(page).toContain('catalog.value?.events')
    // 分组顺序与说明文案都由后端给（zh / en 两份），页面只挑当前语言。
    expect(page).toContain('catalog.value?.groups')
    expect(page).toContain("locale.value.startsWith('zh') ? item.zh : item.en")
    // 新机器人预选默认事件集（webhook 的 default，不是桌面那套）。
    expect(page).toMatch(/defaultEvents\('webhook'\)/)
    expect(page).toMatch(/channel === 'desktop' \? e\.desktop_default : e\.default/)
  })

  it('adds, edits and deletes bots through the config write', () => {
    expect(page).toContain('function openAdd()')
    expect(page).toContain('function openEdit(index: number)')
    expect(page).toContain('function removeBot(index: number)')
    expect(page).toContain('function saveDraft()')
    // 增删改都落到同一张表（整表替换），并保留读不到的 secret_env 名字。
    expect(page).toMatch(/async function saveBots\(\)/)
    expect(page).toContain('putConfigServer({')
    expect(page).toContain('webhooks: bots.value.map((b) => {')
    expect(page).toContain("if (b.secretEnv.trim() !== '') {")
    expect(page).toContain('row.secret_env = b.secretEnv.trim()')
    // 列表显示脱敏 URL，写回用原始 URL。
    expect(page).toContain('b.urlRedacted || b.url')
    expect(page).toContain('url: b.url,')
  })

  it('sends a test message per bot and for the desktop channel', () => {
    expect(page).toMatch(/sendNotifyTest\(target, index\)/)
    expect(page).toContain('data-testid="bot-test"')
    expect(page).toContain('data-testid="desktop-test"')
    // IM 的业务错误原样显示（关键词不匹配之类）。
    expect(page).toContain("t('settings.notify.testFailed', { error: rowResult[i].error ?? '' })")
    // 桌面跳过（没有交互会话 / 没装通知工具）与真的弹出分开说。
    expect(page).toContain("t('settings.notify.testDesktopSkipped', { reason: res.error ?? '' })")
    expect(page).toContain('catalog?.desktop.supported')
  })

  it('writes the desktop channel as a member patch', () => {
    expect(page).toContain('async function saveDesktop()')
    expect(page).toContain('enabled: desktopEnabled.value,')
    expect(page).toContain('events: [...desktopEvents.value],')
    expect(page).toContain('projects: splitList(desktopProjects.value),')
    expect(page).toContain('merge_sec: merge === \'\' ? undefined : Number(merge),')
    // 弹在 server 机器上，而不是当前浏览器 —— 文案必须说清这一点。
    expect(page).toContain("t('settings.notify.desktopMachine')")
  })

  it('calls the documented endpoints', () => {
    expect(api).toContain("request<NotifyEventsResp>('/v1/notify/events')")
    expect(api).toContain("request<NotifyTestResp>('/v1/notify/test'")
    expect(api).toContain("JSON.stringify(target === 'webhook' ? { target, index } : { target })")
  })

  it('leaves the notification config to this page only', () => {
    // 「配置」页的通知表单换成了指路的一行，保存时不再带 notification 成员。
    expect(config).toContain('hintNotificationMoved')
    expect(config).not.toContain('serverForm.notification')
    expect(config).not.toMatch(/notification: \{/)
  })
})
