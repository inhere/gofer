import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(
  ['../views/Work.vue', '../views/Board.vue', '../App.vue', '../ui/AppShell.vue', '../components/QuickCapture.vue', '../components/WorkDrawer.vue', '../components/SessionTopic.vue', '../components/today/DecisionCard.vue', '../api/client.ts', '../store/today.ts'],
  { eager: true, query: '?raw', import: 'default' },
) as Record<string, string>
const src = (n: string): string => {
  const k = Object.keys(mods).find((x) => x.endsWith(n))
  if (!k) throw new Error(`missing ${n}`)
  return mods[k]
}

describe('capture box wiring', () => {
  it('Work loads the capture box as its own lane, and the old ?tab=capture link still lands on it', () => {
    const w = src('Work.vue')
    // P6：草稿 / 收集箱不再是两个页签，而是平铺列表里的两条泳道（收集箱的 ?tab=capture 深链仍落到 captured）。
    expect(w).toContain('laneFromQuery(route.query)')
    expect(w).toContain('CAPTURE_LANE_STATUS')
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
    expect(src('AppShell.vue')).toContain('<QuickCapture />')
    expect(src('AppShell.vue')).not.toContain('<QuickCapture v-if=')
    const q = src('QuickCapture.vue')
    expect(q).toContain('captureWorkItem(built.req)')
    expect(q).toContain('data-test="quick-capture-link"')
    expect(q).toContain('CAPTURE_BOX_LINK')
    expect(src('client.ts')).toContain("'/v1/work-items/capture'")
  })
  it('shows the full body in detail, including after dispatch, and falls back for old records', () => {
    const drawer = src('WorkDrawer.vue')
    expect(drawer).toContain('isCaptured || detail.capture_text')
    expect(drawer).toContain('detail.capture_text || detail.title')
    // A textarea maxlength counts UTF-16 code units and silently clips pasted text.
    // Use the code-point validator instead; rejected text stays in the form.
    expect(src('QuickCapture.vue')).not.toContain(':maxlength=')
  })
  it('Today card navigates through actionLink; topic editor has a clear action', () => {
    expect(src('DecisionCard.vue')).toContain('actionLink(props.card, a)')
    const t = src('SessionTopic.vue')
    expect(t).toContain('data-test="topic-clear"')
    expect(t).toContain('buildClearTopicPatch()')
  })
})
