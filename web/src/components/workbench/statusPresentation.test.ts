import { describe, expect, it } from 'vitest'
import { threadStatusPresentation } from './statusPresentation'

describe('threadStatusPresentation', () => {
  it('shows a neutral waiting label for a resident ACP session', () => {
    expect(threadStatusPresentation('awaiting_input')).toEqual({ label: '等待输入', tone: 'neutral' })
  })

  it('keeps approval and blocked threads conspicuous', () => {
    expect(threadStatusPresentation('blocked')).toEqual({ label: 'blocked', tone: 'attention' })
    expect(threadStatusPresentation('review')).toEqual({ label: 'review', tone: 'attention' })
  })
})
