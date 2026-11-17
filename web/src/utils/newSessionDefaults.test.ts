import { describe, expect, it } from 'vitest'
import { sessionSubmitOptions } from './newSessionDefaults'

// 托管常驻会话（第 6 批）：新建会话默认托管，提交时带 hosted；不托管时不发该字段，
// 后端按默认空闲时限；pty 会话不带这个字段。
describe('sessionSubmitOptions hosted', () => {
  it('sends hosted for an ACP session that asked for it', () => {
    expect(sessionSubmitOptions({ type: 'acp', tier: 'auto', keepDirLock: false, hosted: true })).toEqual({ hosted: true })
  })

  it('omits hosted when it was unchecked', () => {
    expect(sessionSubmitOptions({ type: 'acp', tier: 'auto', keepDirLock: false, hosted: false })).toEqual({})
  })

  it('never sends hosted for a pty session', () => {
    expect(sessionSubmitOptions({ type: 'pty', tier: 'auto', keepDirLock: true, hosted: true })).toEqual({})
  })
})
