// 图标字形扫描（issue gofer-h355）：界面里「当图标用的 Unicode 字形」一律不再出现，
// 图标统一走 ui/Icon.vue（lucide）。设计稿 §5、§8。
//
// 这份清单就是本次替换掉的字形集合。新增图标时 import lucide 组件，而不是再打一个字符；
// 确实要保留（正文、日志、diff、markdown 里的字符不在此列）就在 WHITELIST 里写文件与理由。
//
// 扫描范围：web/src 下的 .vue / .ts，排除测试文件、i18n 字典（业务文案允许出现任意字符）、
// 以及注释（注释里提到旧字形不影响渲染）。
import { describe, expect, it } from 'vitest'

// 本次从界面里清掉的图标字形：导航（◉▤▥◇⧉⌁✉⚑▦⇉◷⬡◈⌂⚙ 里的那些）、状态点、折叠箭头、
// 顶栏按钮、窗口关闭、主题切换等。
const BANNED = {
  '◉': '导航·今天',
  '▤': '导航·工作台',
  '▥': '导航·工作项',
  '◇': '导航·计划',
  '⧉': '导航·任务',
  '⌁': '导航·会话',
  '✉': '信箱（导航 / 顶栏）',
  '▦': '导航·统计',
  '⇉': '导航·工作流',
  '◷': '导航·定时',
  '⬡': '导航·运行节点',
  '■': '停止 / 回合结束',
  '⋯': '更多（手机抽屉入口 / 工作台头部）',
  '⚙': '有参数时运行 / 设置',
  '⌂': '导航·项目',
  '⟨': '侧栏收起',
  '⟩': '侧栏展开',
  '☀': '主题切换到浅色',
  '☾': '主题切换到深色',
  '◔': '待我决策（手机）',
  '↯': '有新版本',
  '▸': '展开',
  '▾': '收起',
  '▴': '展开（项目筛选）',
} as const

// 白名单：文件（相对 web/src）→ 理由。只放「确实不该改」的，不放「还没改」的。
const WHITELIST: Record<string, string> = {}

const SOURCES = import.meta.glob(['../**/*.vue', '../**/*.ts'], {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

// stripComments：去掉 HTML 注释、块注释与整行 // 注释，保留行号（注释里提到旧字形不算）。
function stripComments(src: string): string {
  const keepLines = (m: string): string => '\n'.repeat((m.match(/\n/g) ?? []).length)
  return src
    .replace(/<!--[\s\S]*?-->/g, keepLines)
    .replace(/\/\*[\s\S]*?\*\//g, keepLines)
    .split('\n')
    .map((line) => (line.trimStart().startsWith('//') ? '' : line))
    .join('\n')
}

function filesToScan(): Array<{ path: string; source: string }> {
  const out: Array<{ path: string; source: string }> = []
  for (const [key, raw] of Object.entries(SOURCES)) {
    // glob 的键：同目录是 './x'，其他是 '../ui/x'
    const path = key.replace(/^\.\.\//, '').replace(/^\.\//, '')
    if (/\.test\.ts$/.test(path)) continue
    // 字典与页面文案：业务内容不翻译也不改字符
    if (path.startsWith('i18n/')) continue
    if (path in WHITELIST) continue
    out.push({ path, source: stripComments(raw) })
  }
  return out
}

describe('icon glyph scan', () => {
  it('scans a meaningful part of web/src', () => {
    const files = filesToScan()
    expect(files.length).toBeGreaterThan(50)
    expect(files.some((f) => f.path === 'AppShell.vue')).toBe(true)
    expect(files.some((f) => f.path === 'utils/shell.ts')).toBe(true)
    // 防止 glob 退回空字符串导致用例空转
    expect(files.reduce((n, f) => n + f.source.length, 0)).toBeGreaterThan(1_000_000)
  })

  it('has no character glyphs used as icons left in the sources', () => {
    const offenders: string[] = []
    let checked = 0
    for (const { path, source } of filesToScan()) {
      source.split('\n').forEach((line, i) => {
        checked++
        for (const ch of Object.keys(BANNED)) {
          if (line.includes(ch)) offenders.push(`${path}:${i + 1}: ${ch}（${BANNED[ch as keyof typeof BANNED]}）${line.trim()}`)
        }
      })
    }
    // 行数要多到不像空转（约 5 万行）
    expect(checked).toBeGreaterThan(20_000)
    expect(offenders).toEqual([])
  })

  it('keeps the whitelist documented instead of silently skipping files', () => {
    for (const [path, reason] of Object.entries(WHITELIST)) {
      expect(reason.length, `${path} 的白名单理由太短`).toBeGreaterThan(5)
    }
  })
})
