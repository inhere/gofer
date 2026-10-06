import { describe, expect, it } from 'vitest'
import { buildIssueTree, issueRelations, type IssueNode } from './issueTree'

const n = (id: string, status = 'open', parent?: string, deps?: IssueNode['deps']): IssueNode => ({ id, title: `t-${id}`, status, parent, deps })
const ids = (xs: Array<{ id: string }>) => xs.map((x) => x.id)
const all = (nodes: IssueNode[]) => new Set(nodes.map((x) => x.id))

describe('buildIssueTree', () => {
  it('nests children under the parent with progress counts', () => {
    const nodes = [n('p'), n('c1', 'closed', 'p'), n('c2', 'open', 'p'), n('x')]
    const t = buildIssueTree(nodes, all(nodes))
    expect(ids(t)).toEqual(['p', 'c1', 'c2', 'x'])
    expect(t.map((e) => e.depth)).toEqual([0, 1, 1, 0])
    expect(t[0]).toMatchObject({ childTotal: 2, childClosed: 1, visibleChildren: 2, expanded: true })
  })

  it('supports multiple levels', () => {
    const nodes = [n('a'), n('b', 'open', 'a'), n('c', 'open', 'b')]
    expect(buildIssueTree(nodes, all(nodes)).map((e) => e.depth)).toEqual([0, 1, 2])
  })

  it('flattens children whose parent is filtered out and tags the parent', () => {
    const nodes = [n('p'), n('c', 'open', 'p')]
    const t = buildIssueTree(nodes, new Set(['c']))
    expect(t).toHaveLength(1)
    expect(t[0]).toMatchObject({ id: 'c', depth: 0, orphanParent: 'p' })
  })

  it('treats a parent that does not exist as a root without crashing', () => {
    const nodes = [n('c', 'open', 'ghost')]
    const t = buildIssueTree(nodes, all(nodes))
    expect(t[0]).toMatchObject({ id: 'c', depth: 0 })
    expect(t[0].orphanParent).toBeUndefined()
  })

  it('collapses closed parents by default and honours overrides', () => {
    const nodes = [n('p', 'closed'), n('c', 'closed', 'p')]
    expect(ids(buildIssueTree(nodes, all(nodes)))).toEqual(['p'])
    expect(ids(buildIssueTree(nodes, all(nodes), new Map([['p', true]])))).toEqual(['p', 'c'])
    const open = [n('p'), n('c', 'open', 'p')]
    expect(ids(buildIssueTree(open, all(open), new Map([['p', false]])))).toEqual(['p'])
  })

  it('survives parent cycles and self-parents without losing items', () => {
    const nodes = [n('a', 'open', 'b'), n('b', 'open', 'a'), n('s', 'open', 's')]
    const t = buildIssueTree(nodes, all(nodes))
    expect(ids(t).sort()).toEqual(['a', 'b', 's'])
  })

  it('flat mode ignores hierarchy', () => {
    const nodes = [n('p'), n('c', 'open', 'p')]
    const t = buildIssueTree(nodes, all(nodes), new Map(), true)
    expect(t.map((e) => e.depth)).toEqual([0, 0])
  })
})

describe('issueRelations', () => {
  it('collects parent, children, blocked-by / blocks and other deps', () => {
    const nodes = [
      n('p'),
      n('a', 'open', 'p', [{ id: 'b', type: 'blocks' }, { id: 'r', type: 'related' }]),
      n('b', 'closed'),
      n('c', 'open', 'a', [{ id: 'a', type: 'blocks' }, { id: 'zz', type: 'blocks' }]),
      n('r'),
    ]
    const rel = issueRelations(nodes, 'a')
    expect(rel.parent?.id).toBe('p')
    expect(ids(rel.children)).toEqual(['c'])
    expect(rel.blockedBy).toEqual([expect.objectContaining({ id: 'b', open: false })])
    expect(ids(rel.blocks)).toEqual(['c'])
    expect(rel.other).toEqual([expect.objectContaining({ id: 'r', type: 'related' })])
    const c = issueRelations(nodes, 'c')
    expect(c.blockedBy.map((b) => [b.id, b.open])).toEqual([['a', true], ['zz', true]])
  })

  it('keeps the id of a missing parent', () => {
    expect(issueRelations([n('c', 'open', 'ghost')], 'c')).toMatchObject({ parentId: 'ghost', parent: undefined })
  })
})
