// 信箱「按对话显示、人物呈现」的接线断言（设计 2026-10-20，计划 M2）：纯逻辑在
// utils/mailConversations.test.ts 里单测，这里只钉住页面到 client / 组件的连接——一改就会
// 「页面在，但列表不来自对话接口、正文不渲染、回信跳不过去、人物卡点不开」的地方。
import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(
  [
    '../views/Mail.vue',
    '../components/mail/PersonaChip.vue',
    '../components/MarkdownBlock.vue',
    '../components/SessionDrawer.vue',
    '../api/client.ts',
    '../api/types.ts',
    '../utils/mail.ts',
    '../utils/mailConversations.ts',
    '../i18n/pages/mail.ts',
  ],
  { eager: true, query: '?raw', import: 'default' },
) as Record<string, string>
const src = (n: string): string => {
  const k = Object.keys(mods).find((x) => x.endsWith(n))
  if (!k) throw new Error(`missing ${n}`)
  return mods[k]
}

describe('the left column is a conversation list', () => {
  it('comes from GET /v1/mail/conversations and falls back to local grouping', () => {
    const page = src('Mail.vue')
    expect(page).toContain('listMailConversations(')
    expect(page).toContain('mailConversationBox(')
    // 「待指派」与会话深链没有服务端过滤：按参与方对在本地分组
    expect(page).toContain('groupMailConversations(')
    const c = src('client.ts')
    expect(c).toContain('`/v1/mail/conversations${qs ? `?${qs}` : \'\'}`')
    expect(c).toContain("params.set('peer', opts.peer)")
    expect(c).toContain("params.set('with', opts.with)")
  })

  it('shows both people, the stripped summary, unread, urgent and the activity time', () => {
    const page = src('Mail.vue')
    expect(page).toContain('data-test="mail-conversation"')
    expect(page).toContain('mailSummary(c.last_mail.body, 80)')
    expect(page).toContain('data-test="mail-conv-unread"')
    expect(page).toContain('data-test="mail-conv-urgent"')
    expect(page).toContain('data-test="mail-conv-held"')
    expect(page).toContain('fmtAgo(c.activity_at, nowSec)')
    // 信件数 + 活动时间是一行（文案走本页字典，§9）
    expect(page).toContain("t('mail.conv.meta', { n: c.count, ago: fmtAgo(c.activity_at, nowSec) })")
    // 两个人物卡共用同一个组件
    expect(page.match(/MailPersonaChip/g)?.length ?? 0).toBeGreaterThan(2)
    expect(src('utils/mailConversations.ts')).toContain('export function stripMarkdown(')
  })

  it('keeps the four filter chips and the compose button', () => {
    const page = src('Mail.vue')
    for (const t of ['<FilterChips', ':items="filterItems"', 'data-test="mail-compose-toggle"', 'data-test="mail-to"']) {
      expect(page).toContain(t)
    }
    // 四个筛选（全部 / 收件 / 发件 / 待指派）的 chip 与计数都在页面字典里
    for (const key of ['mail.filter.all', 'mail.filter.in', 'mail.filter.out', 'mail.filter.held']) {
      expect(src('i18n/pages/mail.ts')).toContain(`'${key}'`)
    }
  })
})

describe('the right column is a timeline', () => {
  it('fetches one conversation, oldest first, and renders markdown through the shared block', () => {
    const page = src('Mail.vue')
    expect(page).toContain('mailTimelineQuery(')
    expect(page).toContain('peer: addressable ? q.peer : undefined')
    expect(page).toContain('sortMailTimeline(')
    expect(page).toContain('<MarkdownBlock :text="bodyText(m)" />')
    expect(page).toContain('mailFoldedBody(')
    expect(page).toContain('data-test="mail-letter-fold"')
    // 页面自己不拼 HTML：消毒那条路在 MarkdownBlock（marked + DOMPurify）里
    expect(page).not.toContain('v-html')
    expect(src('MarkdownBlock.vue')).toContain('DOMPurify.sanitize')
  })

  it('splits the bubbles by who is who, labels replies and jumps to the replied letter', () => {
    const page = src('Mail.vue')
    expect(page).toContain('bubbleSide(m, active, me)')
    expect(page).toContain(':data-side="bubbleSide(m, active, me)"')
    expect(page).toContain('`bubble--${bubbleSide(m, active, me)}`')
    expect(page).toContain('data-test="mail-reply-ref"')
    // 跳转抽成了 scrollToLetter：回信角标与小地图上的标记共用（都按信件 id 找那封信）。
    expect(page).toContain('document.getElementById(`mail-${id}`)')
    expect(page).toContain('scrollIntoView(')
    expect(page).toContain('scrollToLetter(m.reply_to)')
    expect(page).toContain('bubble--hot')
    // 投递状态沿用现有的文案与颜色
    expect(page).toContain('mailStateTone(m.state)')
    // 状态文案走本页字典（mail.state.<状态值>）
    expect(page).toContain('stateLabel(m.state)')
  })

  it('keeps reply, readdress, cancel and read-on-open on the page', () => {
    const page = src('Mail.vue')
    for (const t of [
      'replyMail(',
      'readdressMail(',
      'cancelMail(',
      'ackMail(',
      '<Composer',
      'data-test="mail-readdress"',
      'data-test="mail-cancel"',
      'data-test="mail-held"',
      'data-test="mail-held-item"',
      'replyTarget(',
    ]) {
      expect(page).toContain(t)
    }
  })
})

describe('the timeline is paged', () => {
  it('opens on the newest page and walks back with a before cursor', () => {
    const page = src('Mail.vue')
    // 首屏只取最新的一页（utils/mailPaging 里定页大小与合并规则），往上翻用 before=。
    for (const fn of [
      'timelinePage(',
      'timelineFirstPage(',
      'timelineWithOlder(',
      'timelineWithNewest(',
      'oldestMailId(',
      'scrollTopAfterPrepend(',
    ]) {
      expect(page).toContain(fn)
    }
    expect(page).toContain('data-test="mail-older-load"')
    expect(page).toContain('data-test="mail-older-end"')
    expect(page).toContain('data-test="mail-minimap-more"')
    // 同一对话的刷新（ack / 回信 / 新信）只补最新的，不重载已加载的页。
    expect(page).toContain('refreshTimeline(')
    expect(page).toContain('timelineNearBottom(')
    const c = src('client.ts')
    expect(c).toContain("params.set('before', String(opts.before))")
    expect(c).toContain("params.set('order', opts.order)")
  })
})

describe('the persona card', () => {
  it('is one shared component with a stable color, a state dot and a tooltip', () => {
    const chip = src('PersonaChip.vue')
    for (const fn of [
      'personaLabel(',
      'personaColor(',
      'personaSeed(',
      'personaInitial(',
      'personaSubline(',
      'personaStateLabel(',
      'personaStateTone(',
      'personaTooltip(',
    ]) {
      expect(chip).toContain(fn)
    }
    expect(chip).toContain(':title="tip"')
    const util = src('utils/mailConversations.ts')
    expect(util).toContain('export const PERSONA_COLORS')
    // 颜色来自固定色板，不在组件里写死
    expect(chip).not.toContain('#4f7fe0')
  })

  it('opens the session drawer from a session chip', () => {
    expect(src('PersonaChip.vue')).toContain("emit('open-session', sid.value)")
    const page = src('Mail.vue')
    expect(page).toContain('@open-session="openSession"')
    expect(page).toContain('<SessionDrawer v-if="openSid" :sid="openSid"')
  })
})

describe('narrow screens', () => {
  it('switches between the list and the timeline', () => {
    const page = src('Mail.vue')
    expect(page).toContain('@media (max-width: 760px)')
    expect(page).toContain('.split--detail .mail-list {')
    expect(page).toContain('@click="backToList"')
    // 手机上的返回用统一的 BackLink（设计稿《8、§4.3 冻结的手机断点）
    expect(page).toContain('<BackLink')
    expect(page).toContain(':to="listUrl"')
    expect(page).toContain('data-test="mail-back"')
  })
})
