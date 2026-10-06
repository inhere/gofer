// 卡片「展开 / 收起」的状态：只记哪些 id 展开着。Sessions 页三个区与「工作」页共用同一份逻辑，
// 返回新的 Set（而不是原地改），好让 Vue 的 ref 触发更新。

export function toggleExpanded(open: ReadonlySet<string>, id: string): Set<string> {
  const next = new Set(open)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  return next
}

// 列表刷新后丢掉已经不存在的 id，免得 Set 无限增长。
export function pruneExpanded(open: ReadonlySet<string>, liveIds: Iterable<string>): Set<string> {
  const live = new Set(liveIds)
  const next = new Set<string>()
  for (const id of open) if (live.has(id)) next.add(id)
  return next
}
