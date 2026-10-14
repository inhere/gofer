import { describe, expect, it, vi } from 'vitest'
import {
  refusedOwnerHelp,
  refusedOwnerNotice,
  transferFailText,
  transferOwnerLabel,
  transferSessionOwner,
} from './sessionOwner'
import { ApiError } from '../api/client'
import type { AgentSession } from '../api/types'

// gofer-eh53：会话「归属被拒」的提示与转移动作。
const refused = { caller_id: 'default', refused_caller_id: 'agent-dev', refused_at: 1_760_000_000 }
const clean = { caller_id: 'default' }

describe('session owner refusal display', () => {
  it('names both callers when a report was refused', () => {
    expect(refusedOwnerNotice(refused)).toBe('hook 以 agent-dev 身份上报被拒（会话属于 default），状态已停更')
    expect(refusedOwnerHelp(refused)).toContain('转给 agent-dev')
    expect(transferOwnerLabel(refused)).toBe('转给 agent-dev')
  })

  it('renders nothing without a refusal (and survives a legacy server)', () => {
    expect(refusedOwnerNotice(clean)).toBe('')
    expect(transferOwnerLabel(clean)).toBe('')
    const legacy = {} as AgentSession
    expect(refusedOwnerNotice(legacy)).toBe('')
    expect(transferOwnerLabel(legacy)).toBe('')
  })

  it('says so when the session has no owner either', () => {
    expect(refusedOwnerNotice({ refused_caller_id: 'agent-dev' })).toContain('会话属于 （无）')
  })
})

describe('session owner transfer call', () => {
  it('posts the session id and the refused caller', async () => {
    const call = vi.fn(
      async (sid: string, callerId: string) =>
        ({ session_id: sid, caller_id: callerId }) as AgentSession,
    )
    const updated = await transferSessionOwner('sid-1', 'agent-dev', call)
    expect(call).toHaveBeenCalledWith('sid-1', 'agent-dev')
    expect(updated.caller_id).toBe('agent-dev')
  })

  it('surfaces the server reason on failure', async () => {
    const call = vi.fn(async () => {
      throw new ApiError(403, 'admin not permitted for this caller - caller lacks can_admin capability')
    })
    await expect(transferSessionOwner('sid-1', 'agent-dev', call)).rejects.toThrow(/can_admin/)
    try {
      await transferSessionOwner('sid-1', 'agent-dev', call)
    } catch (e) {
      expect(transferFailText(e)).toContain('can_admin')
    }
  })
})
