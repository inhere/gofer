// 验收 / 批准按钮的显示规则（utils/reviewGate.ts）：纯函数，跟 whoami 与 job 的提交者有关。
// 服务端才是权威（403 分三种），这里只保证界面不摆出注定被拒的按钮、并说清为什么。
import { describe, expect, it } from 'vitest'
import { canReviewJob } from './reviewGate'
import type { AuthWhoami } from './permissions'

function who(over: Partial<AuthWhoami> = {}): AuthWhoami {
  return {
    caller_id: 'me',
    kind: 'user',
    root: false,
    permissions: [{ name: 'job.review', source: 'default' }],
    grants_version: 1,
    ...over,
  }
}

const MY_JOB = { caller_id: 'me' }
const OTHER_JOB = { caller_id: 'someone-else' }

describe('canReviewJob', () => {
  it('有 job.review 就能审自己的交付', () => {
    expect(canReviewJob(who(), MY_JOB)).toEqual({ allowed: true, reason: '' })
  })

  it('审别人的 job 还要 job.review.others', () => {
    const gate = canReviewJob(who(), OTHER_JOB)
    expect(gate.allowed).toBe(false)
    expect(gate.reason).toContain('job.review.others')
    const both = who({
      permissions: [
        { name: 'job.review', source: 'default' },
        { name: 'job.review.others', source: 'grant' },
      ],
    })
    expect(canReviewJob(both, OTHER_JOB).allowed).toBe(true)
  })

  it('没有 job.review 就一律不摆', () => {
    const gate = canReviewJob(who({ permissions: [] }), MY_JOB)
    expect(gate.allowed).toBe(false)
    expect(gate.reason).toContain('job.review')
  })

  it('根管理员不受权限表限制', () => {
    expect(canReviewJob(who({ root: true, permissions: [] }), OTHER_JOB).allowed).toBe(true)
  })

  it('只读身份先于权限：给了 job.review 也不摆', () => {
    const gate = canReviewJob(
      who({ ceiling: { allow_exec: false, global_memory: false, read_only: true } }),
      MY_JOB,
    )
    expect(gate.allowed).toBe(false)
    expect(gate.reason).toContain('只读')
  })

  it('受限调用方不能审自己提交的 job（上限所有者是自己）', () => {
    const gate = canReviewJob(
      who({
        ceiling: { allow_exec: false, global_memory: false, read_only: false },
        permissions: [
          { name: 'job.review', source: 'default' },
          { name: 'job.review.others', source: 'grant' },
        ],
      }),
      MY_JOB,
    )
    expect(gate.allowed).toBe(false)
    expect(gate.reason).toContain('自己的 job')
    // 别人的 job 仍可审（有别人的交付要它签）
    expect(
      canReviewJob(
        who({
          ceiling: { allow_exec: false, global_memory: false, read_only: false },
          permissions: [
            { name: 'job.review', source: 'default' },
            { name: 'job.review.others', source: 'grant' },
          ],
        }),
        OTHER_JOB,
      ).allowed,
    ).toBe(true)
  })

  it('whoami 未知（旧 server / 网络）不隐藏，交给服务端 403', () => {
    expect(canReviewJob(null, OTHER_JOB)).toEqual({ allowed: true, reason: '' })
    expect(canReviewJob(undefined, undefined)).toEqual({ allowed: true, reason: '' })
  })

  it('非个人凭据（worker / job）不按权限表拦', () => {
    expect(canReviewJob(who({ kind: 'worker', permissions: [] }), OTHER_JOB).allowed).toBe(true)
  })
})
