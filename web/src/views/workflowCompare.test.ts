import { describe, expect, it } from 'vitest'

const read = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const detail = read(import.meta.glob('./WorkflowDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const jobDetail = read(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const compare = read(import.meta.glob('../components/CompareColumns.vue', { eager: true, query: '?raw', import: 'default' }))
const dialog = read(import.meta.glob('../components/MergeDialog.vue', { eager: true, query: '?raw', import: 'default' }))

describe('Z4 compare view wiring', () => {
  it('renders fan-out steps through the compare columns and refetches after a pick', () => {
    expect(detail).toContain('<CompareColumns')
    expect(detail).toContain('v-if="group.fanned"')
    expect(detail).toContain('@changed="onCompareChanged"')
  })

  it('offers pick, side-by-side diff and a phone one-card-at-a-time layout', () => {
    expect(compare).toContain('选这个并合并')
    expect(compare).toContain('并排展开 diff')
    expect(compare).toContain('<UnifiedDiff')
    expect(compare).toContain('@media (max-width: 720px)')
    expect(compare).toContain('.col:not(.col--active)')
  })

  it('merges before recording the pick so a conflict leaves nothing half-done', () => {
    const merge = dialog.indexOf('mergeJobWorktree(')
    const pick = dialog.indexOf('pickWorkflowFan(')
    expect(merge).toBeGreaterThan(-1)
    expect(pick).toBeGreaterThan(merge)
    expect(dialog).toContain('冲突文件')
    expect(dialog).toContain('清理其余分支')
  })

  it('JobDetail has a merge-to-baseline button greyed out for remote runners', () => {
    expect(jobDetail).toContain('data-test="wt-merge-btn"')
    expect(jobDetail).toContain(':disabled="!wtMergeInfo.ok')
    expect(jobDetail).toContain('<MergeDialog')
  })
})
