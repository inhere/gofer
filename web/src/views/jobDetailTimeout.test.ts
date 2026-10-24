// 详情页「概览」的 duration 行要显示夹紧信息（gofer-ea2i）：请求值与封顶的配置键
// 不能只藏在 tooltip 里——tooltip 要悬停才看得到，调用方的诉求正是「一眼看到被截了」。
// 纯判定在 utils/jobTimeout.test.ts，这里只确认页面真的用了它、真的渲染出来了。
import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const jobDetail = raw(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const jobTypes = raw(import.meta.glob('../api/types.ts', { eager: true, query: '?raw', import: 'default' }))

describe('job detail: clamped timeout is visible', () => {
  it('imports and uses jobTimeoutClampNote with the persisted limit fields', () => {
    expect(jobDetail).toContain('jobTimeoutClampNote')
    expect(jobDetail).toContain('job.value?.requested_timeout_sec')
    expect(jobDetail).toContain('job.value?.timeout_limit_key')
    expect(jobDetail).toContain('job.value?.timeout_limit_source')
  })

  it('renders the note next to the effective timeout', () => {
    expect(jobDetail).toContain('title="timeoutTitle"')
    expect(jobDetail).toMatch(/v-if="timeoutClampText"[^>]*data-test="timeout-clamp"/)
  })

  it('declares the wire fields the note reads', () => {
    expect(jobTypes).toContain('timeout_clamped?: boolean')
    expect(jobTypes).toContain('timeout_limit_key?: string')
    expect(jobTypes).toContain('timeout_limit_source?: string')
  })
})
