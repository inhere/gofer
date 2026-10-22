// 页签的 URL 契约（设计稿 §8：Tabs 支持 ?tab= 深链）：纯函数，组件与测试共用。
// 默认页签不写进 URL（链接干净），其他页签写进去（可分享、可前进后退）。
export interface TabItem {
  value: string
  label: string
  count?: number
  /** 需要人处理的页签显示黄色小角标 */
  badge?: boolean
}

export function defaultTabValue(tabs: TabItem[], fallback = ''): string {
  return fallback || tabs[0]?.value || ''
}

// ?tab= 里认不出来的值（旧链接、手改的 URL）回落到默认页签。
export function tabFromQuery(
  query: Record<string, unknown> | undefined,
  tabs: TabItem[],
  param = 'tab',
  fallback = '',
): string {
  const raw = query?.[param]
  const value = typeof raw === 'string' ? raw : ''
  return tabs.some((tab) => tab.value === value) ? value : defaultTabValue(tabs, fallback)
}

// 切换页签后的 query：保留其他参数，默认页签删掉该键。
export function queryWithTab(
  query: Record<string, unknown> | undefined,
  value: string,
  tabs: TabItem[],
  param = 'tab',
  fallback = '',
): Record<string, string> {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries(query ?? {})) {
    if (typeof v === 'string' && v) next[k] = v
  }
  if (value === defaultTabValue(tabs, fallback)) delete next[param]
  else next[param] = value
  return next
}
