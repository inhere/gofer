// 渲染后的 markdown（v-html 里是 HTML）不能放进 white-space: pre-wrap 的容器：标签之间的换行
// 会变成一行行空白（会话「最后一条消息」展开后空行很多，2026-10-23）。这里扫所有 .vue：
// 带 v-html 且表达式是 markdown 渲染的元素，它的类在同文件的样式里不得声明 pre-wrap，
// 除非同一组合选择器里显式改回 normal。
import { describe, expect, it } from 'vitest'

const SOURCES = import.meta.glob('/src/**/*.vue', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

function offenders(): string[] {
  const out: string[] = []
  for (const [file, src] of Object.entries(SOURCES)) {
    const style = src.slice(src.indexOf('<style'))
    for (const m of src.matchAll(/<\w+([^>]*?)v-html="([^"]*)"/gs)) {
      if (!/md|markdown|render/i.test(m[2])) continue
      const cls = /class="([^"]+)"/.exec(m[1])
      if (!cls) continue
      const classes = cls[1].split(/\s+/)
      for (const c of classes) {
        const rule = new RegExp(`\\.${c.replace(/[-]/g, '\\-')}\\s*\\{([^}]*)\\}`).exec(style)
        if (!rule || !rule[1].includes('pre-wrap')) continue
        const reset = classes.some((o) => o !== c && new RegExp(`\\.${c}\\.${o}\\s*\\{[^}]*white-space:\\s*normal`).test(style))
        if (!reset) out.push(`${file}: .${c} (${m[2].slice(0, 40)})`)
      }
    }
  }
  return out
}

describe('rendered markdown whitespace', () => {
  it('never puts rendered markdown in a pre-wrap container', () => {
    expect(offenders()).toEqual([])
  })
})
