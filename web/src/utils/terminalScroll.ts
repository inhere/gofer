// Mobile pty scrolling is handled against xterm's own viewport. Browsers do not
// translate a touchmove on the xterm canvas into viewport scrolling consistently.
export const TERMINAL_SCROLLBACK = 10_000

export function touchScrollDelta(startY: number, currentY: number): number {
  return startY - currentY
}

export function touchScrollLines(
  startY: number,
  currentY: number,
  lineHeight: number,
): number {
  const delta = touchScrollDelta(startY, currentY)
  if (delta === 0) {
    return 0
  }
  const pixelsPerLine = Math.max(1, lineHeight)
  const lines = Math.max(1, Math.round(Math.abs(delta) / pixelsPerLine))
  return delta > 0 ? -lines : lines
}
