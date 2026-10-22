// 信箱对话小地图的纯函数：标记位置（等分轨道、封数多时压缩）、颜色语义（急件 > 未读 >
// 发件 > 收件）、悬停文案（发件方 · 时间 + 首行摘要）、可视范围高亮带、点击后的滚动目标。
import { describe, expect, it } from 'vitest'
import type { MailMessage, MailParty } from '../api/types'
import {
  buildMinimapMarks,
  MINIMAP_MIN_COUNT,
  minimapBand,
  minimapMarkTopPct,
  minimapScrollTopForPct,
  minimapTooltip,
  minimapTone,
  minimapVisible,
} from './mailMinimap'

const SESSA: MailParty = { kind: 'session', id: 'sess_aaa', caller: 'c1' }
const SESSB: MailParty = { kind: 'session', id: 'sess_bbb', caller: 'c2' }

function mail(over: Partial<MailMessage> = {}): MailMessage {
  return {
    id: 1,
    thread_id: 't1',
    from: SESSA,
    to_address: 'session:sess_bbb',
    to: SESSB,
    body: '你好',
    state: 'delivered',
    created_at: 1_760_000_000,
    ...over,
  }
}

describe('mark positions', () => {
  it('spreads the marks over the track by grid centres', () => {
    expect(minimapMarkTopPct(0, 4)).toBeCloseTo(12.5)
    expect(minimapMarkTopPct(1, 4)).toBeCloseTo(37.5)
    expect(minimapMarkTopPct(2, 4)).toBeCloseTo(62.5)
    expect(minimapMarkTopPct(3, 4)).toBeCloseTo(87.5)
    // 一封在正中间；越界的下标夹进首尾，不画到轨道外面。
    expect(minimapMarkTopPct(0, 1)).toBeCloseTo(50)
    expect(minimapMarkTopPct(9, 3)).toBeCloseTo(minimapMarkTopPct(2, 3))
    expect(minimapMarkTopPct(-3, 3)).toBeCloseTo(minimapMarkTopPct(0, 3))
    expect(minimapMarkTopPct(0, 0)).toBe(0)
  })

  it('compresses proportionally when the conversation gets long', () => {
    // 位置只跟 index / count 有关：100 封时相邻标记间距是 1%，不会互相挤成一团到处是空隙。
    const gap4 = minimapMarkTopPct(1, 4) - minimapMarkTopPct(0, 4)
    const gap40 = minimapMarkTopPct(1, 40) - minimapMarkTopPct(0, 40)
    expect(gap4).toBeCloseTo(25)
    expect(gap40).toBeCloseTo(2.5)
  })
})

describe('tones', () => {
  it('shows direction, urgency and unread with one colour', () => {
    const me = { caller: 'c1' }
    // 发件（from 是我这一端）；收件（from 是对方）。
    expect(minimapTone(mail({ read_at: 5 }), me)).toBe('out')
    expect(minimapTone(mail({ from: SESSB, to: SESSA, to_address: 'session:sess_aaa', read_at: 5 }), me)).toBe('in')
    // 未读压过方向，急件压过未读。
    expect(minimapTone(mail({ from: SESSB, to: SESSA, to_address: 'session:sess_aaa' }), me)).toBe('unread')
    expect(minimapTone(mail({ from: SESSB, to: SESSA, to_address: 'session:sess_aaa', urgent: true }), me)).toBe('urgent')
    expect(minimapTone(mail({ urgent: true }), me)).toBe('urgent')
  })

  it('does not call cancelled mail unread', () => {
    expect(minimapTone(mail({ from: SESSB, to: SESSA, to_address: 'session:sess_aaa', state: 'cancelled' }), { caller: 'c1' })).toBe('in')
  })
})

describe('tooltip', () => {
  it('names the sender, the time and the first line of the body', () => {
    const m = mail({ from: { ...SESSB, persona: { name: '审查会话' } }, body: '# 标题\n\n正文第二段' })
    const tip = minimapTooltip(m, m.created_at + 60)
    const [head, ...rest] = tip.split('\n')
    expect(head).toBe('审查会话 · 1m前')
    expect(rest.join('\n')).toContain('标题')
    expect(tip).not.toContain('#')
  })

  it('falls back to the short id when the persona is unknown', () => {
    expect(minimapTooltip(mail(), 0).split('\n')[0]).toContain('sess_')
  })
})

describe('buildMinimapMarks', () => {
  it('walks the timeline in order and numbers the letters', () => {
    const mails = [
      mail({ id: 3, created_at: 1_760_000_300, read_at: 1 }),
      mail({ id: 1, created_at: 1_760_000_100, read_at: 1 }),
      mail({ id: 2, created_at: 1_760_000_200, from: SESSB, to: SESSA, to_address: 'session:sess_aaa' }),
    ]
    const marks = buildMinimapMarks(mails, { caller: 'c1' }, 1_760_000_400)
    expect(marks.map((m) => m.id)).toEqual([1, 2, 3])
    expect(marks.map((m) => m.seq)).toEqual([1, 2, 3])
    expect(marks.map((m) => m.tone)).toEqual(['out', 'unread', 'out'])
    expect(marks.map((m) => m.mine)).toEqual([true, false, true])
    expect(marks[1].topPct).toBeCloseTo(minimapMarkTopPct(1, 3))
    expect(marks[0].tooltip).toContain(' · ')
  })

  it('hides the map for a conversation with one or two letters', () => {
    const two = buildMinimapMarks([mail({ id: 1 }), mail({ id: 2 })])
    expect(minimapVisible(two.length)).toBe(false)
    const three = buildMinimapMarks([mail({ id: 1 }), mail({ id: 2 }), mail({ id: 3 })])
    expect(minimapVisible(three.length)).toBe(true)
    expect(MINIMAP_MIN_COUNT).toBe(3)
  })
})

describe('visible band', () => {
  it('marks the whole track when the conversation fits on screen', () => {
    expect(minimapBand(0, 600, 600)).toEqual({ topPct: 0, heightPct: 100 })
    expect(minimapBand(0, 600, 0)).toEqual({ topPct: 0, heightPct: 100 })
  })

  it('tracks the scroll offset and keeps the band inside the track', () => {
    const top = minimapBand(0, 500, 2000)
    expect(top.topPct).toBeCloseTo(0)
    expect(top.heightPct).toBeCloseTo(25)

    const mid = minimapBand(750, 500, 2000)
    expect(mid.topPct).toBeCloseTo(37.5)
    expect(mid.heightPct).toBeCloseTo(25)

    const bottom = minimapBand(1500, 500, 2000)
    expect(bottom.topPct).toBeCloseTo(75)
    expect(bottom.topPct + bottom.heightPct).toBeCloseTo(100)
    // 滚过头 / 负值都夹回两端。
    expect(minimapBand(99999, 500, 2000).topPct).toBeCloseTo(75)
    expect(minimapBand(-50, 500, 2000).topPct).toBeCloseTo(0)
  })
})

describe('click to jump', () => {
  it('centres the letter and clamps at both ends', () => {
    // 轨道 2000，视口 500：中间那封滚到中间。
    expect(minimapScrollTopForPct(50, 500, 2000)).toBeCloseTo(750)
    // 最上面的封不能滚成负值；最下面的封停在底部。
    expect(minimapScrollTopForPct(0, 500, 2000)).toBe(0)
    expect(minimapScrollTopForPct(100, 500, 2000)).toBeCloseTo(1500)
    const lastMark = minimapMarkTopPct(9, 10)
    expect(minimapScrollTopForPct(lastMark, 500, 2000)).toBeCloseTo(1500)
  })

  it('does not scroll when everything is visible', () => {
    expect(minimapScrollTopForPct(50, 600, 600)).toBe(0)
    expect(minimapScrollTopForPct(50, 0, 0)).toBe(0)
  })
})
