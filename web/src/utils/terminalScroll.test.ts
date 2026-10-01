import { describe, expect, it } from 'vitest'
import {
  TERMINAL_SCROLLBACK,
  touchScrollDelta,
  touchScrollLines,
} from './terminalScroll'

describe('terminal touch scrolling', () => {
  it('converts an upward finger movement into a positive viewport delta', () => {
    expect(touchScrollDelta(620, 420)).toBe(200)
    expect(touchScrollDelta(420, 620)).toBe(-200)
    expect(touchScrollLines(620, 420, 20)).toBe(-10)
    expect(touchScrollLines(420, 620, 20)).toBe(10)
  })

  it('keeps enough xterm history for several mobile screens', () => {
    expect(TERMINAL_SCROLLBACK).toBeGreaterThanOrEqual(10_000)
  })
})
