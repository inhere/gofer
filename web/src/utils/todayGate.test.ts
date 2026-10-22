// 今天页卡片写操作的显示规则（gofer-darr）：只读身份一律灰显；有权限的照常可点；
// 服务端路由上 Require 的那几类（interaction.answer / memory.write / job.review）按权限灰显。
import { describe, expect, it } from 'vitest'
import { cardActionBlock, missingPermissionText } from './todayGate'
import { CEILING_READ_ONLY_NOTICE } from './ceiling'
import type { AuthWhoami } from './permissions'

function who(over: Partial<AuthWhoami>): AuthWhoami {
  return { caller_id: 'alice', kind: 'user', root: false, permissions: [], grants_version: 1, ...over }
}
const holds = (...names: string[]) => names.map((name) => ({ name, source: 'db' }))
const readOnlyWho = who({ ceiling: { allow_exec: false, global_memory: false, read_only: true } })

describe('read-only identity cannot act on today cards', () => {
  const kinds = ['interaction', 'review', 'approval', 'memory', 'relay', 'permission', 'work', 'plan_blocked'] as const
  it('every write action of every card kind is greyed with the read-only reason', () => {
    for (const kind of kinds) {
      for (const id of ['reply', 'answer', 'ack', 'accept', 'rerun', 'approve', 'reject', 'report', 'park', 'adopt', 'dismiss', 'resume']) {
        expect(cardActionBlock(readOnlyWho, { kind }, { id })).toBe(CEILING_READ_ONLY_NOTICE)
      }
    }
  })
  it('a root administrator with a read-only ceiling is still greyed (the ceiling narrows writes)', () => {
    expect(cardActionBlock(who({ root: true, ceiling: readOnlyWho.ceiling }), { kind: 'work' }, { id: 'park' })).toBe(
      CEILING_READ_ONLY_NOTICE,
    )
  })
})

describe('a writable identity sees the actions its permissions allow', () => {
  it('interaction cards need interaction.answer', () => {
    const plain = who({})
    expect(cardActionBlock(plain, { kind: 'interaction' }, { id: 'answer' })).toBe(
      missingPermissionText('interaction.answer'),
    )
    expect(cardActionBlock(who({ permissions: holds('interaction.answer') }), { kind: 'interaction' }, { id: 'answer' })).toBe('')
  })

  it('memory adoption needs memory.write, ignoring stays available', () => {
    const plain = who({})
    expect(cardActionBlock(plain, { kind: 'memory' }, { id: 'adopt' })).toBe(missingPermissionText('memory.write'))
    expect(cardActionBlock(plain, { kind: 'memory' }, { id: 'dismiss' })).toBe('')
    expect(cardActionBlock(who({ permissions: holds('memory.write') }), { kind: 'memory' }, { id: 'adopt' })).toBe('')
  })

  it('review and approval cards follow utils/reviewGate', () => {
    const plain = who({})
    expect(cardActionBlock(plain, { kind: 'review' }, { id: 'accept' })).toBe('没有审阅权限（job.review）')
    expect(cardActionBlock(plain, { kind: 'approval' }, { id: 'approve' })).toBe('没有审阅权限（job.review）')
    const reviewer = who({ permissions: holds('job.review') })
    expect(cardActionBlock(reviewer, { kind: 'review' }, { id: 'accept' })).toBe('')
    expect(cardActionBlock(reviewer, { kind: 'approval' }, { id: 'approve' })).toBe('')
    expect(cardActionBlock(who({ root: true }), { kind: 'approval' }, { id: 'reject' })).toBe('')
  })

  it('handler-decided cards (relay reply, work report/park) only depend on the read-only ceiling', () => {
    const plain = who({})
    expect(cardActionBlock(plain, { kind: 'relay' }, { id: 'reply' })).toBe('')
    expect(cardActionBlock(plain, { kind: 'work' }, { id: 'report' })).toBe('')
    expect(cardActionBlock(plain, { kind: 'work' }, { id: 'park' })).toBe('')
    expect(cardActionBlock(plain, { kind: 'plan_blocked' }, { id: 'resume' })).toBe('')
  })
})

describe('unknown identity hides nothing', () => {
  it('whoami not loaded yet, or a credential outside the permission subjects', () => {
    expect(cardActionBlock(null, { kind: 'interaction' }, { id: 'answer' })).toBe('')
    expect(cardActionBlock(undefined, { kind: 'review' }, { id: 'accept' })).toBe('')
    expect(cardActionBlock(who({ kind: 'worker' }), { kind: 'interaction' }, { id: 'answer' })).toBe('')
  })
})
