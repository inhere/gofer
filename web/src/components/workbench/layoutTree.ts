export type SplitDirection = 'h' | 'v'
export type FocusDirection = 'left' | 'right' | 'up' | 'down'
export type LayoutRejectReason = 'pane_limit' | 'tab_limit' | 'pane_not_found' | 'tab_not_found'

export const MAX_PANES_PER_TAB = 4
export const MAX_LAYOUT_TABS = 8

export interface PaneNode {
  kind: 'pane'
  thread_id: string | null
  [key: string]: unknown
}

export interface SplitNode {
  kind: 'split'
  dir: SplitDirection
  ratio: number
  a: LayoutNode
  b: LayoutNode
  [key: string]: unknown
}

export type LayoutNode = PaneNode | SplitNode

export interface LayoutTab {
  id: string
  title: string
  focused: string
  root: LayoutNode
  [key: string]: unknown
}

export interface LayoutDocument {
  active_tab_id: string
  tabs: LayoutTab[]
  [key: string]: unknown
}

export interface TreeMutation {
  root: LayoutNode
  focused: string
  reason?: LayoutRejectReason
}

export interface DocumentMutation {
  document: LayoutDocument
  reason?: LayoutRejectReason
}

interface Rect {
  x: number
  y: number
  width: number
  height: number
}

interface LeafGeometry {
  path: string
  x: number
  y: number
}

function emptyPane(threadId: string | null = null): PaneNode {
  return { kind: 'pane', thread_id: threadId }
}

function segments(path: string): string[] | null {
  if (path === '') return []
  const result = path.split('.')
  return result.every((part) => part === 'a' || part === 'b') ? result : null
}

function appendPath(base: string, side: 'a' | 'b'): string {
  return base === '' ? side : `${base}.${side}`
}

function combinePath(base: string, relative: string): string {
  if (base === '') return relative
  if (relative === '') return base
  return `${base}.${relative}`
}

export function nodeAt(root: LayoutNode, path: string): LayoutNode | undefined {
  const parts = segments(path)
  if (parts == null) return undefined
  let current: LayoutNode = root
  for (const side of parts) {
    if (current.kind !== 'split') return undefined
    current = side === 'a' ? current.a : current.b
  }
  return current
}

function replaceNode(root: LayoutNode, path: string, replacement: LayoutNode): LayoutNode | undefined {
  const parts = segments(path)
  if (parts == null) return undefined
  if (parts.length === 0) return replacement
  const [side, ...rest] = parts
  if (root.kind !== 'split') return undefined
  const child = replaceNode(side === 'a' ? root.a : root.b, rest.join('.'), replacement)
  if (!child) return undefined
  return side === 'a' ? { ...root, a: child } : { ...root, b: child }
}

export function panePaths(root: LayoutNode): string[] {
  const result: string[] = []
  const visit = (node: LayoutNode, path: string): void => {
    if (node.kind === 'pane') {
      result.push(path)
      return
    }
    visit(node.a, appendPath(path, 'a'))
    visit(node.b, appendPath(path, 'b'))
  }
  visit(root, '')
  return result
}

function firstPanePath(root: LayoutNode): string {
  return panePaths(root)[0] ?? ''
}

export function splitPane(
  root: LayoutNode,
  targetPath: string,
  dir: SplitDirection,
  threadId: string | null = null,
  position: 'before' | 'after' = 'after',
): TreeMutation {
  if (panePaths(root).length >= MAX_PANES_PER_TAB) {
    return { root, focused: targetPath, reason: 'pane_limit' }
  }
  const target = nodeAt(root, targetPath)
  if (!target || target.kind !== 'pane') {
    return { root, focused: firstPanePath(root), reason: 'pane_not_found' }
  }
  const created = emptyPane(threadId)
  const split: SplitNode = position === 'before'
    ? { kind: 'split', dir, ratio: 0.5, a: created, b: target }
    : { kind: 'split', dir, ratio: 0.5, a: target, b: created }
  const next = replaceNode(root, targetPath, split)
  if (!next) return { root, focused: firstPanePath(root), reason: 'pane_not_found' }
  return {
    root: next,
    focused: appendPath(targetPath, position === 'before' ? 'a' : 'b'),
  }
}

export function closePane(root: LayoutNode, targetPath: string): TreeMutation {
  const targetParts = segments(targetPath)
  const target = nodeAt(root, targetPath)
  if (targetParts == null || !target || target.kind !== 'pane') {
    return { root, focused: firstPanePath(root), reason: 'pane_not_found' }
  }
  if (targetParts.length === 0) {
    return { root: emptyPane(), focused: '' }
  }
  const side = targetParts[targetParts.length - 1] as 'a' | 'b'
  const parentPath = targetParts.slice(0, -1).join('.')
  const parent = nodeAt(root, parentPath)
  if (!parent || parent.kind !== 'split') {
    return { root, focused: firstPanePath(root), reason: 'pane_not_found' }
  }
  const sibling = parent[side === 'a' ? 'b' : 'a']
  const next = replaceNode(root, parentPath, sibling)
  if (!next) return { root, focused: firstPanePath(root), reason: 'pane_not_found' }
  return { root: next, focused: combinePath(parentPath, firstPanePath(sibling)) }
}

export function assignThread(root: LayoutNode, targetPath: string, threadId: string | null): LayoutNode {
  const target = nodeAt(root, targetPath)
  if (!target || target.kind !== 'pane') return root
  return replaceNode(root, targetPath, { ...target, thread_id: threadId }) ?? root
}

function clampedRatio(value: number): number {
  if (!Number.isFinite(value)) return 0.5
  return Math.min(0.9, Math.max(0.1, value))
}

export function setRatio(root: LayoutNode, splitPath: string, ratio: number): LayoutNode {
  const target = nodeAt(root, splitPath)
  if (!target || target.kind !== 'split') return root
  return replaceNode(root, splitPath, { ...target, ratio: clampedRatio(ratio) }) ?? root
}

function collectGeometry(node: LayoutNode, path: string, rect: Rect, result: LeafGeometry[]): void {
  if (node.kind === 'pane') {
    result.push({ path, x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 })
    return
  }
  if (node.dir === 'h') {
    const aWidth = rect.width * node.ratio
    collectGeometry(node.a, appendPath(path, 'a'), { ...rect, width: aWidth }, result)
    collectGeometry(node.b, appendPath(path, 'b'), {
      x: rect.x + aWidth,
      y: rect.y,
      width: rect.width - aWidth,
      height: rect.height,
    }, result)
    return
  }
  const aHeight = rect.height * node.ratio
  collectGeometry(node.a, appendPath(path, 'a'), { ...rect, height: aHeight }, result)
  collectGeometry(node.b, appendPath(path, 'b'), {
    x: rect.x,
    y: rect.y + aHeight,
    width: rect.width,
    height: rect.height - aHeight,
  }, result)
}

export function focusDir(root: LayoutNode, currentPath: string, direction: FocusDirection): string {
  const leaves: LeafGeometry[] = []
  collectGeometry(root, '', { x: 0, y: 0, width: 1, height: 1 }, leaves)
  const current = leaves.find((leaf) => leaf.path === currentPath)
  if (!current) return firstPanePath(root)
  const candidates = leaves.flatMap((leaf) => {
    if (leaf.path === current.path) return []
    const dx = leaf.x - current.x
    const dy = leaf.y - current.y
    let primary = 0
    let secondary = 0
    if (direction === 'left' && dx < 0) [primary, secondary] = [-dx, Math.abs(dy)]
    else if (direction === 'right' && dx > 0) [primary, secondary] = [dx, Math.abs(dy)]
    else if (direction === 'up' && dy < 0) [primary, secondary] = [-dy, Math.abs(dx)]
    else if (direction === 'down' && dy > 0) [primary, secondary] = [dy, Math.abs(dx)]
    else return []
    return [{ leaf, primary, secondary }]
  })
  candidates.sort((left, right) =>
    left.primary - right.primary ||
    left.secondary - right.secondary ||
    left.leaf.path.localeCompare(right.leaf.path),
  )
  return candidates[0]?.leaf.path ?? currentPath
}

function nextTabIdentity(tabs: readonly LayoutTab[]): { id: string; title: string } {
  const ids = new Set(tabs.map((tab) => tab.id))
  let index = 1
  while (ids.has(`tab-${index}`)) index++
  return { id: `tab-${index}`, title: `标签 ${index}` }
}

export function createLayoutDocument(threadId: string | null = null): LayoutDocument {
  return {
    active_tab_id: 'tab-1',
    tabs: [{ id: 'tab-1', title: '标签 1', focused: '', root: emptyPane(threadId) }],
  }
}

export function activeTab(document: LayoutDocument): LayoutTab {
  return document.tabs.find((tab) => tab.id === document.active_tab_id) ?? document.tabs[0]
}

export function addTab(document: LayoutDocument, threadId: string | null = null): DocumentMutation {
  if (document.tabs.length >= MAX_LAYOUT_TABS) return { document, reason: 'tab_limit' }
  const identity = nextTabIdentity(document.tabs)
  const tab: LayoutTab = { ...identity, focused: '', root: emptyPane(threadId) }
  return {
    document: { ...document, active_tab_id: tab.id, tabs: [...document.tabs, tab] },
  }
}

export function closeTab(document: LayoutDocument, tabId: string): DocumentMutation {
  const index = document.tabs.findIndex((tab) => tab.id === tabId)
  if (index < 0) return { document, reason: 'tab_not_found' }
  if (document.tabs.length === 1) {
    const only = document.tabs[0]
    return {
      document: {
        ...document,
        active_tab_id: only.id,
        tabs: [{ ...only, focused: '', root: emptyPane() }],
      },
    }
  }
  const tabs = document.tabs.filter((tab) => tab.id !== tabId)
  const activeId = document.active_tab_id === tabId
    ? tabs[Math.min(index, tabs.length - 1)].id
    : document.active_tab_id
  return { document: { ...document, active_tab_id: activeId, tabs } }
}

export function renameTab(document: LayoutDocument, tabId: string, title: string): DocumentMutation {
  if (!document.tabs.some((tab) => tab.id === tabId)) return { document, reason: 'tab_not_found' }
  const clean = title.trim()
  if (!clean) return { document }
  return {
    document: {
      ...document,
      tabs: document.tabs.map((tab) => tab.id === tabId ? { ...tab, title: clean } : tab),
    },
  }
}

export function activateTab(document: LayoutDocument, tabId: string): DocumentMutation {
  if (!document.tabs.some((tab) => tab.id === tabId)) return { document, reason: 'tab_not_found' }
  return { document: { ...document, active_tab_id: tabId } }
}

export function updateTab(
  document: LayoutDocument,
  tabId: string,
  root: LayoutNode,
  focused: string,
): DocumentMutation {
  if (!document.tabs.some((tab) => tab.id === tabId)) return { document, reason: 'tab_not_found' }
  const safeFocus = nodeAt(root, focused)?.kind === 'pane' ? focused : firstPanePath(root)
  return {
    document: {
      ...document,
      tabs: document.tabs.map((tab) => tab.id === tabId ? { ...tab, root, focused: safeFocus } : tab),
    },
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function normalizeNode(value: unknown, budget: { remaining: number }, depth: number): LayoutNode | null {
  if (!isRecord(value) || depth > 16) return null
  if (value.kind === 'pane') {
    if (budget.remaining <= 0) return null
    budget.remaining--
    return {
      ...value,
      kind: 'pane',
      thread_id: typeof value.thread_id === 'string' ? value.thread_id : null,
    }
  }
  if (value.kind !== 'split') return null
  const a = normalizeNode(value.a, budget, depth + 1)
  const b = normalizeNode(value.b, budget, depth + 1)
  if (!a && !b) return null
  if (!a) return b
  if (!b) return a
  return {
    ...value,
    kind: 'split',
    dir: value.dir === 'v' ? 'v' : 'h',
    ratio: clampedRatio(typeof value.ratio === 'number' ? value.ratio : 0.5),
    a,
    b,
  }
}

export function normalize(value: unknown, initialThreadId: string | null = null): LayoutDocument {
  if (!isRecord(value) || !Array.isArray(value.tabs) || value.tabs.length === 0) {
    return createLayoutDocument(initialThreadId)
  }
  const used = new Set<string>()
  const tabs: LayoutTab[] = []
  for (const rawValue of value.tabs.slice(0, MAX_LAYOUT_TABS)) {
    const raw = isRecord(rawValue) ? rawValue : {}
    let id = typeof raw.id === 'string' && raw.id.trim() ? raw.id.trim() : ''
    if (!id || used.has(id)) {
      id = nextTabIdentity(tabs).id
    }
    used.add(id)
    const budget = { remaining: MAX_PANES_PER_TAB }
    const root = normalizeNode(raw.root, budget, 0) ?? emptyPane()
    const requestedFocus = typeof raw.focused === 'string' ? raw.focused : ''
    const focused = nodeAt(root, requestedFocus)?.kind === 'pane'
      ? requestedFocus
      : firstPanePath(root)
    tabs.push({
      ...raw,
      id,
      title: typeof raw.title === 'string' && raw.title.trim()
        ? raw.title.trim()
        : `标签 ${tabs.length + 1}`,
      focused,
      root,
    })
  }
  if (tabs.length === 0) return createLayoutDocument(initialThreadId)
  const requestedActive = typeof value.active_tab_id === 'string' ? value.active_tab_id : ''
  return {
    ...value,
    active_tab_id: tabs.some((tab) => tab.id === requestedActive) ? requestedActive : tabs[0].id,
    tabs,
  }
}
