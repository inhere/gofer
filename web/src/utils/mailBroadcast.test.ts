import { describe, expect, it } from 'vitest'
import { buildMailSend, mailRecipientPreview, parseMailRecipients } from './mail'

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
