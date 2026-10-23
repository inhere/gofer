import { describe, expect, it } from 'vitest'
import {
  isMobileWidth,
  MOBILE_MAX_WIDTH,
  MOBILE_TOAST_DWELL_MS,
  mobileHeaderFallback,
  sessionHeaderSummary,
  toastDwellMs,
} from './mobileLayout'

describe('mobile layout helpers', () => {
  it('treats <= 900px as the phone breakpoint and never guesses without a width', () => {
    expect(isMobileWidth(400)).toBe(true)
    expect(isMobileWidth(MOBILE_MAX_WIDTH)).toBe(true)
    expect(isMobileWidth(MOBILE_MAX_WIDTH + 1)).toBe(false)
    expect(isMobileWidth(1280)).toBe(false)
    // 取不到宽度（0 / 负值）时不当作手机，免得把桌面也压成手机布局。
    expect(isMobileWidth(0)).toBe(false)
    expect(isMobileWidth(-1)).toBe(false)
  })

  it('only auto-dismisses the toast on the phone', () => {
    expect(toastDwellMs(true)).toBe(MOBILE_TOAST_DWELL_MS)
    expect(toastDwellMs(false)).toBe(0)
  })

  it('builds the collapsed session header as "topic · scope"', () => {
    expect(sessionHeaderSummary('修手机版工作台', ['web/src'])).toBe('修手机版工作台 · web/src')
    expect(sessionHeaderSummary('修手机版工作台', ['web/src', 'internal/job'])).toBe('修手机版工作台 · web/src internal/job')
    expect(sessionHeaderSummary('  只有主题  ', [])).toBe('只有主题')
    expect(sessionHeaderSummary('', ['web/src'])).toBe('web/src')
    expect(sessionHeaderSummary('', [])).toBe(mobileHeaderFallback())
    expect(sessionHeaderSummary('   ', ['  '])).toBe(mobileHeaderFallback())
  })
})
