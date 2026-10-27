// 设置 · 通知（gofer-x9mb）：IM 机器人 + 桌面通知。
//
// 项目里没有 DOM 测试环境（只有 SSR），onMounted 在 SSR 下不跑，页面渲染不出接口数据，
// 所以按仓库既有做法用源码断言把接线钉住：事件目录来自后端、机器人增删改都写回同一份
// webhook 表、测试投递走 POST /v1/notify/test、桌面通知与机器人同一套「摘要 + 侧栏」编辑、
// 且「配置」页不再编辑同一份 notification。后端契约由 internal/httpapi 的 handler 测试覆盖。
import { describe, expect, it } from 'vitest'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const page = raw(import.meta.glob('./Notifications.vue', { eager: true, query: '?raw', import: 'default' }))
const config = raw(import.meta.glob('../Config.vue', { eager: true, query: '?raw', import: 'default' }))
const api = raw(import.meta.glob('../../api/notify.ts', { eager: true, query: '?raw', import: 'default' }))
const picker = raw(
  import.meta.glob('../../components/NotifyEventsPicker.vue', { eager: true, query: '?raw', import: 'default' }),
)
const projects = raw(
  import.meta.glob('../../components/NotifyProjectsField.vue', { eager: true, query: '?raw', import: 'default' }),
)

describe('notification settings page', () => {
  it('has the three channels in one page', () => {
    expect(page).toContain("t('settings.notify.deviceTitle')")
    expect(page).toContain("t('settings.notify.imTitle')")
    expect(page).toContain("t('settings.notify.desktopTitle')")
    expect(page).toContain('data-testid="bot-add"')
    expect(page).toContain('data-testid="desktop-state"')
  })

  it('renders its event pickers from the backend catalog', () => {
    expect(page).toContain('listNotifyEvents()')
    expect(page).toContain('catalog.value?.events')
    // 分组顺序与说明文案都由后端给（zh / en 两份），页面只挑当前语言；这些都在共用组件里。
    expect(picker).toContain('props.catalog?.groups')
    expect(picker).toContain("locale.value.startsWith('zh') ? item.zh : item.en")
    expect(picker).toContain("t('settings.notify.eventsHint')")
    // 新机器人预选默认事件集（webhook 的 default）；桌面频道留空同样表示默认事件集。
    expect(page).toMatch(/defaultEvents\(\)/)
    expect(page).toContain('.filter((e) => e.default)')
  })

  it('shares one event picker and one projects field between both drawers', () => {
    // 抽出共用组件，不复制第二份（两个侧栏各用一次）。
    expect(page).toContain("import NotifyEventsPicker from '../../components/NotifyEventsPicker.vue'")
    expect(page).toContain("import NotifyProjectsField from '../../components/NotifyProjectsField.vue'")
    expect(page.match(/<NotifyEventsPicker/g)).toHaveLength(2)
    expect(page.match(/<NotifyProjectsField/g)).toHaveLength(2)
    // 勾选整份替换（父组件 v-model 收到新列表），而不是在侧栏里各写一份 toggle。
    expect(picker).toContain('events.value = next')
    expect(projects).toContain("t('settings.notify.projectsLabel')")
    expect(projects).toContain("t('settings.notify.projectsPlaceholder')")
    expect(page).not.toContain('function toggleEvent(')
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

  it('edits the desktop channel in a drawer, the page keeps only a summary', () => {
    // 页面只留摘要：开关状态、已选事件数、项目、合并窗口。
    expect(page).toContain('data-testid="desktop-summary"')
    expect(page).toContain('data-testid="desktop-events"')
    expect(page).toContain('data-testid="desktop-projects"')
    expect(page).toContain('data-testid="desktop-merge"')
    expect(page).toContain("t('settings.notify.desktopEventsCount', { n: desktopEvents.value.length })")
    expect(page).toContain("t('settings.notify.desktopAllProjects')")
    // 点「编辑」打开侧栏：草稿从已保存的值复制（摘要永远是已保存的值）。
    expect(page).toContain('data-testid="desktop-edit"')
    expect(page).toMatch(/function openDesktop\(\)/)
    expect(page).toContain('enabled: desktopEnabled.value,')
    expect(page).toContain('events: [...desktopEvents.value],')
    expect(page).toContain('projects: desktopProjects.value,')
    expect(page).toContain('mergeSec: desktopMergeSec.value,')
    expect(page).toContain('desktopOpen.value = true')
    // 两个侧栏同一宽度（随屏幕变化，手机由 Drawer 的媒体查询铺满）。
    expect(page.match(/width="clamp\(560px, 46vw, 900px\)"/g)).toHaveLength(2)
    expect(page).toMatch(/<Drawer v-model="desktopOpen"[^>]*width="clamp\(560px, 46vw, 900px\)"/)
    // 取消只关侧栏，不落盘。
    expect(page).toMatch(/data-testid="desktop-cancel"[^>]*@click="desktopOpen = false"/)
  })

  it('writes the desktop channel as a member patch', () => {
    expect(page).toContain('async function saveDesktop()')
    expect(page).toContain('enabled: desktopDraft.enabled,')
    expect(page).toContain('events: [...desktopDraft.events],')
    expect(page).toContain('projects: splitList(desktopDraft.projects),')
    expect(page).toContain('merge_sec: merge === \'\' ? undefined : Number(merge),')
    // 保存成功后关侧栏并重读配置（摘要跟着变）。
    expect(page).toMatch(/notice\.value = t\('settings\.notify\.saved'\)\s*desktopOpen\.value = false\s*await loadNotify\(\)/)
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
