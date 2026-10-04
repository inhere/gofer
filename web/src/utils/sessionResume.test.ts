import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/client'
import { resumeConfirmText, resumeFailText, resumeLabel, resumeTitle } from './sessionResume'

describe('session wake-up helpers', () => {
  it('labels an ended session 唤醒 and a live one 接管', () => {
    expect(resumeLabel({ state: 'ended' })).toBe('唤醒')
    expect(resumeLabel({ state: 'idle' })).toBe('接管')
  })

  it('uses the server explanation as the hover title, with a fallback', () => {
    expect(resumeTitle({ state: 'ended', can_resume: false, resume_message: '项目没有开启交互终端' })).toBe('项目没有开启交互终端')
    expect(resumeTitle({ state: 'ended', can_resume: true })).toContain('已结束')
    expect(resumeTitle({ state: 'idle', can_resume: false, resume_reason: 'no_runner' })).toContain('no_runner')
  })

  it('prefers the Chinese detail of a refused resume', () => {
    expect(resumeFailText(new ApiError(409, 'resume failed: x - 中文原因', '中文原因', 'resume failed: x'))).toBe('中文原因')
    expect(resumeFailText(new Error('boom'))).toBe('boom')
  })

  it('asks for confirmation with the plan warning when there is one', () => {
    const text = resumeConfirmText({ resume_message: '将在 w1 上起新进程' }, { warning: '原终端可能仍开着' })
    expect(text).toContain('将在 w1 上起新进程')
    expect(text).toContain('原终端可能仍开着')
    expect(text.endsWith('继续吗？')).toBe(true)
  })
})
