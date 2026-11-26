import { describe, expect, it } from 'vitest'

function raw(value: object): string {
  return Object.values(value)[0] as string
}

const threadView = raw(import.meta.glob('./ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))
const badge = raw(import.meta.glob('./StageBadge.vue', { eager: true, query: '?raw', import: 'default' }))

// 工作台线程头的流程小标签（计划 2026-11-26 F5）：标签本身的位置与数据来源。
// 「<步骤> n/N」的文案与跳转由 utils/stage 的 stageBadgeText / stageWorkItemLink 单测覆盖。
describe('workbench thread stage badge', () => {
  it('sits in the thread head and opens the work item', () => {
    expect(threadView).toContain('import StageBadge from')
    expect(threadView).toContain('<StageBadge v-if="thread" :thread-id="thread.id" />')
  })

  it('reads the stage from the session view of that one thread', () => {
    // 线程列表不带 stage：只给当前打开的这一条发一次会话视图请求，不按会话逐个拉
    expect(badge).toContain("getAgentSession(sid, { limit: 1 })")
    expect(badge).toContain('d.session?.stage')
    expect(badge).toContain('sessionIDOfThread')
  })

  it('shows the label as a link to the work item, with a blocked style', () => {
    expect(badge).toContain(':to="link"')
    expect(badge).toContain('data-test="thread-stage"')
    expect(badge).toContain("'stage-badge--blocked': blocked")
    expect(badge).toContain('stageBadgeText')
    expect(badge).toContain('stageWorkItemLink')
  })
})
