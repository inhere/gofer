import { describe, expect, it } from 'vitest'

function raw(value: object): string {
  return Object.values(value)[0] as string
}

const drawer = raw(import.meta.glob('./WorkDrawer.vue', { eager: true, query: '?raw', import: 'default' }))

// 详情页流程区的接线（计划 2026-11-26 F5）：读哪个接口、三个动作调哪个接口、门禁没过怎么显示。
// 画出来的样子由 WorkStage.vue 的 SSR 用例覆盖，接口与参数由 api/stage.test.ts 覆盖。
describe('work drawer stage wiring', () => {
  it('reads the flow of the item together with the detail', () => {
    expect(drawer).toContain('getWorkStage(props.id)')
    // 没有流程是 404，只当「不显示」，不让详情报错
    expect(drawer).toContain('await getWorkStage(props.id).catch(() => null)')
  })

  it('renders the flow block only when the item has one', () => {
    expect(drawer).toContain('<WorkStage\n          v-if="stage"')
    expect(drawer).toContain(':stage="stage"')
    expect(drawer).toContain("@level=\"setStageLevel\"")
  })

  it('calls done and approve, and shows what the gate still wants instead of an error', () => {
    expect(drawer).toContain("kind === 'done' ? await stageDone(d.id) : await stageApprove(d.id)")
    expect(drawer).toContain('notice.value = stageDoneNotice(res.gate)')
  })

  it('sends the typed reason with the flow when sending it back', () => {
    expect(drawer).toContain('await stageBack(d.id, reason.trim())')
    // 空理由不发（服务端 400）
    expect(drawer).toContain('!reason.trim()')
  })

  it('switches the level and explains a refused approval', () => {
    expect(drawer).toContain('await stageSetLevel(d.id, level)')
    expect(drawer).toContain("e.status === 403")
    expect(drawer).toContain("t('work.stage.err.forbidden')")
  })

  it('labels the flow lines of the journal timeline', () => {
    expect(drawer).toContain("stage: 'work.d.kindStage'")
    expect(drawer).toContain("stage_action: 'work.d.kindStageAction'")
  })
})
