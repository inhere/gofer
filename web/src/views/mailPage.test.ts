// 信箱页 UI 重设计 P4 的迁移断言：能 SSR 渲染的（页头、筛选 chip、项目 chip、空态、
// 对话行里的徽标、`?project=` 的筛选空态）直接渲染，无法渲染的（点击、Sheet、滚动、
// 请求）用 ?raw 读源码断言接线。纯逻辑（对话分组、分页、小地图、地址解析）另有自己的测试。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import Mail from './Mail.vue'
import { mail as mailDict } from '../i18n/pages/mail'
import { mailConversationInProject } from '../utils/mailConversations'
import type { MailConversation } from '../api/types'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const pageSrc = raw(import.meta.glob('./Mail.vue', { eager: true, query: '?raw', import: 'default' }))
const composerSrc = raw(import.meta.glob('../ui/Composer.vue', { eager: true, query: '?raw', import: 'default' }))

async function render(path = '/mail', query: Record<string, string> = {}): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(Mail as never) })
  app.use(router)
  await router.push({ path, query })
  await router.isReady()
  return renderToString(app)
}

function decode(html: string): string {
  return html
    .replace(/&#39;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&amp;/g, '&')
}

function conv(over: Partial<MailConversation> = {}): MailConversation {
  return {
    key: 'session:sess-a|session:sess-b',
    a: { kind: 'session', id: 'sess-a', persona: { name: 'suga', project_key: 'p-one' } },
    b: { kind: 'session', id: 'sess-b', persona: { name: 'claude', project_key: 'p-two' } },
    last_mail: { id: 7, thread_id: 't1', from: { kind: 'session', id: 'sess-a' }, to: { kind: 'session', id: 'sess-b' }, to_address: 'session:sess-b', body: 'hello', state: 'delivered', created_at: 1_760_000_000 },
    count: 3,
    unread: 1,
    urgent: 1,
    activity_at: 1_760_000_000,
    ...over,
  } as unknown as MailConversation
}

describe('/mail 页头与筛选', () => {
  it('标题、计数、四个筛选 chip 与项目 chip 走本页字典', async () => {
    const html = decode(await render())
    expect(html).toContain(mailDict.zh['mail.h.title'])
    expect(html).toContain(mailDict.zh['mail.h.compose'])
    expect(html).toContain(mailDict.zh['mail.h.refresh'])
    // 四个筛选 chip（全部 / 收件 / 发件 / 待指派）
    for (const key of ['mail.filter.all', 'mail.filter.in', 'mail.filter.out', 'mail.filter.held']) {
      expect(html).toContain(mailDict.zh[key])
    }
    // 项目是筛选条件，不是全局状态（§6.4）：项目 chip 在列表头。
    expect(html).toContain('data-test="project-filter"')
    expect(html).toContain('全部项目')
    // 常驻 agent 挂在左栏（§7.1）。
    expect(html).toContain('data-test="mail-resident-agents"')
  })

  it('空态与「选一个对话」的说明走本页字典', async () => {
    const html = decode(await render())
    expect(html).toContain(mailDict.zh['mail.list.empty'])
    expect(html).toContain(mailDict.zh['mail.thread.select'])
  })

  it('带 ?project= 时空态说明是「当前项目下没有对话」', async () => {
    const html = decode(await render('/mail', { project: 'p-one' }))
    expect(html).toContain(mailDict.zh['mail.list.emptyProject'])
  })

  it('带 ?session= 时显示会话过滤 chip，可以去掉', async () => {
    const html = decode(await render('/mail', { session: 'sess-a' }))
    expect(html).toContain('data-test="mail-session-filter"')
    expect(html).toContain(decode(mailDict.zh['mail.h.session']).replace('{sid}', 'sess-a'))
  })
})

describe('页面接线（源码断言）', () => {
  it('写信放进统一的 Sheet，回复与写信的正文都是 Composer（急件在信箱可用）', () => {
    expect(pageSrc).toContain("import Sheet from '../ui/Sheet.vue'")
    expect(pageSrc).toContain('<Sheet v-model="composeOpen" :label="t(\'mail.compose.title\')"')
    expect(pageSrc.match(/<Composer/g)?.length).toBe(2)
    // 急件开关由调用方声明：回复框与写信框各一处。
    expect(pageSrc.match(/urgent-option/g)?.length).toBe(2)
    expect(pageSrc).toContain('v-model:urgent="reply.urgent"')
    expect(pageSrc).toContain('v-model:urgent="compose.urgent"')
    // 回复框的只读灰显走 Composer 的 canSend + 说明（不再自己画按钮）
    expect(pageSrc).toContain(':can-send="!replyWriteBlock"')
    expect(composerSrc).toContain('data-test="composer-urgent"')
  })

  it('筛选与项目 chip 走 P2 的基元，筛选值仍然写回 URL', () => {
    expect(pageSrc).toContain("import FilterChips from '../ui/FilterChips.vue'")
    expect(pageSrc).toContain("import ProjectFilter from '../ui/ProjectFilter.vue'")
    expect(pageSrc).toContain("useProjectFilter('/mail')")
    expect(pageSrc).toContain("router.replace({ path: '/mail'")
    // 计数口径：conversations 接口的 count 是这一页的条数，所以按返回的对话数数（原先 limit: 1
    // 取 count，四个 chip 全显示 1），项目筛选与页头同一个口径。
    expect(pageSrc).toContain('async function loadCounts()')
    expect(pageSrc).toContain("listMailConversations({ box: 'all', limit: MAIL_COUNT_LIMIT })")
    expect(pageSrc).toContain('mailCountConversations(all.conversations ?? [], project.value)')
    expect(pageSrc).toContain('mailHeldQuery()')
  })

  it('线程顶部是「加载更早的 N 条」，小地图与视口框都在', () => {
    expect(pageSrc).toContain("t('mail.older.load', { n: MAIL_TIMELINE_PAGE })")
    expect(pageSrc).toContain('data-test="mail-older-load"')
    // 「还有更早的信件未加载」与按钮同一行，不叠在信件正文上。
    expect(pageSrc).toContain('data-test="mail-older-hint"')
    expect(pageSrc).not.toContain('minimap-more')
    expect(pageSrc).toContain('<MailMinimap')
    expect(pageSrc).toContain(':band="minimapVisibleBand"')
  })

  it('待指派的信显示改投 / 撤回与候选，广播卡片带统计', () => {
    for (const t of ['data-test="mail-held"', 'data-test="mail-readdress"', 'data-test="mail-cancel"', 'data-test="mail-candidates"', 'data-test="mail-broadcast"']) {
      expect(pageSrc).toContain(t)
    }
    expect(pageSrc).toContain("t('mail.broadcast.head'")
  })

  it('手机（≤760）两级：列表 → 线程用 BackLink 返回', () => {
    expect(pageSrc).toContain('@media (max-width: 760px)')
    expect(pageSrc).toContain('.split--detail .mail-list {')
    expect(pageSrc).toContain('<BackLink')
    expect(pageSrc).toContain(':to="listUrl"')
    expect(pageSrc).toContain('@click="backToList"')
  })
})

describe('对话 id（可复制的固定手柄）', () => {
  it('头部显示对话 id 与复制按钮，每封信都给 thread id 的复制入口', () => {
    expect(pageSrc).toContain("t('mail.thread.convId', { id: activeConvId })")
    expect(pageSrc).toContain('data-test="mail-conv-id-copy"')
    expect(pageSrc).toContain("t('mail.letter.copyThread', { id: m.thread_id })")
    expect(pageSrc).toContain('data-test="mail-thread-copy"')
    // 复制走统一剪贴板 helper（不可用时安静地失败）。
    expect(pageSrc).toContain("import { copyText } from '../utils/sessionMessaging'")
    expect(pageSrc).toContain('function copyConvId')
    expect(pageSrc).toContain('function copyThread')
    // 本地分组的「待指派」对话没有服务端 id：不显示空壳。
    expect(pageSrc).toContain('v-if="activeConvId"')
  })

  it('/mail?conv=<id> 深链按 id 要那一个对话（服务端可见范围内反查）', () => {
    expect(pageSrc).toContain("queryValue(route.query.conv)")
    expect(pageSrc).toContain('async function selectConvFromQuery')
    expect(pageSrc).toContain("listMailConversations({ conv: id, limit: 1 })")
    // 查不到（404）时把服务端的话显示在错误区（load 末尾统一写 error，不被成功清掉）。
    expect(pageSrc).toContain("t('mail.thread.convMissing', { id })")
    expect(pageSrc).toContain('error.value = convErr')
    // 地址栏的 conv 变化也要重新取（外链点两次不同对话）。
    expect(pageSrc).toContain('route.query.session, route.query.conv]')
  })
})

describe('英文界面', () => {
  it('切到 en 后页头、筛选与空态没有残留中文', async () => {
    const { setLocale, locale } = await import('../store/locale')
    const before = locale.value
    try {
      setLocale('en-US')
      const html = decode(await render())
      expect(html).toContain(mailDict.en['mail.h.title'])
      expect(html).toContain(mailDict.en['mail.filter.held'])
      expect(html).toContain(mailDict.en['mail.list.empty'])
      expect(html).not.toContain(mailDict.zh['mail.h.title'])
      expect(html).not.toContain(mailDict.zh['mail.list.empty'])
    } finally {
      setLocale(before)
    }
  })
})

describe('项目筛选（本地按参与方所属项目收窄）', () => {
  it('空项目 = 全部；两端任一属于该项目即算', () => {
    const c = conv()
    expect(mailConversationInProject(c, '')).toBe(true)
    expect(mailConversationInProject(c, 'p-one')).toBe(true)
    expect(mailConversationInProject(c, 'p-two')).toBe(true)
    expect(mailConversationInProject(c, 'p-three')).toBe(false)
    expect(mailConversationInProject(conv({ a: { kind: 'caller', id: 'x' }, b: { kind: 'caller', id: 'y' } }), 'p-one')).toBe(false)
  })
})
