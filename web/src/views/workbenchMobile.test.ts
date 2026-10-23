import { describe, expect, it } from 'vitest'

const sidebar = Object.values(import.meta.glob('../components/workbench/WorkbenchSidebar.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const composer = Object.values(import.meta.glob('../components/workbench/WorkbenchComposer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// 手机版工作台：会话列表按状态分组（需要你 / 活跃 / 空闲与离线），「已结束」默认折叠；
// 手机上 exec 开关与浮动发起按钮仍然隐藏（改由侧栏「＋」与抽屉承载）。
describe('Workbench mobile controls', () => {
  it('collapses the ended group and keeps the exec toggle off on phones', () => {
    expect(sidebar).toContain('threadGroupLabelKey(group)')
    expect(sidebar).toContain('<template v-if="group !== \'ended\' || endedOpen">')
    expect(sidebar).toContain(':aria-expanded="endedOpen"')
    expect(sidebar).toContain('.exec-toggle {\n    display: none;\n  }')
    expect(composer).toContain('.mobile-launch { display: none; }')
  })

  it('groups sessions by status instead of by project', () => {
    expect(sidebar).toContain("THREAD_GROUP_ORDER")
    expect(sidebar).toContain('threadGroupLabelKey(group)')
    expect(sidebar).not.toContain('project-row')
  })
})
