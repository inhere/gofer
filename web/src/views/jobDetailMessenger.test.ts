import { describe, expect, it } from 'vitest'
import source from './JobDetail.vue?raw'

describe('job detail messenger metadata', () => {
  it('renders target session, original message, and channel', () => {
    expect(source).toContain('job.messenger')
    expect(source).toContain('目标会话')
    expect(source).toContain('消息原文')
    expect(source).toContain('传话通道')
  })
})
