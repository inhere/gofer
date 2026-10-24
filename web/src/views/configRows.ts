// 配置总览的行构造（gofer-4uk5）：GET /v1/config 现在会带回每个顶层段，控制台把它们
// 按分组展平成「键 = 值」的行来展示。这里只管取值与格式化，文案由调用方注入（两本字典、
// 两种语言），所以是纯函数、可以单独测。

export interface CfgRow {
  label: string
  value: string
  // set 只有在该键是服务端的脱敏表示（`*_set`）时才有值：true = 已配置，false = 未配置。
  set?: boolean
}

export interface CfgSection {
  key: string
  title: string
  rows: CfgRow[]
  // 列表型段（tools / mcp_servers / secrets / pricing / mail.agents）的条目。
  items?: { label: string; rows: CfgRow[] }[]
}

export interface CfgLabels {
  yes: string
  no: string
  set: string
  unset: string
  // 未设置的取值（服务端回 null = 继承默认）。
  def: string
  sep: string
}

// isSetKey 是「这是一个敏感字段的状态位」的判定：服务端对不可回声的值只发 `*_set`
// （token_set / secret_env_set / proxy_url_set …），而密钥登记的每条只发一个 `set`。
// 两者都必须渲染成「已配置 / 未配置」，不能落到普通布尔的「是 / 否」。
export function isSetKey(key: string): boolean {
  return key.endsWith('_set') || key === 'set'
}

// formatCfgValue 把一个配置值渲染成一段文本。
// 状态位必须先于布尔判断（见 isSetKey）。
export function formatCfgValue(key: string, v: unknown, labels: CfgLabels): string {
  if (isSetKey(key)) {
    return v === true ? labels.set : labels.unset
  }
  if (v === null || v === undefined) {
    return labels.def
  }
  if (typeof v === 'boolean') {
    return v ? labels.yes : labels.no
  }
  if (Array.isArray(v)) {
    return v.length > 0 ? v.map(String).join(labels.sep) : '-'
  }
  if (typeof v === 'string' && v === '') {
    return '-'
  }
  return String(v)
}

// cfgRows 把一个段（可以嵌套）展成「键路径 = 值」的行；空对象/空列表不产生行。
export function cfgRows(obj: Record<string, unknown> | undefined, labels: CfgLabels, prefix = ''): CfgRow[] {
  const out: CfgRow[] = []
  for (const [k, v] of Object.entries(obj ?? {})) {
    const label = prefix ? `${prefix}.${k}` : k
    if (v !== null && typeof v === 'object' && !Array.isArray(v)) {
      out.push(...cfgRows(v as Record<string, unknown>, labels, label))
      continue
    }
    out.push({ label, value: formatCfgValue(k, v, labels), set: isSetKey(k) ? v === true : undefined })
  }
  return out
}

// cfgItems 把列表型段展成条目：条目标题取 titleKey（key / name / model），其余字段为行。
export function cfgItems(
  items: Record<string, unknown>[] | undefined,
  titleKey: string,
  labels: CfgLabels,
): CfgSection['items'] {
  return (items ?? []).map((it) => {
    const { [titleKey]: title, ...rest } = it
    return { label: String(title ?? ''), rows: cfgRows(rest, labels) }
  })
}
