import { describe, expect, it } from 'vitest'
import {
  buildMailSend,
  emptyMailCompose,
  filterComposeSessions,
  mailAddressLabel,
  mailComposeAddresses,
  mailComposePreviewText,
  mailRecipientPreview,
  parseMailRecipients,
} from './mail'

// 群发（设计 2026-10-22「D1」/「D5」）：收件人可以是多个地址或 each: 选择器，写信框
// 发送前能说出大约投给几个会话。
describe('parseMailRecipients', () => {
  it('一个普通地址保持字符串写法', () => {
    const r = parseMailRecipients('session:sess-a')
    expect(r.ok).toBe(true)
    expect(r.addresses).toEqual(['session:sess-a'])
    expect(buildMailSend({ to: 'session:sess-a', body: 'hi', urgent: false }).req).toEqual({
      to: 'session:sess-a', body: 'hi',
    })
  })

  it('空收件人是自己的收集箱', () => {
    expect(parseMailRecipients('   ').addresses).toEqual(['caller'])
  })

  it('多个地址（空格 / 逗号分隔）是群发数组', () => {
    const r = parseMailRecipients('session:sess-a, topic:login  session:sess-b')
    expect(r.ok).toBe(true)
    expect(r.addresses).toEqual(['session:sess-a', 'topic:login', 'session:sess-b'])
    expect(buildMailSend({ to: 'session:sess-a topic:login', body: 'hi', urgent: false }).req?.to).toEqual([
      'session:sess-a', 'topic:login',
    ])
  })

  it('each: 选择器单独一封也发成数组', () => {
    const r = parseMailRecipients('each:project:gofer')
    expect(r.ok).toBe(true)
    expect(r.addresses).toEqual(['each:project:gofer'])
    expect(buildMailSend({ to: 'each:project:gofer', body: 'hi', urgent: false }).req?.to).toEqual(['each:project:gofer'])
  })

  it('each: 只接受 topic / path / project / agent', () => {
    for (const bad of ['each:session:sess-a', 'each:job:j1', 'each:caller', 'each:project:']) {
      const r = parseMailRecipients(bad)
      expect(r.ok, bad).toBe(false)
      expect(r.error).toContain('each:')
    }
  })

  it('普通地址的校验照旧', () => {
    expect(parseMailRecipients('sess:s1').ok).toBe(false)
    expect(parseMailRecipients('agent:claude').ok).toBe(false)
  })
})

describe('mailRecipientPreview', () => {
  const sessions = [
    { session_id: 'sess-a', agent: 'claude', project_key: 'gofer', topic: 'login 页面' },
    { session_id: 'sess-b', agent: 'suga', project_key: 'gofer', topic: '' },
    { session_id: 'sess-c', agent: 'claude', project_key: 'suga', topic: 'login 修复' },
  ]

  it('显式地址按个数算', () => {
    expect(mailRecipientPreview('session:sess-a session:sess-b', sessions)).toBe(2)
  })

  it('each:project: / each:agent: / each:topic: 按会话匹配', () => {
    expect(mailRecipientPreview('each:project:gofer', sessions)).toBe(2)
    expect(mailRecipientPreview('each:agent:claude', sessions)).toBe(2)
    expect(mailRecipientPreview('each:topic:login', sessions)).toBe(2)
  })

  it('path: 选择器按服务端匹配，预览报 -1', () => {
    expect(mailRecipientPreview('each:path:web/src', sessions)).toBe(-1)
  })

  it('同一个会话被两个地址选中只算一次', () => {
    expect(mailRecipientPreview('session:sess-a each:project:gofer', sessions)).toBe(2)
  })
})

// 写信框的收件人（gofer-ndy5）：会话多选 + 「按项目 / 主题 / agent 全部」选器 + 手写地址。
describe('mailComposeAddresses', () => {
  it('会话多选生成 session: 地址', () => {
    const st = { ...emptyMailCompose(), sessions: ['sess-a', 'sess-b'] }
    expect(mailComposeAddresses(st).addresses).toEqual(['session:sess-a', 'session:sess-b'])
  })

  it('选器生成对应的 each: 地址', () => {
    const st = { ...emptyMailCompose(), scopes: [
      { scheme: 'project' as const, value: 'gofer' },
      { scheme: 'agent' as const, value: 'suga' },
      { scheme: 'topic' as const, value: 'login' },
      { scheme: 'path' as const, value: 'web/src' },
    ] }
    expect(mailComposeAddresses(st).addresses).toEqual([
      'each:project:gofer', 'each:agent:suga', 'each:topic:login', 'each:path:web/src',
    ])
  })

  it('手写地址（高级）与选择合并、按值去重', () => {
    const st = {
      ...emptyMailCompose(),
      sessions: ['sess-a'],
      scopes: [{ scheme: 'project' as const, value: 'gofer' }],
      manual: 'session:sess-a caller',
    }
    expect(mailComposeAddresses(st).addresses).toEqual(['session:sess-a', 'each:project:gofer', 'caller'])
  })

  it('什么都没选 = 自己的收集箱', () => {
    expect(mailComposeAddresses(emptyMailCompose()).addresses).toEqual(['caller'])
    expect(mailComposeAddresses({ ...emptyMailCompose(), sessions: ['  '] }).addresses).toEqual(['caller'])
  })

  it('选器缺值 / 带空白报错', () => {
    for (const value of ['', 'a b']) {
      const r = mailComposeAddresses({ ...emptyMailCompose(), scopes: [{ scheme: 'project', value }] })
      expect(r.ok, value).toBe(false)
      expect(r.error).toContain('选器')
    }
  })

  it('手写部分的形状校验照旧（非法前缀整份报错）', () => {
    const r = mailComposeAddresses({ ...emptyMailCompose(), manual: 'sess:x' })
    expect(r.ok).toBe(false)
    expect(r.error).toContain('前缀')
  })

  it('地址数组直接当邮件请求体（群发）', () => {
    const st = { ...emptyMailCompose(), sessions: ['sess-a'], scopes: [{ scheme: 'project' as const, value: 'gofer' }] }
    expect(buildMailSend({ to: mailComposeAddresses(st).addresses, body: 'hi', urgent: false }).req).toEqual({
      to: ['session:sess-a', 'each:project:gofer'], body: 'hi',
    })
  })

  it('空地址数组拒绝发送（不会发成空收件人）', () => {
    expect(buildMailSend({ to: [], body: 'hi', urgent: false })).toEqual({ req: null, error: '没有收件人' })
  })
})

describe('mailComposePreviewText', () => {
  it('没选收件人时说投给自己', () => {
    expect(mailComposePreviewText(['caller'], null, 0)).toContain('收集箱')
  })

  it('服务端 preview 给出「将投递 M 个会话（略过 K 个）」', () => {
    expect(mailComposePreviewText(['each:project:gofer'], { delivered: 3, skipped: 2, matched: 5 }, 3))
      .toBe('将投递 3 个会话（略过 2 个）')
    expect(mailComposePreviewText(['each:project:gofer'], { delivered: 3, skipped: 0, matched: 3 }, 3))
      .toBe('将投递 3 个会话')
  })

  it('还没有服务端回答时用本地估算', () => {
    expect(mailComposePreviewText(['session:sess-a'], null, 2)).toBe('约 2 个会话（服务端确认中…）')
    expect(mailComposePreviewText(['each:path:web'], null, -1)).toContain('服务端')
  })
})

describe('filterComposeSessions / mailAddressLabel', () => {
  const options = [
    { session_id: 'sess-a1', agent: 'claude', project_key: 'gofer', topic: 'login', title: '登录页重构' },
    { session_id: 'sess-b1', agent: 'suga', project_key: 'other', topic: '', title: '发布检查' },
  ]

  it('按 id / 标题 / agent / 项目搜索', () => {
    expect(filterComposeSessions(options, '').length).toBe(2)
    expect(filterComposeSessions(options, '登录').map((s) => s.session_id)).toEqual(['sess-a1'])
    expect(filterComposeSessions(options, 'SUGA').map((s) => s.session_id)).toEqual(['sess-b1'])
    expect(filterComposeSessions(options, 'gofer').map((s) => s.session_id)).toEqual(['sess-a1'])
    expect(filterComposeSessions(options, 'sess-b1').map((s) => s.session_id)).toEqual(['sess-b1'])
    expect(filterComposeSessions(options, 'zzz')).toEqual([])
  })

  it('标签文案', () => {
    expect(mailAddressLabel('session:sess-a')).toBe('会话 sess-a')
    expect(mailAddressLabel('each:project:gofer')).toBe('项目 gofer 全部')
    expect(mailAddressLabel('each:agent:suga')).toBe('agent suga 全部')
    expect(mailAddressLabel('caller')).toContain('收集箱')
  })
})
