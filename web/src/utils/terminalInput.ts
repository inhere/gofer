// Keep Escape in xterm's normal key path so its onData event owns the byte.
export function handleTerminalShortcut(
  ev: KeyboardEvent,
  hasSelection: boolean,
  copySelection: () => void,
  sendInput: (data: string) => void,
): boolean {
  if (ev.type !== 'keydown') return false
  const ctrl = ev.ctrlKey || ev.metaKey
  if (!ctrl) return false
  const key = ev.key.toLowerCase()
  if (key !== 'c' && key !== 'v') return false

  if (key === 'c') ev.preventDefault()
  ev.stopPropagation()
  ev.stopImmediatePropagation()
  if (key === 'c') {
    if (hasSelection) copySelection()
    else sendInput('\x03')
  }
  return true
}

export function forwardTerminalData(data: string, sendInput: (data: string) => void): void {
  sendInput(data)
}
