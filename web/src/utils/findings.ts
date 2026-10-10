// 「发现但不碰」解析（gofer-3nxa.3）：agent 汇报里范围外发现的问题写在这一节，验收面板单列
// 成「发现」页签。规则与 Go 侧 internal/job/findings.go 一致（两边测试用同一组用例）：
//   取汇报中【最后一个】标题为「发现但不碰」（兼容 Out of scope / Out-of-scope findings，大小写
//   不敏感，任意级别 #）的小节，到下一个同级或更高级标题为止，抽出列表项；带缩进的续行并入
//   上一条；代码块（``` / ~~~）里的标题不算。

export const FINDINGS_SECTION_TITLE = '发现但不碰'

const HEADING_RE = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/
const ITEM_RE = /^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?(.*)$/
const FENCE_RE = /^\s{0,3}(```|~~~)/

function trimChars(s: string, chars: string): string {
  let start = 0
  let end = s.length
  while (start < end && chars.includes(s[start])) start++
  while (end > start && chars.includes(s[end - 1])) end--
  return s.slice(start, end)
}

function isFindingsTitle(title: string): boolean {
  let t = trimChars(title.trim(), ':： ')
  t = trimChars(t, '*_`「」"\'').trim()
  t = trimChars(t, ':： ')
  if (t === FINDINGS_SECTION_TITLE) {
    return true
  }
  const n = t.toLowerCase().replace(/-/g, ' ').split(/\s+/).filter(Boolean).join(' ')
  return n === 'out of scope' || n === 'out of scope findings'
}

export function parseFindings(report: string | undefined | null): string[] {
  let items: string[] = []
  let inside = false
  let level = 0
  let inFence = false
  for (const raw of (report ?? '').split('\n')) {
    const line = raw.replace(/[ \t\r]+$/, '')
    if (FENCE_RE.test(line)) {
      inFence = !inFence
      continue
    }
    if (inFence) {
      continue
    }
    const h = HEADING_RE.exec(line)
    if (h) {
      if (isFindingsTitle(h[2])) {
        // 后出现的同名小节覆盖前面的：汇报末尾才是结论。
        items = []
        inside = true
        level = h[1].length
        continue
      }
      if (inside && h[1].length <= level) {
        inside = false
      }
      continue
    }
    if (!inside || line.trim() === '') {
      continue
    }
    const m = ITEM_RE.exec(line)
    if (m) {
      const text = m[1].trim()
      if (text !== '') {
        items.push(text)
      }
      continue
    }
    if (items.length > 0 && /^[ \t]/.test(line)) {
      items[items.length - 1] += ' ' + line.trim()
    }
  }
  return items
}

// shellQuote：把文本包成 POSIX shell 的双引号参数（转义 \ " $ `）。
function shellQuote(s: string): string {
  return '"' + s.replace(/[\\"$`]/g, '\\$&') + '"'
}

// findingIssueCommand：一条发现 → 在当前仓库建 issue 的命令（服务端暂无建 tracker issue 的
// 写接口，验收面板只负责生成命令，复制到仓库里执行）。
export function findingIssueCommand(text: string, jobId: string): string {
  return `gofer issue create ${shellQuote(text.replace(/\s+/g, ' ').trim())} -d ${shellQuote(`discovered in job ${jobId}`)} --tag discovered`
}
