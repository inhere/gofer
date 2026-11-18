import { describe, expect, it } from 'vitest'

function raw(value: object): string {
  return Object.values(value)[0] as string
}

const sidebar = raw(import.meta.glob('./WorkbenchSidebar.vue', { eager: true, query: '?raw', import: 'default' }))
const types = raw(import.meta.glob('../../api/types.ts', { eager: true, query: '?raw', import: 'default' }))

// 工作台线程的「托管」标记（issue gofer-zc8m）：线程投影带 hosted，行上显示与 job 详情同款的徽标。
describe('workbench thread hosted badge', () => {
  it('carries hosted on the thread type next to the other session shape fields', () => {
    expect(types).toContain('  // hosted：常驻会话——不按空闲结束、无最大时长（job 详情的「托管」徽标同一个字段）。\n  hosted?: boolean')
  })

  it('shows the badge on the thread row only when the session is hosted', () => {
    expect(sidebar).toContain('v-if="thread.hosted"')
    expect(sidebar).toContain('data-test="thread-hosted-badge"')
    expect(sidebar).toContain("t('sessions.hosted')")
  })
})
