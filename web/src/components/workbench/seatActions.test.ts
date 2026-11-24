// 名册行的动作（计划 T5）：接口通过 deps 传进来，这里只断言「点了以后取什么、写什么、复制什么」。
import { describe, expect, it } from 'vitest'
import type { SeatOpenPlan, SeatPatch, SeatView } from '../../api/types'
import { archiveSeat, bindSessionToSeat, continueInTerminal, loadSeatCandidates, saveSeatEdit } from './seatActions'

function seat(over: Partial<SeatView> = {}): SeatView {
  return {
    id: 's1',
    project_key: 'p',
    name: 'review',
    charter_by: 'user',
    kind: 'terminal',
    created_at: 1,
    updated_at: 2,
    state: 'running',
    ...over,
  }
}

const plan: SeatOpenPlan = {
  seat_id: 's1',
  kind: 'terminal',
  runner: 'w-docker-claude',
  cwd: '/work/repo',
  resumable: true,
  sh: 'claude --resume sid-1',
  powershell: '& claude --resume sid-1',
  new_sh: 'claude',
  new_powershell: 'claude',
}

describe('continue in a terminal', () => {
  it('copies the open plan command for the chosen shell and reports where it runs', async () => {
    const copied: string[] = []
    const got = await continueInTerminal(
      {
        fetchOpenPlan: async (id) => ({ ...plan, seat_id: id }),
        copy: async (text) => {
          copied.push(text)
          return true
        },
        shell: 'powershell',
      },
      's1',
    )
    expect(copied).toEqual(['& claude --resume sid-1'])
    expect(got).toMatchObject({ copied: true, runner: 'w-docker-claude', cwd: '/work/repo', kind: 'terminal' })
    expect(got.command).toMatchObject({ kind: 'resume', shell: 'powershell' })
  })

  it('copies the sh command on other platforms', async () => {
    const copied: string[] = []
    await continueInTerminal({ fetchOpenPlan: async () => plan, copy: async (t) => { copied.push(t); return true }, shell: 'sh' }, 's1')
    expect(copied).toEqual(['claude --resume sid-1'])
  })

  it('copies nothing and keeps the reason when there is no command', async () => {
    let called = false
    const got = await continueInTerminal(
      {
        fetchOpenPlan: async () => ({ ...plan, resumable: false, sh: '', powershell: '', new_sh: '', new_powershell: '', reason: 'needs_restart' }),
        copy: async () => { called = true; return true },
        shell: 'sh',
      },
      's1',
    )
    expect(called).toBe(false)
    expect(got.command).toMatchObject({ kind: 'none', reason: 'needs_restart' })
  })

  it('passes the acp thread through', async () => {
    const got = await continueInTerminal(
      { fetchOpenPlan: async () => ({ ...plan, kind: 'acp', thread_id: 's:sid-9', needs_restart: true }), copy: async () => true, shell: 'sh' },
      's1',
    )
    expect(got).toMatchObject({ kind: 'acp', threadID: 's:sid-9', needsRestart: true })
  })
})

describe('editing purpose and charter', () => {
  it('sends only what changed', async () => {
    const calls: Array<{ id: string; patch: SeatPatch }> = []
    const deps = { patchSeat: async (id: string, patch: SeatPatch) => { calls.push({ id, patch }); return seat() } }
    const target = seat({ purpose: '审 PR', charter: 'old' })
    expect(saveSeatEdit(deps, target, { purpose: '审 PR', charter: 'new' })).not.toBeNull()
    await Promise.resolve()
    expect(calls).toEqual([{ id: 's1', patch: { charter: 'new' } }])
  })

  it('sends nothing when nothing changed', async () => {
    let calls = 0
    const deps = { patchSeat: async () => { calls++; return seat() } }
    expect(saveSeatEdit(deps, seat({ purpose: 'x', charter: 'y' }), { purpose: ' x ', charter: 'y' })).toBeNull()
    expect(calls).toBe(0)
  })
})

describe('archiving', () => {
  it('patches archived', async () => {
    const calls: Array<{ id: string; patch: SeatPatch }> = []
    await archiveSeat({ patchSeat: async (id, patch) => { calls.push({ id, patch }); return seat({ archived_at: 5 }) } }, 's1')
    expect(calls).toEqual([{ id: 's1', patch: { archived: true } }])
  })
})

describe('making a session resident', () => {
  it('binds the session to the chosen seat', async () => {
    const calls: Array<{ id: string; ref: string; replace: boolean }> = []
    const got = await bindSessionToSeat(
      { bindSeat: async (id, ref, replace) => { calls.push({ id, ref, replace: replace ?? false }); return seat({ current_ref: ref }) } },
      'seat-1',
      'sid-1',
    )
    expect(calls).toEqual([{ id: 'seat-1', ref: 'sid-1', replace: false }])
    expect(got.current_ref).toBe('sid-1')
  })

  it('loads the candidates and skips a thread without a session id', async () => {
    const rows = [seat({ id: 'a', name: 'review' })]
    const got = await loadSeatCandidates({ seatCandidates: async () => rows }, 'sid-1')
    expect(got).toEqual(rows)
    let called = false
    expect(await loadSeatCandidates({ seatCandidates: async () => { called = true; return rows } }, '')).toEqual([])
    expect(called).toBe(false)
  })
})
