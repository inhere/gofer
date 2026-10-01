import { describe, expect, it } from 'vitest'
import { appendRenderedLog } from './logPlaceholder'

describe('appendRenderedLog', () => {
  it('replaces the empty stdout placeholder when the first live text arrives', () => {
    const pre = {
      textContent: '（无 stdout 输出）',
      insertAdjacentHTML(_position: string, html: string) { this.textContent += html },
    }
    appendRenderedLog(pre, '<span>hello</span>', 'stdout')
    expect(pre.textContent).toBe('<span>hello</span>')
  })

  it('preserves earlier output when a later chunk arrives', () => {
    const pre = {
      textContent: 'first',
      insertAdjacentHTML(_position: string, html: string) { this.textContent += html },
    }
    appendRenderedLog(pre, 'second', 'stdout')
    expect(pre.textContent).toBe('firstsecond')
  })
})
