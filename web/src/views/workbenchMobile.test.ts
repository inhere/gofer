import { describe, expect, it } from 'vitest'

const sidebar = Object.values(import.meta.glob('../components/workbench/WorkbenchSidebar.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const composer = Object.values(import.meta.glob('../components/workbench/WorkbenchComposer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Workbench mobile controls', () => {
  it('groups ended sessions and removes the duplicate floating launcher', () => {
    expect(sidebar).toContain('已结束 ({{ endedThreads(project).length }})')
    expect(sidebar).toContain('.exec-toggle { display: none; }')
    expect(composer).toContain('.mobile-launch { display: none; }')
  })
})
