import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(
  ['../views/Work.vue', '../views/Board.vue', '../App.vue', '../components/QuickCapture.vue', '../components/SessionTopic.vue', '../components/today/DecisionCard.vue', '../api/client.ts', '../store/today.ts'],
  { eager: true, query: '?raw', import: 'default' },
) as Record<string, string>
const src = (n: string): string => {
  const k = Object.keys(mods).find((x) => x.endsWith(n))
  if (!k) throw new Error(`missing ${n}`)
  return mods[k]
}

describe('capture box wiring', () => {
  it('Work has draft / capture tabs driven by ?tab and loads captured separately', () => {
    const w = src('Work.vue')
    expect(w).toContain('data-test="tab-drafts"')
    expect(w).toContain('data-test="tab-capture"')
    expect(w).toContain('triageTabFromQuery(route.query.tab)')
    expect(w).toContain('listWorkItems(boardListOpts(showClosed.value))')
    expect(w).toContain('listWorkItems(capturedListOpts())')
    expect(w).toContain('items.value = excludeCaptured(')
  })
  it('the jobs Board never touches captured work items', () => {
    const b = src('Board.vue')
    expect(b).not.toContain('captured')
    expect(b).not.toContain('listWorkItems')
  })
  it('the top bar mounts the quick capture which posts to the capture route', () => {
    expect(src('App.vue')).toContain('<QuickCapture />')
    const q = src('QuickCapture.vue')
    expect(q).toContain('captureWorkItem(built.req)')
    expect(q).toContain('data-test="quick-capture-link"')
    expect(q).toContain('CAPTURE_BOX_LINK')
    expect(src('client.ts')).toContain("'/v1/work-items/capture'")
  })
  it('Today card navigates through actionLink; topic editor has a clear action', () => {
    expect(src('DecisionCard.vue')).toContain('actionLink(props.card, a)')
    const t = src('SessionTopic.vue')
    expect(t).toContain('data-test="topic-clear"')
    expect(t).toContain('buildClearTopicPatch()')
  })
})
