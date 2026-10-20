// 信箱「按对话显示」的纯函数（设计 2026-10-20，计划 M2）：对话 key 与分组、气泡方向、
// 人物卡取色 / 名字回退 / 状态、摘要去 markdown、回信标号与折叠。
import { describe, expect, it } from 'vitest'
import type { MailConversation, MailMessage, MailParty } from '../api/types'
import {
  bubbleSide,
  conversationKey,
  groupMailConversations,
  mailAddressFetchable,
  mailAddressIsMine,
  mailConversationBox,
  mailConversationKey,
  mailConversationSeq,
  mailFoldedBody,
  mailLongBody,
  mailPartyAddress,
  mailReplyLabel,
  mailSummary,
  mailTimelineAddressable,
  mailTimelineQuery,
  mailUnread,
  MAIL_LONG_BODY,
  PERSONA_COLORS,
  personaColor,
  personaInitial,
  personaLabel,
  personaSeed,
  personaState,
  personaStateLabel,
  personaStateTone,
  personaSubline,
  personaTooltip,
  replyTarget,
  sortMailTimeline,
  stripMarkdown,
} from './mailConversations'

const S1: MailParty = { kind: 'session', id: 'sess_aaa', caller: 'c1' }
const S2: MailParty = { kind: 'session', id: 'sess_bbb', caller: 'c1' }
const BOX: MailParty = { kind: 'caller', caller: 'c1' }

function mail(over: Partial<MailMessage> = {}): MailMessage {
  return {
    id: 1,
    thread_id: 't1',
    from: S1,
    to_address: 'session:sess_bbb',
    to: S2,
    body: '你好',
    state: 'delivered',
    created_at: 1_760_000_000,
    ...over,
  }
}

describe('conversation key', () => {
  it('is direction free and matches the server key shape', () => {
    expect(conversationKey('session:a', 'caller:c1')).toBe('caller:c1|session:a')
    expect(conversationKey('caller:c1', 'session:a')).toBe(conversationKey('session:a', 'caller:c1'))
    expect(conversationKey('session:a', 'session:a')).toBe('session:a|session:a')
  })

  it('names each end by its canonical address', () => {
    expect(mailPartyAddress(S1)).toBe('session:sess_aaa')
    expect(mailPartyAddress({ kind: 'job', id: 'job-1' })).toBe('job:job-1')
    expect(mailPartyAddress(BOX)).toBe('caller:c1')
    expect(mailPartyAddress({ kind: 'caller' })).toBe('caller')
    // 待指派信没解析到人：服务端把原文地址放在 display 里
    expect(mailPartyAddress({ kind: '', display: 'topic:reviews' })).toBe('topic:reviews')
    expect(mailPartyAddress(undefined)).toBe('')
  })

  it('keys a letter by its two ends whichever way it went', () => {
    const out = mail()
    const back = mail({ id: 2, from: S2, to: S1 })
    expect(mailConversationKey(out)).toBe(mailConversationKey(back))
    expect(mailConversationKey(out)).toBe('session:sess_aaa|session:sess_bbb')
  })

  it('only session / job / caller addresses can be fetched with peer=&with=', () => {
    expect(mailAddressFetchable('session:sess_aaa')).toBe(true)
    expect(mailAddressFetchable('job:job-1')).toBe(true)
    expect(mailAddressFetchable('caller:c1')).toBe(true)
    expect(mailAddressFetchable('topic:reviews')).toBe(false)
    expect(mailAddressFetchable('path:web/src')).toBe(false)
  })
})

describe('grouping into conversations', () => {
  it('merges several threads and both directions into one conversation', () => {
    const rows = groupMailConversations([
      // 两个 thread、两个方向：一个对话
      mail({ id: 1, thread_id: 't1', created_at: 100 }),
      mail({ id: 2, thread_id: 't1', created_at: 200, from: S2, to: S1, to_address: 'session:sess_aaa' }),
      mail({ id: 3, thread_id: 't9', created_at: 300 }),
      // 另一对
      mail({ id: 4, created_at: 400, from: S2, to: BOX, to_address: 'caller', thread_id: 't2' }),
    ])
    expect(rows).toHaveLength(2)
    const pair = rows.find((r) => r.key === 'session:sess_aaa|session:sess_bbb')
    expect(pair?.count).toBe(3)
    // a 是对话里最先发信的一方（决定两边都不是我时谁在左）
    expect(mailPartyAddress(pair?.a)).toBe('session:sess_aaa')
    expect(mailPartyAddress(pair?.b)).toBe('session:sess_bbb')
    expect(pair?.last_mail.id).toBe(3)
    expect(pair?.activity_at).toBe(300)
    // 最新的对话在前
    expect(rows[0].key).toBe('caller:c1|session:sess_bbb')
  })

  it('counts unread and urgent letters, oldest first inside the conversation', () => {
    const rows = groupMailConversations([
      mail({ id: 5, created_at: 500, urgent: true }),
      mail({ id: 6, created_at: 600, read_at: 700 }),
      mail({ id: 7, created_at: 700, state: 'answered' }),
      mail({ id: 8, created_at: 800, state: 'cancelled' }),
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0].unread).toBe(1)
    expect(rows[0].urgent).toBe(1)
    expect(rows[0].count).toBe(4)
    const keys = sortMailTimeline([
      mail({ id: 7, created_at: 700 }),
      mail({ id: 5, created_at: 500 }),
      mail({ id: 6, created_at: 500 }),
    ]).map((m) => m.id)
    expect(keys).toEqual([5, 6, 7])
  })

  it('reads unread the way the server does', () => {
    expect(mailUnread(mail({ state: 'delivered' }))).toBe(true)
    expect(mailUnread(mail({ state: 'pending' }))).toBe(true)
    expect(mailUnread(mail({ state: 'delivered', read_at: 1 }))).toBe(false)
    expect(mailUnread(mail({ state: 'answered' }))).toBe(false)
    expect(mailUnread(mail({ state: 'failed' }))).toBe(false)
    expect(mailUnread(mail({ state: 'expired' }))).toBe(false)
    expect(mailUnread(mail({ state: 'cancelled' }))).toBe(false)
  })
})

describe('timeline query', () => {
  const conv: MailConversation = {
    key: 'session:sess_aaa|session:sess_bbb',
    a: S1,
    b: S2,
    last_mail: mail(),
    count: 1,
    unread: 1,
    urgent: 0,
    activity_at: 1,
  }

  it('maps the tab to the box and adds state=held for the held tab', () => {
    expect(mailConversationBox('all')).toBe('all')
    expect(mailConversationBox('in')).toBe('in')
    expect(mailConversationBox('out')).toBe('out')
    expect(mailConversationBox('held')).toBe('all')
    expect(mailTimelineQuery(conv, 'all')).toEqual({
      peer: 'session:sess_aaa',
      with: 'session:sess_bbb',
      box: 'all',
      key: conv.key,
    })
    expect(mailTimelineQuery(conv, 'held').state).toEqual(['held'])
    expect(mailTimelineAddressable(mailTimelineQuery(conv, 'all'))).toBe(true)
  })

  it('falls back to local filtering for an address that resolved to nobody', () => {
    const held: MailConversation = {
      ...conv,
      key: 'session:sess_aaa|topic:reviews',
      b: { kind: '', display: 'topic:reviews' },
    }
    expect(mailTimelineAddressable(mailTimelineQuery(held, 'held'))).toBe(false)
  })
})

describe('bubble side (decision 4)', () => {
  const conv: MailConversation = {
    key: 'session:mine|session:other',
    a: { kind: 'session', id: 'other' },
    b: { kind: 'session', id: 'mine' },
    last_mail: mail(),
    count: 1,
    unread: 0,
    urgent: 0,
    activity_at: 1,
  }
  const me = { caller: 'c1', session: 'mine' }

  it('puts my own side on the right, whichever end it is', () => {
    expect(mailAddressIsMine('session:mine', me)).toBe(true)
    expect(mailAddressIsMine('caller:c1', me)).toBe(true)
    expect(mailAddressIsMine('session:other', me)).toBe(false)
    // 我的会话发的信
    expect(bubbleSide(mail({ from: { kind: 'session', id: 'mine' } }), conv, me)).toBe('right')
    // 别人发给我的会话：收信在左
    expect(
      bubbleSide(mail({ from: { kind: 'session', id: 'other' }, to: { kind: 'session', id: 'mine' } }), conv, me),
    ).toBe('left')
    // 别人的会话之间：先发信的一方在左
    expect(bubbleSide(mail({ from: conv.a }), conv, { caller: 'c9' })).toBe('left')
    expect(bubbleSide(mail({ from: conv.b }), conv, { caller: 'c9' })).toBe('right')
    // 我的收集箱发的信也在右
    expect(bubbleSide(mail({ from: { kind: 'caller', caller: 'c1' } }), conv, me)).toBe('right')
  })
})

describe('persona card', () => {
  const withPersona = (over: Partial<MailParty> = {}): MailParty => ({
    kind: 'session',
    id: 'sess_aaa',
    persona: {
      name: '修登录',
      agent: 'claude',
      project_key: 'app',
      runner: 'box-1',
      state: 'running',
      last_seen_at: 1_760_000_000,
      cwd: '/work/app',
    },
    ...over,
  })

  it('colors stably by id, from one fixed palette', () => {
    const a = personaColor(personaSeed(withPersona()))
    expect(PERSONA_COLORS).toContain(a)
    expect(personaColor(personaSeed(withPersona()))).toBe(a)
    // 另一张卡（另一个 id）也是色板里的一色
    expect(PERSONA_COLORS).toContain(personaColor(personaSeed({ kind: 'session', id: 'sess_zzz' })))
    // 空种子也有颜色（不会 undefined）
    expect(PERSONA_COLORS).toContain(personaColor(''))
  })

  it('takes the name from the persona and falls back to the short id', () => {
    expect(personaLabel(withPersona())).toBe('修登录')
    expect(personaLabel({ kind: 'session', id: 'abcdef0123456789' })).toBe('会话 abcdef012345')
    expect(personaInitial('修登录')).toBe('修')
    expect(personaInitial('')).toBe('?')
  })

  it('shows agent · project · runner as the subline', () => {
    expect(personaSubline(withPersona())).toBe('claude · app · box-1')
    expect(personaSubline({ kind: 'session', id: 's', persona: { name: 'x', agent: 'codex' } })).toBe('codex')
    expect(personaSubline({ kind: 'caller', caller: 'c1' })).toBe('')
  })

  it('maps the state to a label and a page tone', () => {
    expect(personaState(withPersona())).toBe('running')
    expect(personaStateLabel('running')).toBe('在线')
    expect(personaStateLabel('idle')).toBe('空闲')
    expect(personaStateLabel('awaiting_input')).toBe('等回复')
    expect(personaStateLabel('offline')).toBe('离线')
    expect(personaStateLabel('ended')).toBe('已结束')
    expect(personaStateTone('running')).toBe('run')
    expect(personaStateTone('idle')).toBe('done')
    expect(personaStateTone('failed')).toBe('fail')
    expect(personaStateTone('awaiting_input')).toBe('queue')
    // 没见过的新状态照原样显示，不用「—」盖掉
    expect(personaStateLabel('brand_new')).toBe('brand_new')
  })

  it('puts the full id, cwd and last activity in the tooltip', () => {
    const tip = personaTooltip(withPersona(), 1_760_000_120)
    expect(tip.split('\n')[0]).toBe('session:sess_aaa')
    expect(tip).toContain('/work/app')
    expect(tip).toContain('最后活动')
    // 读不到人（没有 persona）时只有地址
    expect(personaTooltip({ kind: 'job', id: 'job-1' })).toBe('job:job-1')
  })
})

describe('markdown summary', () => {
  it('strips the markdown a letter is written in', () => {
    expect(stripMarkdown('# 标题\n\n正文**加粗**与`代码`')).toBe('标题 正文加粗与代码')
    expect(stripMarkdown('- 一\n- 二\n\n1. 三')).toBe('一 二 三')
    expect(stripMarkdown('> 引用 [链接](https://x.test) ![图](a.png)')).toBe('引用 链接 图')
    expect(stripMarkdown('```go\nfn()\n```\n说完')).toBe('说完')
    expect(stripMarkdown('<b>x</b> ~~旧~~ __重__')).toBe('x 旧 重')
    expect(stripMarkdown('a\n\n\nb')).toBe('a b')
  })

  it('keeps the summary on one line and truncates it', () => {
    expect(mailSummary('## 结论\n\n**好了**', 80)).toBe('结论 好了')
    expect(mailSummary('abcdef', 3)).toBe('abc…')
  })
})

describe('reply badge and folding', () => {
  const rows = [mail({ id: 11, created_at: 100 }), mail({ id: 12, created_at: 200, reply_to: 11, from: S2, to: S1 })]

  it('numbers the letters and labels a reply with the replied position', () => {
    const seq = mailConversationSeq(rows)
    expect(seq.get(11)).toBe(1)
    expect(seq.get(12)).toBe(2)
    expect(mailReplyLabel(rows[0], seq)).toBe('')
    expect(mailReplyLabel(rows[1], seq)).toBe('回复 #1')
    // 被回复的那封不在这一页里：只标「回复」，不瞎编行号
    expect(mailReplyLabel(mail({ id: 13, reply_to: 99 }), seq)).toBe('回复')
  })

  it('replies to the newest incoming letter', () => {
    expect(replyTarget(rows, { caller: 'c1', session: 'sess_aaa' })?.id).toBe(12)
    expect(replyTarget(rows, { caller: 'c9' })?.id).toBe(12)
    expect(replyTarget([], {}) ?? null).toBeNull()
  })

  it('folds only a long body', () => {
    const long = 'x'.repeat(MAIL_LONG_BODY + 1)
    expect(mailLongBody('短')).toBe(false)
    expect(mailLongBody(long)).toBe(true)
    expect(mailFoldedBody(long).endsWith('…')).toBe(true)
    expect([...mailFoldedBody(long)].length).toBe(MAIL_LONG_BODY + 1)
    expect(mailFoldedBody('短')).toBe('短')
  })
})
