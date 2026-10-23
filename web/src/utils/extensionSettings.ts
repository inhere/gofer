// K5：扩展设置页的纯逻辑（views/settings/Extensions.vue 用，单测在
// extensionSettings.test.ts）。
//
// 这里只做「把接口数据摆成界面」的事：按键名前缀分组、类型 → 编辑控件、列表文本 ↔
// 数组、保存前的基本校验。**服务端仍是唯一权威**——前端校验只是为了少跑一趟 400，
// 服务端拒绝的原因原样显示。
import type { SettingItem, SettingKind, SettingValue } from '../api/types'
import { t } from '../i18n'

// 组的显示名（键前缀就是组名：work.* / prompt.*）。未知组用组名本身，新注册的组不会
// 因为前端没跟上而空白。
export const GROUP_LABELS: Record<string, string> = {
  get work() { return t('settings.ext.group.work') },
  get prompt() { return t('settings.ext.group.prompt') },
}

export interface SettingGroup {
  group: string
  label: string
  items: SettingItem[]
}

// groupSettings 按注册顺序分组：同组连续出现的键归一张卡（接口按注册表顺序返回）。
// 组第一次出现的位置就是它在页面上的位置。
export function groupSettings(items: SettingItem[]): SettingGroup[] {
  const out: SettingGroup[] = []
  const index = new Map<string, SettingGroup>()
  for (const it of items) {
    let g = index.get(it.group)
    if (!g) {
      g = { group: it.group, label: GROUP_LABELS[it.group] ?? it.group, items: [] }
      index.set(it.group, g)
      out.push(g)
    }
    g.items.push(it)
  }
  return out
}

// 编辑控件的种类：int → 数字输入，bool → 开关，列表 → 一行一个的多行输入，
// 提示词（multiline）→ 大文本域，其余 string → 单行输入。
export type ControlKind = 'int' | 'bool' | 'list' | 'textarea' | 'text'

export function controlFor(item: Pick<SettingItem, 'kind' | 'multiline'>): ControlKind {
  switch (item.kind) {
    case 'int':
      return 'int'
    case 'bool':
      return 'bool'
    case 'strings':
    case 'patterns':
      return 'list'
    default:
      return item.multiline ? 'textarea' : 'text'
  }
}

// kindLabel 是类型的显示名（卡片上的类型标签）。
export function kindLabel(kind: SettingKind): string {
  switch (kind) {
    case 'int':
      return t('settings.ext.kind.int')
    case 'bool':
      return t('settings.ext.kind.bool')
    case 'strings':
      return t('settings.ext.kind.strings')
    case 'patterns':
      return t('settings.ext.kind.patterns')
    default:
      return t('settings.ext.kind.string')
  }
}

// listText 把列表值渲染成「一行一个」的文本。
export function listText(v: SettingValue | null | undefined): string {
  if (!Array.isArray(v)) return ''
  return v.join('\n')
}

// parseList 把多行文本解析成列表：去掉行首尾空白与空行（注释行不特殊处理——列表项
// 本身就是数据，`#` 可能是某个 cwd 片段的一部分）。
export function parseList(text: string): string[] {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')
}

// valueText 是人读的取值：空列表「（空）」、空字符串「（空）」、开关「开 / 关」。
export function valueText(v: SettingValue | null | undefined): string {
  if (v === null || v === undefined) return t('settings.ext.empty')
  if (Array.isArray(v)) return v.length === 0 ? t('settings.ext.empty') : v.join(t('util.sep.comma'))
  if (typeof v === 'boolean') return v ? t('settings.ext.on') : t('settings.ext.off')
  const s = String(v)
  return s === '' ? t('settings.ext.empty') : s
}

// boundsHint 是输入框旁的取值提示：int 给闭区间，string 给字节上限。
export function boundsHint(item: Pick<SettingItem, 'kind' | 'min' | 'max' | 'max_len'>): string {
  if (item.kind === 'int') {
    const lo = item.min ?? null
    const hi = item.max ?? null
    if (lo !== null && hi !== null) return t('settings.ext.bounds.range', { lo, hi })
    if (lo !== null) return `≥ ${lo}`
    if (hi !== null) return `≤ ${hi}`
    return t('settings.ext.bounds.int')
  }
  if (item.max_len && item.max_len > 0) {
    return t('settings.ext.bounds.maxLen', { n: item.max_len })
  }
  return ''
}

// utf8Len 数字节的长度（max_len 是字节，不是字符：一个汉字 3 字节）。
export function utf8Len(s: string): number {
  return new TextEncoder().encode(s).length
}

// validRegex 报告一条正则能否编译（服务端也会编译，这里只是先给出位置明确的提示）。
export function validRegex(pattern: string): boolean {
  try {
    new RegExp(pattern)
    return true
  } catch {
    return false
  }
}

// draftOf 给一个键当前的编辑态初值：列表是「一行一个」的文本，其余直接取生效值。
export function draftOf(item: SettingItem): Draft {
  const kind = controlFor(item)
  if (kind === 'bool') return item.value === true
  if (kind === 'list') return listText(item.value)
  if (kind === 'int') {
    return typeof item.value === 'number' ? String(item.value) : item.value ? String(item.value) : ''
  }
  return typeof item.value === 'string' ? item.value : String(item.value ?? '')
}

// Draft 是编辑控件里的值：开关是 boolean，其余都是文本。
export type Draft = string | boolean

// valueOfDraft 把编辑态转成提交值。数字输入解析成 number（空 / 非法时交给校验拦），
// 列表解析成数组，提示词与字符串原样提交（不 trim：提示词的空格与换行有意义）。
export function valueOfDraft(item: SettingItem, draft: Draft): SettingValue {
  const kind = controlFor(item)
  if (kind === 'bool') return draft === true
  const text = typeof draft === 'string' ? draft : ''
  if (kind === 'int') {
    const n = Number.parseInt(text.trim(), 10)
    return Number.isFinite(n) ? n : 0
  }
  if (kind === 'list') return parseList(text)
  return text
}

// sameValue 比较两个取值（列表逐项比）；用于「有没有改过」与「是否等于默认」。
export function sameValue(a: SettingValue | null | undefined, b: SettingValue | null | undefined): boolean {
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b)) return false
    if (a.length !== b.length) return false
    return a.every((x, i) => x === b[i])
  }
  return a === b
}

// dirty 是「编辑框里的值跟当前生效值不同」——保存按钮只在它成立时可用。
export function dirty(item: SettingItem, draft: Draft): boolean {
  return !sameValue(valueOfDraft(item, draft), item.value)
}

// validateDraft 做保存前的基本校验，返回第一条错误（'' 表示通过）。服务端会再校验一遍，
// 它给出的原因才是最终的（例如正则语法错误的具体位置）。
export function validateDraft(item: SettingItem, draft: Draft): string {
  const kind = controlFor(item)
  if (kind === 'bool') return ''
  const text = typeof draft === 'string' ? draft : ''
  if (kind === 'int') {
    const raw = text.trim()
    if (raw === '' || !/^[-+]?\d+$/.test(raw)) return t('settings.ext.err.int')
    const n = Number.parseInt(raw, 10)
    if (item.min !== undefined && n < item.min) return t('settings.ext.err.min', { min: item.min })
    if (item.max !== undefined && n > item.max) return t('settings.ext.err.max', { max: item.max })
    return ''
  }
  if (kind === 'list') {
    const lines = parseList(text)
    if (item.kind === 'patterns') {
      for (const line of lines) {
        if (!validRegex(line)) return t('settings.ext.err.regex', { pattern: line })
      }
    }
    return ''
  }
  if (item.max_len && item.max_len > 0 && utf8Len(text) > item.max_len) {
    return t('settings.ext.err.tooLong', { len: utf8Len(text), max: item.max_len })
  }
  return ''
}

// promptHint 是提示词键在空值时该显示的说明。提示词的「代码里的默认全文」不在接口里
// （注册表里 prompt.* 的 default 是空串，含义就是「用内置默认」），所以界面只说这一句。
export const PROMPT_EMPTY_HINT = (): string => t('settings.ext.promptEmpty')

// isPrompt 判断一个键是不是提示词覆盖（空 = 用内置默认）。
export function isPrompt(item: Pick<SettingItem, 'key' | 'kind' | 'multiline'>): boolean {
  return item.multiline === true || (item.kind === 'string' && item.key.startsWith('prompt.'))
}

// defaultText 是「对比默认」里显示的内容：提示词的默认是空串时给出上面那句说明。
export function defaultText(item: SettingItem): string {
  if (isPrompt(item) && (item.default === '' || item.default === null || item.default === undefined)) {
    return PROMPT_EMPTY_HINT()
  }
  return valueText(item.default)
}

// importActionLabel 是导入预览里一行动作的说法。
export function importActionLabel(action: string): string {
  switch (action) {
    case 'set':
      return t('settings.ext.import.set')
    case 'reset':
      return t('settings.ext.import.reset')
    default:
      return t('settings.ext.import.keep')
  }
}

// importSummary 汇总一次预览 / 写入：多少行要改、多少行被拒。
export function importSummary(changes: { action: string; error?: string }[]): string {
  let set = 0
  let reset = 0
  let unchanged = 0
  let refused = 0
  for (const c of changes) {
    if (c.error) {
      refused++
      continue
    }
    if (c.action === 'set') set++
    else if (c.action === 'reset') reset++
    else unchanged++
  }
  const parts = [
    t('settings.ext.importSummary.set', { n: set }),
    t('settings.ext.importSummary.reset', { n: reset }),
    t('settings.ext.importSummary.keep', { n: unchanged }),
  ]
  if (refused > 0) parts.push(t('settings.ext.importSummary.refused', { n: refused }))
  return parts.join(' · ')
}
