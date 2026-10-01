import { describe, expect, it, vi } from 'vitest'
import { encodeInput } from '../api/attach'
import { handleTerminalShortcut, forwardTerminalData } from './terminalInput'

describe('terminal keyboard ownership', () => {
  it('leaves Escape to xterm when the terminal has focus', () => {
    const preventDefault = vi.fn()
    const event = {
      type: 'keydown', key: 'Escape', ctrlKey: false, metaKey: false,
      preventDefault, stopPropagation: vi.fn(), stopImmediatePropagation: vi.fn(),
    } as unknown as KeyboardEvent
    const send = vi.fn()

    expect(handleTerminalShortcut(event, false, vi.fn(), send)).toBe(false)
    expect(preventDefault).not.toHaveBeenCalled()
    expect(send).not.toHaveBeenCalled()
  })

  it('forwards xterm onData Escape unchanged to the attach input frame', () => {
    const send = vi.fn()

    forwardTerminalData('\x1b', send)

    expect(send).toHaveBeenCalledExactlyOnceWith('\x1b')
    expect(encodeInput(send.mock.calls[0][0])).toBe('Gw==')
  })
})
