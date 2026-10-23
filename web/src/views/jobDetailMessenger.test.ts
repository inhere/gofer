import { describe, expect, it } from 'vitest'
import source from './JobDetail.vue?raw'

describe('job detail messenger metadata', () => {
  it('renders target session, original message, and channel', () => {
    expect(source).toContain('job.messenger')
    // P3 起文案走 i18n（i18n/pages/jobs.ts 的 jobs.detail.*）。
    expect(source).toContain("t('jobs.detail.targetSession')")
    expect(source).toContain("t('jobs.detail.messageBody')")
    expect(source).toContain("t('jobs.detail.messengerChannel'")
  })
})
