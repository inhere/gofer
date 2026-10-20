import { describe, expect, it } from 'vitest'
import jobDetail from './JobDetail.vue?raw'
import threadView from '../components/workbench/ThreadView.vue?raw'

// 取消 job 的两个入口（job 详情页的「取消」、工作台的「停止」）都必须走 canCancelJob，
// 不许再各自枚举状态（gofer-83fe 的成因就是详情页只认 running）。
describe('job cancel entries', () => {
  it('job detail gates 取消 on canCancelJob', () => {
    expect(jobDetail).toContain("import { canCancelJob } from '../utils/jobCancel'")
    expect(jobDetail).toMatch(/const canCancel = computed\(\(\) => canCancelJob\(job\.value\?\.status\) && !readOnly\.value\)/)
    expect(jobDetail).toMatch(/v-if="showCancel"/)
  })

  it('the workbench stop button uses the same predicate', () => {
    expect(threadView).toContain("import { canCancelJob } from '../../utils/jobCancel'")
    expect(threadView).toMatch(/const canStop = computed\(\(\) => canCancelJob\(rawStatus\.value\)\)/)
    expect(threadView).toMatch(/:disabled="!canStop \|\| stopping"/)
  })
})
