// 顶栏信封按钮（components/MailButton.vue）的计数查询与徽标文案（纯逻辑，utils/mail.ts）。
import { describe, expect, it } from 'vitest'
import { mailBadgeLabel, mailHeldQuery } from './mail'

describe('topbar mailbox badge', () => {
  it('asks /v1/mail for the held letters on both sides', () => {
    // 待指派是一个状态过滤：我发的信也可能没投出去，所以箱是 all。
    expect(mailHeldQuery()).toEqual({ box: 'all', state: ['held'], limit: 200 })
  })

  it('caps the badge label at 99+', () => {
    expect(mailBadgeLabel(1)).toBe('1')
    expect(mailBadgeLabel(99)).toBe('99')
    expect(mailBadgeLabel(100)).toBe('99+')
  })
})
