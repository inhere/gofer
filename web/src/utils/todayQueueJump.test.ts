import { describe, expect, it } from 'vitest'
import { isNextCardKey, nextQueueIndex } from './todayQueueJump'

// 极简 DOM 替身：isTypingTarget 只读 tagName / isContentEditable。
function el(tagName: string, extra: { isContentEditable?: boolean } = {}): EventTarget {
  return { tagName, isContentEditable: extra.isContentEditable ?? false } as unknown as EventTarget
}

describe('isNextCardKey', () => {
  it('accepts Ctrl+J and Cmd+J', () => {
    expect(isNextCardKey({ key: 'j', ctrlKey: true })).toBe(true)
    expect(isNextCardKey({ key: 'j', metaKey: true })).toBe(true)
    expect(isNextCardKey({ key: 'J', ctrlKey: true })).toBe(true)
  })

  it('ignores the key without Ctrl/Cmd, or with Alt/Shift', () => {
    expect(isNextCardKey({ key: 'j' })).toBe(false)
    expect(isNextCardKey({ key: 'j', ctrlKey: true, altKey: true })).toBe(false)
    expect(isNextCardKey({ key: 'j', ctrlKey: true, metaKey: true, shiftKey: true })).toBe(false)
    expect(isNextCardKey({ key: 'k', ctrlKey: true })).toBe(false)
  })

  it('does not fire while typing in an input, textarea, select or rich editor', () => {
    expect(isNextCardKey({ key: 'j', ctrlKey: true, target: el('INPUT') })).toBe(false)
    expect(isNextCardKey({ key: 'j', ctrlKey: true, target: el('TEXTAREA') })).toBe(false)
    expect(isNextCardKey({ key: 'j', ctrlKey: true, target: el('SELECT') })).toBe(false)
    expect(isNextCardKey({ key: 'j', ctrlKey: true, target: el('DIV', { isContentEditable: true }) })).toBe(false)
    // 普通元素 / 没有焦点信息时照常触发
    expect(isNextCardKey({ key: 'j', ctrlKey: true, target: el('BODY') })).toBe(true)
    expect(isNextCardKey({ key: 'j', ctrlKey: true })).toBe(true)
  })

  it('does not fire while an IME is composing', () => {
    expect(isNextCardKey({ key: 'j', ctrlKey: true, isComposing: true })).toBe(false)
  })
})

describe('nextQueueIndex', () => {
  it('starts at the first card and cycles', () => {
    expect(nextQueueIndex(-1, 3)).toBe(0)
    expect(nextQueueIndex(0, 3)).toBe(1)
    expect(nextQueueIndex(2, 3)).toBe(0)
  })

  it('wraps after cards leave the queue', () => {
    // 高亮第 5 张时前面处理掉两张：光标越界也要落回合法下标
    expect(nextQueueIndex(4, 3)).toBe(2)
    expect(nextQueueIndex(7, 3)).toBe(2)
  })

  it('returns -1 for an empty queue', () => {
    expect(nextQueueIndex(0, 0)).toBe(-1)
    expect(nextQueueIndex(-1, 0)).toBe(-1)
  })
})
