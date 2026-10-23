// 概览的「最近日志」是纯文本 pre（不走 LogTape 的 ANSI 渲染），所以必须先去掉转义码：
// 截图检查 P6-2 里它把 `□[2m` / `□[36m` 这类控制字符直接显示给了用户。
import { describe, expect, it } from 'vitest'
import source from './JobDetail.vue?raw'
import { stripAnsi } from '../utils/logRender'

describe('job detail recent log', () => {
  it('strips ANSI escapes before showing the tail', () => {
    const start = source.indexOf('const logTail = computed')
    expect(start).toBeGreaterThan(-1)
    const block = source.slice(start, start + 400)
    expect(block).toContain('stripAnsi(')
    // 6 行不够就不行（紧凑卡片的约定），别在改的时候丢掉。
    expect(block).toContain('slice(-6)')
  })

  it('renders the tail as text, not as HTML', () => {
    const start = source.indexOf('class="log-tail-pre')
    expect(start).toBeGreaterThan(-1)
    expect(source).toContain('{{ logTail.join(\'\\n\') }}')
  })

  it('the stripper leaves a readable line from a coloured log', () => {
    const raw = '\u001b[2m$ npm test\u001b[22m\n\u001b[36m✓\u001b[0m 12 passed'
    expect(stripAnsi(raw)).toBe('$ npm test\n✓ 12 passed')
  })
})
