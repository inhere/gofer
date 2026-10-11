import { describe, expect, it } from 'vitest'

const workbench = Object.values(import.meta.glob('./Workbench.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// gofer-k5d4：Sessions 页新建的 ACP 会话还没有 session_id 时跳 /workbench?job=<id>，
// 工作台要像发起栏新建会话一样等这个 job 出现并聚焦，而不是忽略参数。
describe('Workbench ?job= deep link', () => {
  it('registers the job as a launch intent on mount and when the query changes', () => {
    expect(workbench).toMatch(/function consumeRequestedJob\(\): void \{[\s\S]*?route\.query\.job[\s\S]*?delete next\.job[\s\S]*?submitted\(jobID\)/)
    expect(workbench).toMatch(/locateRequestedThread\(true\)\n\s*consumeRequestedJob\(\)\n\s*poller\.start\(\)/)
    expect(workbench).toMatch(/\(\) => route\.query\.job,\n\s*\(\) => \{\n\s*if \(layoutReady\.value\) consumeRequestedJob\(\)/)
  })
})
