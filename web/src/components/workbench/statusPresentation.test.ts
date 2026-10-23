import { describe, expect, it } from 'vitest'
import { setLocale } from '../../store/locale'
import { threadStatusPresentation } from './statusPresentation'

describe('threadStatusPresentation', () => {
  it('names a resident ACP session through the status dictionary', () => {
    setLocale('zh-CN')
    expect(threadStatusPresentation('awaiting_input')).toEqual({ label: '等待输入', family: 'need' })
  })

  it('keeps approval and blocked threads in the need family (yellow = needs a human)', () => {
    setLocale('zh-CN')
    expect(threadStatusPresentation('blocked')).toEqual({ label: '受阻', family: 'need' })
    expect(threadStatusPresentation('review')).toEqual({ label: '待验收', family: 'need' })
    expect(threadStatusPresentation('working').family).toBe('run')
    expect(threadStatusPresentation('idle').family).toBe('idle')
    expect(threadStatusPresentation('done').family).toBe('done')
  })

  it('translates the label with the current locale', () => {
    setLocale('en-US')
    expect(threadStatusPresentation('blocked').label).toBe('Blocked')
    expect(threadStatusPresentation('idle').label).toBe('Idle')
    setLocale('zh-CN')
  })
})
