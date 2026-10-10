// 验收标准（gofer-3nxa.4）：job / todo 的 acceptance 是一段 markdown（通常是列表）。验收面板
// 把其中的列表项拆成可勾选的行（勾选只是前端本地状态，不持久化，用来人工逐条对照汇报），
// 非列表的段落原样交给 markdown 渲染。
//
// 规则：以 `-` / `*` / `+` / `1.` / `1)` 开头的行是一条；条目前的 `[ ]` / `[x]` 去掉；
// 紧跟在条目后、带缩进的非列表行算该条的续行；其余连续行合成一段 text。

export interface AcceptanceLine {
  kind: 'item' | 'text'
  text: string
}

const ITEM_RE = /^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?(.*)$/

export function acceptanceLines(src: string | undefined | null): AcceptanceLine[] {
  const out: AcceptanceLine[] = []
  for (const raw of (src ?? '').split('\n')) {
    const line = raw.replace(/\s+$/, '')
    const m = ITEM_RE.exec(line)
    const last = out[out.length - 1]
    if (m) {
      out.push({ kind: 'item', text: m[1].trim() })
      continue
    }
    if (line.trim() === '') {
      // 空行结束一段 text；条目之间的空行不产生空段。
      if (last && last.kind === 'text' && !last.text.endsWith('\n\n')) {
        last.text += '\n'
      }
      continue
    }
    if (last && last.kind === 'item' && /^\s+/.test(line)) {
      last.text += ' ' + line.trim()
      continue
    }
    if (last && last.kind === 'text') {
      last.text += '\n' + line
      continue
    }
    out.push({ kind: 'text', text: line })
  }
  return out.map((l) => ({ kind: l.kind, text: l.text.trim() })).filter((l) => l.text !== '')
}
