// 工作台深链：`?thread=` 指向的会话可能早于首页窗口，首页请求要用 include 把它（以及打开着的会话）带上，
// 否则推送刷新后列表里没有它，已打开的窗格会变空（gofer-7yrg）。服务端对 include 的数量有上限。
export const MAX_INCLUDE_IDS = 8

// 路由目标排第一，其后是打开着的会话；去空、去重、截断。
export function includeIDs(routeThread: unknown, wanted: string[]): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  const add = (id: unknown) => {
    if (typeof id !== 'string' || !id || seen.has(id)) return
    seen.add(id)
    out.push(id)
  }
  add(routeThread)
  wanted.forEach(add)
  return out.slice(0, MAX_INCLUDE_IDS)
}

// 路由目标是非空字符串且不在已加载集合里，就需要重拉首页。
export function needsReload(routeThread: unknown, loaded: { has(id: string): boolean }): boolean {
  return typeof routeThread === 'string' && routeThread !== '' && !loaded.has(routeThread)
}
