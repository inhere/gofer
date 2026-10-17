import { describe, expect, it } from 'vitest'
import type { Decision, Interaction, Job, PendingApproval } from '../api/types'
import { approvalFromJob, emptySeen, pendingAlert, pendingItems, type PendingLists } from './pendingAlert'

function approval(id: string, extra: Partial<PendingApproval> = {}): PendingApproval {
  return {
    id,
    title: `job ${id}`,
    project_key: 'self',
    waiting_since: 1_700_000_000,
    expires_at: 1_700_086_400,
    hold_cmd: ['git', 'push', 'origin', 'main'],
    ...extra,
  }
}

function decision(id: string, extra: Partial<Decision> = {}): Decision {
  return { id, title: id, question: 'q', state: 'OPEN', timeout_sec: 3600, asked_at: 1_700_000_000, ...extra }
}

function interaction(id: string, extra: Partial<Interaction> = {}): Interaction {
  return { id, job_id: 'job-1', type: 'confirmation', prompt: 'p', status: 'pending', created_at: 1_700_000_000, ...extra }
}

function lists(partial: Partial<PendingLists> = {}): PendingLists {
  return { interactions: [], decisions: [], approvals: [], ...partial }
}

// gofer-5wvc：待批准的 hold job 要在顶栏弹出提示（原来只触发 Today 刷新，没有提示条），
// 点击进 job 详情页——待批面板就在页首，批准就是在那儿按的。
describe('pendingAlert · 待批准 job', () => {
  it('新出现的待批 job 弹一条带命令的提示，并跳到它的批准区', () => {
    const r = pendingAlert(emptySeen(), null, lists({ approvals: [approval('h1')] }))
    expect(r.toast).toEqual({
      key: 'approval:h1',
      title: '⏸ job 等你批准 · self',
      text: 'job h1 — git push origin main',
      to: '/jobs/h1',
    })
    expect(r.seen.approvals.has('h1')).toBe(true)
  })

  it('同一条还在等就不重复弹（保留已经弹出的那条）', () => {
    const first = pendingAlert(emptySeen(), null, lists({ approvals: [approval('h1')] }))
    const again = pendingAlert(first.seen, first.toast, lists({ approvals: [approval('h1')] }))
    expect(again.toast).toEqual(first.toast)
  })

  it('批准 / 拒绝后计数减少，它代表的提示条一起收起', () => {
    const both = pendingAlert(emptySeen(), null, lists({ approvals: [approval('h1'), approval('h2')] }))
    expect(both.toast?.key).toBe('approval:h1')
    expect(both.seen.approvals.size).toBe(2)

    const after = pendingAlert(both.seen, both.toast, lists({ approvals: [approval('h2')] }))
    expect(after.seen.approvals.size).toBe(1)
    expect(after.toast).toBeNull()
  })

  it('决定过的 job 再回来（同 id 重新排队）会重新弹', () => {
    const first = pendingAlert(emptySeen(), null, lists({ approvals: [approval('h1')] }))
    const decided = pendingAlert(first.seen, first.toast, lists())
    expect(decided.toast).toBeNull()
    const again = pendingAlert(decided.seen, decided.toast, lists({ approvals: [approval('h1')] }))
    expect(again.toast?.key).toBe('approval:h1')
  })

  it('待批 job 比新决策请求先弹（job 停在那儿自己走不下去）', () => {
    const r = pendingAlert(emptySeen(), null, lists({ approvals: [approval('h1')], decisions: [decision('d1')] }))
    expect(r.toast?.key).toBe('approval:h1')
  })

  it('没有标题 / 命令的 job 退回 job id', () => {
    const r = pendingAlert(emptySeen(), null, lists({ approvals: [approval('h7', { hold_cmd: undefined, title: '' })] }))
    expect(r.toast?.text).toBe('h7')
  })
})

// 原来的两种提示（needs_human 交互 / 决策）行为不变。
describe('pendingAlert · 交互与决策', () => {
  it('needs_human 的交互弹一次，普通交互不弹', () => {
    const quiet = pendingAlert(emptySeen(), null, lists({ interactions: [interaction('i1')] }))
    expect(quiet.toast).toBeNull()
    const loud = pendingAlert(quiet.seen, quiet.toast, lists({ interactions: [interaction('i2', { needs_human: 1 })] }))
    expect(loud.toast?.title).toContain('needs_human')
    const again = pendingAlert(loud.seen, loud.toast, lists({ interactions: [interaction('i2', { needs_human: 1 })] }))
    expect(again.toast).toEqual(loud.toast)
  })

  it('已标「无需回复」的中继轮次不算待处理', () => {
    const acked = decision('d2', { kind: 'relay', session_id: 's1', acked_at: 1_700_000_100 })
    const r = pendingAlert(emptySeen(), null, lists({ decisions: [acked] }))
    expect(r.toast).toBeNull()
    expect(r.seen.decisions.size).toBe(0)
  })

  it('权限 / 中继 / 普通决策各一种提示条', () => {
    const permission = pendingAlert(emptySeen(), null, lists({ decisions: [decision('p1', { kind: 'permission', session_id: 's1' })] }))
    expect(permission.toast?.title).toContain('会话需要授权')
    const relay = pendingAlert(emptySeen(), null, lists({ decisions: [decision('r1', { kind: 'relay', session_id: 's1' })] }))
    expect(relay.toast?.title).toContain('会话等待回复')
    const plain = pendingAlert(emptySeen(), null, lists({ decisions: [decision('d3')] }))
    expect(plain.toast?.title).toContain('新的决策请求')
    expect(plain.toast?.to).toBeUndefined()
  })
})

describe('pendingAlert · 断线兜底的形状', () => {
  it('approvalFromJob 把 job 行折成与推送快照同形的待批准条目', () => {
    const job: Pick<Job, 'id' | 'project_key' | 'started_at' | 'title' | 'hold'> = {
      id: 'h9',
      project_key: 'self',
      started_at: 1_700_000_123,
      title: 'push it',
      hold: { timeout_sec: 3600, expires_at: 1_700_003_600, command: ['git', 'push'] },
    }
    expect(approvalFromJob(job)).toEqual({
      id: 'h9',
      title: 'push it',
      project_key: 'self',
      waiting_since: 1_700_000_123,
      expires_at: 1_700_003_600,
      hold_cmd: ['git', 'push'],
    })
  })

  it('pendingItems 列出所有等待中的条目（含不需要提示的普通交互）', () => {
    const items = pendingItems(
      lists({ interactions: [interaction('i1')], decisions: [decision('d1')], approvals: [approval('h1')] }),
    )
    expect(items.map((it) => it.key)).toEqual(['interaction:i1', 'decision:d1', 'approval:h1'])
  })
})
