// Issues 页的层级视图：把平铺的 issue 按 parent 挂成树，并算出依赖关系。
// 纯函数，不碰 DOM，便于单测。

export interface IssueNode {
  id: string
  title?: string
  status?: string
  parent?: string
  deps?: Array<{ id: string; type: string }>
}

export interface TreeEntry {
  id: string
  depth: number
  // 全部子项（不受筛选影响）数量与其中已关闭数量，用于「2/8 已关闭」
  childTotal: number
  childClosed: number
  // 当前视图里实际挂在它下面的子项数（筛选后）
  visibleChildren: number
  expanded: boolean
  // 父项不在当前结果里（或不存在）时，子项平铺并标注父项 id
  orphanParent?: string
}

export function isClosed(status: string | undefined): boolean {
  return status === 'closed'
}

// 默认展开进行中的父项（未关闭），已关闭的父项默认折叠；overrides 是用户手动切换。
export function defaultExpanded(status: string | undefined): boolean {
  return !isClosed(status)
}

export function buildIssueTree(
  all: IssueNode[],
  visibleIds: Set<string>,
  overrides: Map<string, boolean> = new Map(),
  flat = false,
): TreeEntry[] {
  const byId = new Map(all.map((n) => [n.id, n]))
  const children = new Map<string, string[]>()
  for (const n of all) {
    if (n.parent && n.parent !== n.id && byId.has(n.parent)) {
      const list = children.get(n.parent) ?? []
      list.push(n.id)
      children.set(n.parent, list)
    }
  }
  const stats = (id: string) => {
    const kids = children.get(id) ?? []
    return { total: kids.length, closed: kids.filter((k) => isClosed(byId.get(k)?.status)).length }
  }
  const out: TreeEntry[] = []
  const seen = new Set<string>()
  const orphanOf = (n: IssueNode): string | undefined =>
    n.parent && n.parent !== n.id && byId.has(n.parent) && !visibleIds.has(n.parent) ? n.parent : undefined

  const emit = (id: string, depth: number): void => {
    if (seen.has(id)) return // 环 / 重复保护
    seen.add(id)
    const n = byId.get(id)!
    const s = stats(id)
    const kids = flat ? [] : (children.get(id) ?? []).filter((k) => visibleIds.has(k))
    const expanded = overrides.get(id) ?? defaultExpanded(n.status)
    out.push({ id, depth, childTotal: s.total, childClosed: s.closed, visibleChildren: kids.length, expanded })
    if (!flat && expanded) for (const k of kids) emit(k, depth + 1)
  }

  for (const n of all) {
    if (!visibleIds.has(n.id) || seen.has(n.id)) continue
    const parentVisible = !flat && !!n.parent && n.parent !== n.id && byId.has(n.parent) && visibleIds.has(n.parent)
    if (parentVisible) continue // 由父项带出
    emit(n.id, 0)
    const e = out.find((x) => x.id === n.id)
    const o = orphanOf(n)
    if (e && o) e.orphanParent = o
  }
  // 环（A 的父是 B、B 的父是 A）里没有任何根：兜底按平铺补上，不丢项。
  // 同时补上「父项折叠时」不显示的子孙之外的真正遗漏。
  for (const n of all) {
    if (visibleIds.has(n.id) && !seen.has(n.id) && !hiddenByCollapse(n.id, byId, visibleIds, overrides, flat)) {
      emit(n.id, 0)
    }
  }
  return out
}

// 祖先链上有被折叠的可见父项 => 这个子项是被折叠藏起来的，不是遗漏。
function hiddenByCollapse(
  id: string, byId: Map<string, IssueNode>, visible: Set<string>, overrides: Map<string, boolean>, flat: boolean,
): boolean {
  if (flat) return false
  const guard = new Set<string>([id])
  let cur = byId.get(id)?.parent
  while (cur && byId.has(cur) && !guard.has(cur)) {
    guard.add(cur)
    const p = byId.get(cur)!
    if (visible.has(cur) && !(overrides.get(cur) ?? defaultExpanded(p.status))) return true
    cur = p.parent
  }
  return false
}

export interface IssueRelations {
  parent?: IssueNode
  parentId?: string // parent 指向不存在的 issue 时保留 id
  children: IssueNode[]
  blockedBy: Array<{ id: string; node?: IssueNode; open: boolean }>
  blocks: Array<{ id: string; node?: IssueNode; open: boolean }>
  other: Array<{ id: string; type: string; node?: IssueNode }>
}

export function issueRelations(all: IssueNode[], id: string): IssueRelations {
  const byId = new Map(all.map((n) => [n.id, n]))
  const self = byId.get(id)
  const rel: IssueRelations = { children: [], blockedBy: [], blocks: [], other: [] }
  if (!self) return rel
  if (self.parent && self.parent !== id) {
    rel.parentId = self.parent
    rel.parent = byId.get(self.parent)
  }
  rel.children = all.filter((n) => n.parent === id && n.id !== id)
  for (const d of self.deps ?? []) {
    const node = byId.get(d.id)
    if (d.type === 'blocks') rel.blockedBy.push({ id: d.id, node, open: !isClosed(node?.status) })
    else rel.other.push({ id: d.id, type: d.type, node })
  }
  // 反向：别人 deps 里 blocks 指向我 => 我阻塞了它
  for (const n of all) {
    if (n.id === id) continue
    for (const d of n.deps ?? []) {
      if (d.id === id && d.type === 'blocks') rel.blocks.push({ id: n.id, node: n, open: !isClosed(n.status) })
      else if (d.id === id && d.type !== 'blocks') rel.other.push({ id: n.id, type: `${d.type}（被）`, node: n })
    }
  }
  return rel
}
