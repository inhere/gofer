import { describe, expect, it } from 'vitest'
import source from './JobDetail.vue?raw'
import { detailIncludes } from '../utils/jobDetailInclude'

// 「消息」区（gofer-chd9）：job 详情页要能回答「我到底给 agent 发了什么」，不再只能去工作台看。
describe('job detail 消息 section', () => {
  it('asks the detail aggregation for the messages include', () => {
    // 没有这一项，详情页永远拿不到消息（服务端按 include 返回）。
    expect(detailIncludes({ session: true })).toContain('messages')
  })

  it('renders the sent messages, the injected-rules toggle and the agent replies', () => {
    expect(source).toContain('data-test="job-messages"')
    expect(source).toContain('发给 agent')
    expect(source).toContain('agent 回复')
    // P3 起文案走 i18n（i18n/pages/jobs.ts 的 jobs.detail.*）。
    expect(source).toContain("t('jobs.detail.viewInjected')")
    expect(source).toContain("t('jobs.detail.truncated')")
    expect(source).toContain("t('jobs.detail.earlierOmitted')")
    // 消息按服务端给的时间升序原样渲染：前端不重排（首条 prompt 必须在最前）。
    expect(source).toContain('v-for="(m, i) in messages"')
    // 消息区在会话输入框之前：先看对话，再发下一句。
    expect(source.indexOf('data-test="job-messages"')).toBeLessThan(
      source.indexOf('class="session-composer-card"'),
    )
  })

  it('only shows and requests messages for session jobs (gofer-h7hy)', () => {
    expect(source).toContain('showJobMessages(job) && messages.length > 0')
    expect(source).toContain('detailIncludes(job.value)')
    expect(source).not.toContain('DETAIL_INCLUDES')
  })

  it('keeps the aggregation result when the response carries it', () => {
    expect(source).toContain('if (msgs !== undefined) messages.value = msgs')
  })
})
