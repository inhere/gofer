import { describe, expect, it } from 'vitest'
import type { MailMessage } from '../api/types'
import {
  buildMailReaddress,
  buildMailReply,
  buildMailSend,
  MAIL_HELD_LINK,
  mailExcerpt,
  mailFilterFromQuery,
  mailListQuery,
  mailCanReaddress,
  mailOutgoing,
  mailPartyLabel,
  mailPeerLabel,
  mailReplyTarget,
  mailStateLabel,
  mailStateTone,
  mailUnread,
  parseMailAddress,
  groupMailThreads,
} from './mail'

function mail(over: Partial<MailMessage> = {}): MailMessage {
  return {
    id: 1,
    thread_id: 'm1',
    from: { kind: 'session', id: 's1', caller: 'c1', display: '会话 s1（claude）' },
    to_address: 'topic:reviews',
    to: { kind: 'session', id: 's2', caller: 'c1' },
    body: '你好',
    state: 'delivered',
    created_at: 1_760_000_000,
    ...over,
  }
}

describe('mail address', () => {
  it('accepts the five shapes and normalizes them', () => {
    expect(parseMailAddress('  session:abc  ')).toEqual({ ok: true, scheme: 'session', value: 'abc', address: 'session:abc', error: '' })
    expect(parseMailAddress('job:j-1').address).toBe('job:j-1')
    expect(parseMailAddress('topic:联调').address).toBe('topic:联调')
    expect(parseMailAddress('path:web/src').address).toBe('path:web/src')
    expect(parseMailAddress('caller').address).toBe('caller')
    // 空输入 = 自己的收集箱，与服务端同义
    expect(parseMailAddress('   ')).toEqual({ ok: true, scheme: 'caller', value: '', address: 'caller', error: '' })
  })

  it('refuses agent: and unknown prefixes with a usable message', () => {
    const agent = parseMailAddress('agent:claude')
    expect(agent.ok).toBe(false)
    expect(agent.address).toBe('')
    expect(agent.error).toContain('agent:')
    expect(agent.error).toContain('session:')
    expect(parseMailAddress('sess:s1').ok).toBe(false)
  })

  it('refuses a missing prefix, an empty target and inner whitespace', () => {
    expect(parseMailAddress('abc').error).toContain('地址要带前缀')
    expect(parseMailAddress('session:').error).toBe('session: 后面要写目标')
    expect(parseMailAddress('topic:a b').error).toBe('地址中间不能有空白')
    expect(parseMailAddress('caller:me').error).toBe('caller 后面不带值')
  })
})

describe('mail compose forms', () => {
  it('builds a trimmed body and omits urgent when unchecked', () => {
    expect(buildMailSend({ to: 'session:s2', body: '  看一下  ', urgent: false })).toEqual({
      req: { to: 'session:s2', body: '看一下' },
      error: '',
    })
    expect(buildMailSend({ to: 'session:s2', body: 'x', urgent: true }).req).toEqual({ to: 'session:s2', body: 'x', urgent: true })
  })

  it('refuses an empty body or a bad address before calling the server', () => {
    expect(buildMailSend({ to: 'session:s2', body: '  ', urgent: false }).req).toBeNull()
    expect(buildMailSend({ to: 'agent:x', body: 'hi', urgent: false }).error).toContain('不支持')
  })

  it('enforces the 32 KB ceiling in bytes', () => {
    const ok = buildMailSend({ to: 'caller', body: '中'.repeat(10_000), urgent: false })
    expect(ok.req).not.toBeNull()
    const big = buildMailSend({ to: 'caller', body: '中'.repeat(11_000), urgent: false })
    expect(big.req).toBeNull()
    expect(big.error).toBe('正文最多 32768 字节')
  })

  it('reply and readdress carry no address / only the address', () => {
    expect(buildMailReply('  好  ', true).req).toEqual({ body: '好', urgent: true })
    expect(buildMailReply(' ', false).req).toBeNull()
    expect(buildMailReaddress(' job:j2 ').req).toEqual({ to: 'job:j2' })
    expect(buildMailReaddress('agent:x').error).not.toBe('')
  })
})

describe('mail state and parties', () => {
  it('labels every state and maps it to a tone', () => {
    expect(['pending', 'delivering', 'delivered', 'answered', 'held', 'expired', 'failed', 'cancelled'].map(mailStateLabel)).toEqual([
      '待送达', '投递中', '已送达', '已答复', '待指派', '已过期', '投递失败', '已撤回',
    ])
    expect(mailStateTone('pending')).toBe('run')
    expect(mailStateTone('answered')).toBe('done')
    expect(mailStateTone('held')).toBe('queue')
    expect(mailStateTone('failed')).toBe('fail')
    expect(mailStateLabel('weird')).toBe('weird')
  })

  it('names a party from the server display or from its kind', () => {
    expect(mailPartyLabel({ kind: 'session', id: 'abcdef0123456789', display: '会话 x' })).toBe('会话 x')
    expect(mailPartyLabel({ kind: 'session', id: 'abcdef0123456789' })).toBe('会话 abcdef012345')
    expect(mailPartyLabel({ kind: 'job', id: 'job-1234567890' })).toBe('job job-12345678')
    expect(mailPartyLabel({ kind: 'caller', caller: 'c1' })).toBe('调用方 c1')
    expect(mailPartyLabel(undefined)).toBe('未知')
  })

  it('decides the direction from the caller, or from the session when given', () => {
    const out = mail({ from: { kind: 'session', id: 's2', caller: 'c1', display: '会话 s2' }, to_address: 'session:s1' })
    expect(mailOutgoing(out, { caller: 'c1' })).toBe(true)
    expect(mailOutgoing(out, { caller: 'other' })).toBe(false)
    expect(mailOutgoing(out, { caller: 'other', session: 's2' })).toBe(true)
    expect(mailPeerLabel(out, { caller: 'c1' })).toBe('session:s1')
    expect(mailPeerLabel(out, { caller: 'other' })).toBe('会话 s2')
  })

  it('unread is about the recipient, not about the letter being old', () => {
    expect(mailUnread(mail({ state: 'delivered' }))).toBe(true)
    expect(mailUnread(mail({ state: 'delivered', read_at: 12 }))).toBe(false)
    expect(mailUnread(mail({ state: 'held' }))).toBe(false)
    expect(mailUnread(mail({ state: 'answered' }))).toBe(false)
  })

  it('collapses the body into one line for the list', () => {
    expect(mailExcerpt('第一行\n\n第二行  ')).toBe('第一行 第二行')
    expect(mailExcerpt('abcdef', 3)).toBe('abc…')
  })
})

describe('mail threads', () => {
  const me = { caller: 'c1' }
  it('groups, orders oldest-first inside a thread and newest activity first between threads', () => {
    const threads = groupMailThreads(
      [
        mail({ id: 1, thread_id: 'm1', created_at: 10, state: 'answered' }),
        mail({ id: 2, thread_id: 'm2', created_at: 50, from: { kind: 'caller', caller: 'other' }, to: { kind: 'session', id: 's1', caller: 'c1' } }),
        mail({ id: 3, thread_id: 'm1', created_at: 30, urgent: true }),
      ],
      me,
    )
    expect(threads.map((t) => t.threadId)).toEqual(['m2', 'm1'])
    expect(threads[1].mails.map((m) => m.id)).toEqual([1, 3])
    expect(threads[1].urgent).toBe(true)
    expect(threads[1].stateLabel).toBe('已送达')
    expect(threads[0].peer).toBe('调用方 other')
  })

  it('counts unread incoming letters per thread', () => {
    const threads = groupMailThreads(
      [
        mail({ id: 1, thread_id: 'm1', read_at: 5 }),
        mail({ id: 2, thread_id: 'm1', from: { kind: 'session', id: 's2', caller: 'other' } }),
      ],
      me,
    )
    expect(threads[0].unread).toBe(1)
  })

  it('falls back to one letter per thread without a thread id, and replies to the newest incoming letter', () => {
    const threads = groupMailThreads([mail({ id: 7, thread_id: '' })], me)
    expect(threads[0].threadId).toBe('m7')
    expect(mailReplyTarget(threads[0], me).id).toBe(7)

    const pair = groupMailThreads(
      [
        mail({ id: 1, thread_id: 'm1', from: { kind: 'session', id: 's9', caller: 'other' } }),
        mail({ id: 2, thread_id: 'm1', from: { kind: 'session', id: 's1', caller: 'c1' }, to_address: 'session:s2' }),
      ],
      me,
    )[0]
    expect(mailReplyTarget(pair, me).id).toBe(1)
    expect(mailCanReaddress(mail({ state: 'held' }))).toBe(true)
    expect(mailCanReaddress(mail({ state: 'pending' }))).toBe(false)
  })
})

describe('mail filters and query', () => {
  it('reads the tab from the query, Today and the session drawer included', () => {
    expect(mailFilterFromQuery({})).toBe('all')
    expect(mailFilterFromQuery({ box: 'out' })).toBe('out')
    expect(mailFilterFromQuery({ state: 'held' })).toBe('held')
    expect(mailFilterFromQuery({ filter: 'in' })).toBe('in')
    expect(mailFilterFromQuery({ session: 's1' })).toBe('all')
    expect(mailFilterFromQuery({ box: 'bogus' })).toBe('all')
  })

  it('maps the tab onto the store query: 待指派 is a state filter over both boxes', () => {
    expect(mailListQuery('in', { limit: 50 })).toEqual({ box: 'in', limit: 50 })
    expect(mailListQuery('out')).toEqual({ box: 'out', limit: 200 })
    expect(mailListQuery('held')).toEqual({ box: 'all', state: ['held'], limit: 200 })
    expect(mailListQuery('all', { session: 's1', thread: 'm1', offset: 200 })).toEqual({
      box: 'all', session: 's1', thread: 'm1', offset: 200, limit: 200,
    })
    expect(MAIL_HELD_LINK).toBe('/mail?state=held')
  })
})
