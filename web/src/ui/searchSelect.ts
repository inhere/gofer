// 可搜索下拉的纯逻辑（issue gofer-b8nh）：把选项按输入词过滤并排序。
// 组件（ui/SearchSelect.vue）只负责渲染与开关，判定都放这里，便于单测。
//
// 匹配口径：
//   - 大小写不敏感、包含匹配（label / value / keywords 三处都查）；
//   - 排序：完全相等 → 0，前缀命中 → 1，词首命中（分隔符后）→ 2，其它包含 → 3；
//     同分保持原始顺序（sort 是稳定的），所以「前缀优先」不会打乱同组顺序；
//   - 查询词为空时原样返回（保持调用方的顺序）。
export interface SearchOption {
  value: string
  label: string
  /** 次要说明（如项目路径、agent 类型），显示但不参与搜索 */
  hint?: string
  /** 额外参与匹配的词（别名：拼音首字母、路径、旧名字） */
  keywords?: string
  disabled?: boolean
}

const SEPARATORS = /[\s/\\_.\-:·|]/

function normalize(text: string): string {
  return text.trim().toLowerCase()
}

/** 词首命中：q 出现在 hay 的开头，或紧跟在分隔符之后。 */
function hitsWordStart(hay: string, q: string): boolean {
  let from = 0
  for (;;) {
    const at = hay.indexOf(q, from)
    if (at < 0) return false
    if (at === 0 || SEPARATORS.test(hay[at - 1])) return true
    from = at + 1
  }
}

/**
 * 单个选项的匹配等级：-1 = 不匹配，数字越小越靠前。
 * 大小写不敏感；空查询词命中一切（等级 0）。
 */
export function matchRank(option: SearchOption, query: string): number {
  const q = normalize(query)
  if (!q) return 0
  const label = option.label.toLowerCase()
  const value = option.value.toLowerCase()
  const keywords = (option.keywords ?? '').toLowerCase()
  if (label === q || value === q) return 0
  if (label.startsWith(q) || value.startsWith(q)) return 1
  if (hitsWordStart(label, q) || hitsWordStart(value, q)) return 2
  if (label.includes(q) || value.includes(q)) return 3
  if (keywords.includes(q)) return hitsWordStart(keywords, q) ? 2 : 3
  return -1
}

/** 按查询词过滤并排序；空查询词原样返回（不拷贝顺序变化）。 */
export function filterOptions(options: SearchOption[], query: string): SearchOption[] {
  if (!normalize(query)) return options
  const scored: Array<{ option: SearchOption; rank: number }> = []
  for (const option of options) {
    const rank = matchRank(option, query)
    if (rank >= 0) scored.push({ option, rank })
  }
  scored.sort((a, b) => a.rank - b.rank)
  return scored.map((s) => s.option)
}

/** 打开面板时的初始高亮：当前值在（过滤后的）列表里的下标，找不到就是第一项。 */
export function initialIndex(options: SearchOption[], value: string): number {
  if (!options.length) return -1
  const at = options.findIndex((o) => o.value === value)
  return at >= 0 ? at : 0
}
