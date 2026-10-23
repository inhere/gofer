// CSS 变量不许「靠写死的兜底色」活着：web/src 里 var(--x) 引用的每个变量，必须在
// styles/tokens.css（或其它 src/styles/*.css）里定义，或者在用到它的同一个组件 / 文件里局部
// 定义（例如 SessionGrantDialog 的 --risk：按风险等级在本地覆写）。否则深浅主题切换时它不跟着
// 变——2026-10-23 全页面截图检查里 --muted / --warn / --hover / --dim / --accent-fg / --fs-base
// 就是这样漏掉的（--muted 那次一律拿 #888 兜底，暗色下还能看，浅色下就糊了）。
import { describe, expect, it } from 'vitest'

// 源码用 Vite 的 ?raw 一次性读进来（与 i18n/noHardcodedCjk.test.ts 同一口径，不给测试引入 @types/node）。
const VUE = import.meta.glob('/src/**/*.vue', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const CSS = import.meta.glob('/src/**/*.css', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const RAW: Record<string, string> = { ...VUE, ...CSS }

const STYLES = /^\/src\/styles\/[^/]+\.css$/

function defs(text: string): Set<string> {
  const out = new Set<string>()
  // 定义形式：`--name: value`（样式块）与内联样式对象 / 字符串里的 `--name: value`、
  // `'--name': value`。
  for (const m of text.matchAll(/(^|[\s{;,'"`])(--[a-zA-Z0-9-]+)['"]?\s*:/gm)) out.add(m[2])
  return out
}

function uses(text: string): Set<string> {
  const out = new Set<string>()
  for (const m of text.matchAll(/var\(\s*(--[a-zA-Z0-9-]+)/g)) out.add(m[1])
  return out
}

function rel(path: string): string {
  return path.replace(/^\/src\//, '')
}

// 全局 token：src/styles 下所有样式文件里定义的变量。
function globalDefs(): Set<string> {
  const out = new Set<string>()
  for (const [path, text] of Object.entries(RAW)) {
    if (!STYLES.test(path)) continue
    for (const v of defs(text)) out.add(v)
  }
  return out
}

describe('CSS custom properties used in web/src are defined', () => {
  it('every var(--x) comes from tokens (or is defined locally in the same component)', () => {
    const global = globalDefs()
    expect(global.size).toBeGreaterThan(20) // tokens.css 真的被读到了
    const missing: string[] = []
    for (const [path, text] of Object.entries(RAW)) {
      if (path.endsWith('.test.ts')) continue
      const local = defs(text)
      for (const name of uses(text)) {
        if (global.has(name) || local.has(name)) continue
        missing.push(`${rel(path)}: ${name}`)
      }
    }
    // 排序让失败信息稳定（glob 的键顺序各环境不同）。
    expect(missing.sort()).toEqual([])
  })

  it('the tokens the screenshot check found missing are now real tokens', () => {
    const global = globalDefs()
    for (const name of ['--ink-3', '--need', '--need-ink', '--surface-2', '--accent-ink', '--fs-md', '--fs-input-mobile']) {
      expect(global.has(name), name).toBe(true)
    }
    // 旧拼写（曾靠兜底色）：不许再出现在任何 var() 引用里。
    const legacy = ['--muted', '--warn', '--hover', '--dim', '--accent-fg', '--fs-base']
    const offenders: string[] = []
    for (const [path, text] of Object.entries(RAW)) {
      for (const name of uses(text)) {
        if (legacy.includes(name)) offenders.push(`${rel(path)}: ${name}`)
      }
    }
    expect(offenders.sort()).toEqual([])
  })
})
