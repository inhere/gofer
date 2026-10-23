// 决策卡快捷键（设计稿 §10）：纯函数单测。
import { describe, expect, it } from 'vitest'
import { cardKey } from './todayKeys'

describe('cardKey', () => {
  it('maps a / d / s / r / i to the card actions', () => {
    expect(cardKey('a')).toBe('approve')
    expect(cardKey('A')).toBe('approve')
    expect(cardKey('d')).toBe('reject')
    expect(cardKey('s')).toBe('snooze')
    expect(cardKey('r')).toBe('reply')
    expect(cardKey('i')).toBe('info')
  })

  it('maps 1–9 to the action at that position and Esc to dismiss', () => {
    expect(cardKey('1')).toEqual({ kind: 'action', index: 0 })
    expect(cardKey('3')).toEqual({ kind: 'action', index: 2 })
    expect(cardKey('0')).toBeNull()
    expect(cardKey('Escape')).toBe('dismiss')
  })

  it('stays out of the way while typing, composing or with a modifier', () => {
    expect(cardKey('a', { typing: true })).toBeNull()
    expect(cardKey('1', { typing: true })).toBeNull()
    // 输入法组字中不触发（否则拼音里的 d / s 会被当成操作）
    expect(cardKey('d', { composing: true })).toBeNull()
    expect(cardKey('a', { modifier: true })).toBeNull()
    expect(cardKey('Escape', { modifier: true })).toBeNull()
  })

  it('ignores keys the card does not use', () => {
    expect(cardKey('j')).toBeNull()
    expect(cardKey('Enter')).toBeNull()
    expect(cardKey(' ')).toBeNull()
  })
})
