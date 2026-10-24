// 「改动」页签的懒加载与 409 说明（gofer-yb5x）：打开工作台不再拉 diff，点开页签才拉；
// 没有 git 基线（409）时在页签里说明原因，不冒泡成页面错误。
import { describe, expect, it } from 'vitest'

function raw(value: object): string {
  return Object.values(value)[0] as string
}

const threadView = raw(import.meta.glob('./ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))
const changesView = raw(import.meta.glob('./ThreadChangesView.vue', { eager: true, query: '?raw', import: 'default' }))
const layoutPane = raw(import.meta.glob('./LayoutPane.vue', { eager: true, query: '?raw', import: 'default' }))
const dict = raw(import.meta.glob('../../i18n/pages/workbench.ts', { eager: true, query: '?raw', import: 'default' }))

describe('workbench changes tab (gofer-yb5x)', () => {
  it('mounts the changes view only after the tab is opened', () => {
    // 挂载才请求（ThreadChangesView 的 onMounted 拉 diff）：不是打开工作台就拉。
    expect(threadView).toContain('const changesMounted = ref(')
    expect(threadView).toContain("v-if=\"changesMounted && thread.kind !== 'relay' && latestJobID\"")
    expect(threadView).toMatch(/function showChanges\(\)[\s\S]*changesMounted\.value = true/)
  })

  it('remembers the open tab per pane so switching sessions requests that session’s diff', () => {
    // ThreadView 按 thread 重建（:key），选择存在 pane 上，换会话时由 initial-view 恢复。
    expect(layoutPane).toContain("const view = ref<'process' | 'changes'>('process')")
    expect(layoutPane).toContain(':initial-view="view"')
    expect(layoutPane).toContain('@view="view = $event"')
    expect(threadView).toContain('initialView')
    expect(threadView).toContain("watch(activeView, (view) => emit('view', view))")
  })

  it('explains a 409 inside the tab instead of reporting a load failure', () => {
    expect(changesView).toContain("import { classifyDiffFailure } from '../../utils/workbenchDiff'")
    expect(changesView).toMatch(/if \(failure\.unavailable\) \{[\s\S]*unavailable\.value = failure\.reason/)
    // 说明走页签内的块，且不再 emit('error')（页面顶部红字 + 控制台都留给真正的失败）。
    expect(changesView).toContain('data-test="changes-unavailable"')
    expect(changesView).toContain("t('workbench.changes.unavailable')")
    const failBranch = changesView.slice(
      changesView.indexOf('const failure = classifyDiffFailure(cause)'),
      changesView.indexOf('function refresh()'),
    )
    expect(failBranch).toContain("emit('error'")
    expect(failBranch.indexOf('if (failure.unavailable)')).toBeLessThan(failBranch.indexOf("emit('error'"))
  })

  it('has the explanation text in both dictionaries', () => {
    expect(dict.match(/workbench\.changes\.unavailable'/g)?.length).toBe(2)
  })
})
