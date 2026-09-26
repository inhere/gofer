import { describe, expect, it } from 'vitest'
import {
  addTab,
  closePane,
  focusDir,
  normalize,
  setRatio,
  splitPane,
  type LayoutDocument,
  type LayoutNode,
} from './layoutTree'

function pane(threadId: string | null): LayoutNode {
  return { kind: 'pane', thread_id: threadId }
}

function fourPaneTree(): LayoutNode {
  return {
    kind: 'split',
    dir: 'h',
    ratio: 0.5,
    a: {
      kind: 'split',
      dir: 'v',
      ratio: 0.5,
      a: pane('a-top'),
      b: pane('a-bottom'),
    },
    b: {
      kind: 'split',
      dir: 'v',
      ratio: 0.5,
      a: pane('b-top'),
      b: pane('b-bottom'),
    },
  }
}

describe('layoutTree', () => {
  it('splitPane creates a focused sibling without mutating the input', () => {
    const root = pane('first')
    const result = splitPane(root, '', 'h', 'second')

    expect(result.reason).toBeUndefined()
    expect(root).toEqual(pane('first'))
    expect(result.focused).toBe('b')
    expect(result.root).toEqual({
      kind: 'split',
      dir: 'h',
      ratio: 0.5,
      a: pane('first'),
      b: pane('second'),
    })
  })

  it('closePane promotes the sibling and remaps focus', () => {
    const root: LayoutNode = {
      kind: 'split',
      dir: 'h',
      ratio: 0.4,
      a: pane('left'),
      b: {
        kind: 'split',
        dir: 'v',
        ratio: 0.6,
        a: pane('top-right'),
        b: pane('bottom-right'),
      },
    }

    const result = closePane(root, 'b.a')
    expect(result.root).toEqual({
      kind: 'split',
      dir: 'h',
      ratio: 0.4,
      a: pane('left'),
      b: pane('bottom-right'),
    })
    expect(result.focused).toBe('b')
  })

  it('closePane turns the final pane into an empty leaf', () => {
    expect(closePane(pane('only'), '')).toEqual({
      root: pane(null),
      focused: '',
    })
  })

  it('focusDir chooses the nearest pane in all four geometric directions', () => {
    const root = fourPaneTree()
    expect(focusDir(root, 'a.a', 'right')).toBe('b.a')
    expect(focusDir(root, 'b.a', 'left')).toBe('a.a')
    expect(focusDir(root, 'a.a', 'down')).toBe('a.b')
    expect(focusDir(root, 'a.b', 'up')).toBe('a.a')
  })

  it('setRatio clamps split ratios to 0.1 through 0.9', () => {
    const root: LayoutNode = {
      kind: 'split',
      dir: 'h',
      ratio: 0.5,
      a: pane('left'),
      b: pane('right'),
    }
    expect(setRatio(root, '', -3)).toMatchObject({ ratio: 0.1 })
    expect(setRatio(root, '', 3)).toMatchObject({ ratio: 0.9 })
  })

  it('rejects a fifth pane and preserves the original tree', () => {
    const root = fourPaneTree()
    const result = splitPane(root, 'a.a', 'h', 'fifth')
    expect(result.reason).toBe('pane_limit')
    expect(result.root).toBe(root)
  })

  it('rejects a ninth tab and preserves the original document', () => {
    const document: LayoutDocument = {
      active_tab_id: 'tab-1',
      tabs: Array.from({ length: 8 }, (_, index) => ({
        id: `tab-${index + 1}`,
        title: `标签 ${index + 1}`,
        focused: '',
        root: pane(null),
      })),
    }
    const result = addTab(document)
    expect(result.reason).toBe('tab_limit')
    expect(result.document).toBe(document)
  })

  it('normalize repairs missing fields, out-of-range ratios, and empty splits', () => {
    const repaired = normalize({
      active_tab_id: 'missing',
      tabs: [
        {
          id: '',
          focused: 'nowhere',
          root: {
            kind: 'split',
            dir: 'h',
            ratio: 7,
            a: { kind: 'pane' },
            b: null,
          },
        },
        {
          id: '',
          title: 'empty split',
          root: { kind: 'split', dir: 'v', ratio: -2, a: null, b: null },
        },
      ],
    })

    expect(repaired.tabs).toHaveLength(2)
    expect(new Set(repaired.tabs.map((tab) => tab.id)).size).toBe(2)
    expect(repaired.tabs.some((tab) => tab.id === repaired.active_tab_id)).toBe(true)
    expect(repaired.tabs[0].root).toEqual(pane(null))
    expect(repaired.tabs[0].focused).toBe('')
    expect(repaired.tabs[1].root).toEqual(pane(null))

    const clamped = normalize({
      active_tab_id: 'ratio',
      tabs: [{
        id: 'ratio',
        title: 'ratio',
        focused: 'a',
        root: {
          kind: 'split',
          dir: 'v',
          ratio: 12,
          a: pane('top'),
          b: pane('bottom'),
        },
      }],
    })
    expect(clamped.tabs[0].root).toMatchObject({ kind: 'split', ratio: 0.9 })
  })
})
