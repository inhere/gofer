import { describe, expect, it, vi } from 'vitest'
import { encodeInput } from '../api/attach'
import { handleTerminalShortcut, forwardTerminalData } from './terminalInput'

describe('terminal keyboard ownership', () => {
  it('leaves Escape to xterm when the terminal has focus', () => {
    const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    const send = vi.fn()

    expect(handleTerminalShortcut(event, false, vi.fn(), send)).toBe(false)
    expect(event.defaultPrevented).toBe(false)
    expect(send).not.toHaveBeenCalled()
  })

  it('forwards xterm onData Escape unchanged to the attach input frame', () => {
    const send = vi.fn()

    forwardTerminalData('\x1b', send)

    expect(send).toHaveBeenCalledExactlyOnceWith('\x1b')
    expect(encodeInput(send.mock.calls[0][0])).toBe('Gw==')
  })
})
