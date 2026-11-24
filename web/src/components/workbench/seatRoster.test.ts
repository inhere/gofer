// 常驻会话名册（计划 T5）：状态点映射、分组与顺序稳定、回座位的命令、会话行上的「设为 X？」，
// 以及名册组件的渲染（SSR：这里没有 DOM 交互，点击类行为在 seatActions.test.ts 里测）。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { SeatOpenPlan, SeatView, WorkbenchThread } from '../../api/types'
import SeatRoster from './SeatRoster.vue'
import {
  canOpenThread,
  charterBySession,
  charterHistoryRows,
  detectShell,
  foldText,
  groupSeats,
  isSessionThread,
  readDismissedSeats,
  rememberDismissedSeat,
  seatCommand,
  seatDot,
  seatHintOf,
  seatRefLabel,
  seatThreadID,
  seatWaitingKey,
  sessionIDOfThread,
  SEAT_HINT_DISMISSED_KEY,
  type SeatDot,
} from './seatRoster'

function seat(over: Partial<SeatView> = {}): SeatView {
  return {
    id: over.id ?? 's1',
    project_key: 'p',
    name: over.name ?? 'review',
    charter_by: 'user',
    kind: 'terminal',
    created_at: 1,
    updated_at: 2,
    state: 'running',
    ...over,
  }
}

describe('seat waiting key', () => {
  it('uses the session subject for terminal seats and the job subject for acp seats', () => {
    expect(seatWaitingKey({ kind: 'terminal', current_ref: 'sid-1' })).toBe('session:sid-1')
    expect(seatWaitingKey({ kind: 'acp', current_ref: 'j-1' })).toBe('job:j-1')
    expect(seatWaitingKey({ kind: 'terminal', current_ref: '' })).toBe('')
  })
})

describe('seat status dot', () => {
  const states: SeatView['state'][] = ['none', 'running', 'awaiting', 'offline', 'ended', 'failed']

  it('maps every state to exactly one dot', () => {
    const got = Object.fromEntries(states.map((state) => [state, seatDot(seat({ state }), new Set())]))
    expect(got).toEqual({
      none: 'none',
      running: 'running',
      awaiting: 'awaiting',
      offline: 'offline',
      ended: 'ended',
      failed: 'failed',
    } satisfies Record<SeatView['state'], SeatDot>)
  })

  it('shows the waiting dot over any state when the seat is waiting for me', () => {
    for (const state of states) {
      // 服务端在 seat 上给的 waiting 字段
      expect(seatDot(seat({ state, waiting: 'answer' }), new Set())).toBe('waiting')
      // 推送的「等我」快照里有这个座位的 key（实时的那一份）
      expect(seatDot(seat({ state, current_ref: 'sid-1' }), new Set(['session:sid-1']))).toBe('waiting')
    }
  })

  it('ignores a waiting key that belongs to another session', () => {
    expect(seatDot(seat({ state: 'running', current_ref: 'sid-1' }), new Set(['session:sid-2']))).toBe('running')
    // 空位没有主体 key，别人的 waiting 不会让它亮
    expect(seatDot(seat({ state: 'none', current_ref: '' }), new Set(['session:sid-1']))).toBe('none')
  })
})

describe('seat groups', () => {
  const rows = [
    seat({ id: 'c', name: 'review', project_key: 'p2' }),
    seat({ id: 'b', name: 'Build', project_key: 'p1' }),
    seat({ id: 'a', name: 'api', project_key: 'p1' }),
    seat({ id: 'd', name: 'Docs', project_key: 'p2' }),
  ]

  it('groups by project and sorts both levels by name', () => {
    const got = groupSeats(rows).map((g) => [g.project_key, g.seats.map((s) => s.name)])
    expect(got).toEqual([
      ['p1', ['api', 'Build']],
      ['p2', ['Docs', 'review']],
    ])
  })

  it('keeps the order whatever order the seats arrive in and whoever is active', () => {
    const before = groupSeats(rows).map((g) => g.seats.map((s) => s.id).join(','))
    // 打开某一行 / 它回了一句 / 状态变了：只改状态，不改顺序
    const after = groupSeats(
      [...rows].reverse().map((s) => ({ ...s, state: 'awaiting' as const, waiting: 'answer' })),
    ).map((g) => g.seats.map((s) => s.id).join(','))
    expect(after).toEqual(before)
  })

  it('filters by project and leaves archived seats out unless asked for', () => {
    const withArchived = [...rows, seat({ id: 'z', name: 'old', project_key: 'p1', archived_at: 9 })]
    expect(groupSeats(withArchived).flatMap((g) => g.seats.map((s) => s.id))).not.toContain('z')
    expect(groupSeats(withArchived, { archived: true }).flatMap((g) => g.seats.map((s) => s.id))).toEqual(['z'])
    expect(groupSeats(rows, { project: 'p2' })).toHaveLength(1)
    expect(groupSeats([])).toEqual([])
  })
})

describe('opening a seat thread', () => {
  it('reads the session id out of a thread id', () => {
    expect(sessionIDOfThread('s:sid-1')).toBe('sid-1')
    expect(sessionIDOfThread('r:sid-2')).toBe('sid-2')
    expect(sessionIDOfThread('j:job-1')).toBe('')
    expect(sessionIDOfThread('')).toBe('')
  })

  it('tells session threads from one-shot job threads', () => {
    expect(isSessionThread({ id: 'r:sid-1', kind: 'relay' } as WorkbenchThread)).toBe(true)
    expect(isSessionThread({ id: 's:sid-1', kind: 'agent' } as WorkbenchThread)).toBe(true)
    expect(isSessionThread({ id: 'j:job-1', kind: 'job' } as WorkbenchThread)).toBe(false)
  })

  it('prefers the thread the seat reports and builds the terminal one otherwise', () => {
    expect(seatThreadID(seat({ thread_id: 's:x', current_ref: 'x' }))).toBe('s:x')
    expect(seatThreadID(seat({ kind: 'terminal', current_ref: 'sid-1' }))).toBe('r:sid-1')
    expect(canOpenThread(seat({ current_ref: '' }))).toBe(false)
  })

  it('shortens the sitting session id for the row', () => {
    expect(seatRefLabel({ current_ref: '', kind: 'terminal' })).toBe('')
    expect(seatRefLabel({ current_ref: 'abcdefghijklmnop', kind: 'terminal' })).toBe('abcdefgh…mnop')
    expect(seatRefLabel({ current_ref: 'short', kind: 'acp' })).toBe('short')
  })
})

describe('the command that goes back into a seat', () => {
  const plan: SeatOpenPlan = {
    seat_id: 's1',
    kind: 'terminal',
    runner: 'w-docker',
    cwd: '/work/repo',
    resumable: true,
    sh: "claude --resume sid-1",
    powershell: 'claude --resume sid-1',
    new_sh: 'claude',
    new_powershell: 'claude',
  }

  it('picks the shell for the browser platform', () => {
    expect(detectShell('Win32')).toBe('powershell')
    expect(detectShell('MacIntel')).toBe('sh')
    expect(detectShell(undefined)).toBe('sh')
  })

  it('resumes the session when it can, and falls back to a fresh session', () => {
    expect(seatCommand(plan, 'sh')).toMatchObject({ kind: 'resume', command: 'claude --resume sid-1' })
    expect(seatCommand({ ...plan, sh: 'gofer seat open review' }, 'sh').command).toBe('gofer seat open review')
    expect(seatCommand({ ...plan, resumable: false, reason: 'vacant' }, 'powershell')).toMatchObject({
      kind: 'new',
      command: 'claude',
      reason: 'vacant',
    })
  })

  it('reports the reason when there is no command at all', () => {
    expect(seatCommand({ ...plan, resumable: false, reason: 'unknown_agent', new_sh: '', new_powershell: '' }, 'sh')).toMatchObject({
      kind: 'none',
      command: '',
      reason: 'unknown_agent',
    })
    expect(seatCommand(null, 'sh')).toMatchObject({ kind: 'none', reason: '' })
  })
})

describe('charter by whom', () => {
  it('reads the writer out of charter_by', () => {
    expect(charterBySession('user')).toBe('')
    expect(charterBySession('session:sid-1')).toBe('sid-1')
    expect(charterBySession(undefined)).toBe('')
  })
})

describe('the “make it X?” hint on a session row', () => {
  it('takes the first candidate and can be dismissed for one seat', () => {
    const candidates = [seat({ id: 'a', name: 'review' }), seat({ id: 'b', name: 'build' })]
    expect(seatHintOf(candidates, new Set(), 'r:sid-1')).toEqual({ seatID: 'a', name: 'review' })
    expect(seatHintOf(candidates, new Set(['a']), 'r:sid-1')).toEqual({ seatID: 'b', name: 'build' })
    expect(seatHintOf(candidates, new Set(['a', 'b']), 'r:sid-1')).toBeNull()
    expect(seatHintOf(candidates, new Set(['r:sid-1']), 'r:sid-1')).toBeNull()
    expect(seatHintOf([], new Set(), 'r:sid-1')).toBeNull()
  })

  it('remembers a dismissal locally', () => {
    const store = new Map<string, string>()
    const storage = {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => { store.set(key, value) },
    }
    expect(readDismissedSeats(storage).size).toBe(0)
    rememberDismissedSeat(storage, 'a')
    rememberDismissedSeat(storage, 'b')
    expect([...readDismissedSeats(storage)]).toEqual(['a', 'b'])
    expect(store.get(SEAT_HINT_DISMISSED_KEY)).toBe('a,b')
    // 没有本地存储时只在本页生效，不抛
    expect(rememberDismissedSeat(undefined, 'c').has('c')).toBe(true)
  })
})

describe('the charter history of a seat', () => {
  const rows = [
    { id: 1, purpose: 'v1', charter: 'be careful', by: 'user', at: 100 },
    { id: 2, purpose: 'v2', charter: 'line one\n\n  line   two', by: 'session:sid-w', at: 200 },
    { id: 3, purpose: 'v3', charter: '', by: 'user', at: 200 },
  ]

  it('lists the newest revision first and folds each body to one line', () => {
    const got = charterHistoryRows(rows)
    expect(got.map((r) => r.id)).toEqual([3, 2, 1])
    expect(got[1]).toMatchObject({ by: 'session:sid-w', preview: 'line one line two', body: 'line one\n\n  line   two' })
    // 没有章程正文时用用途当预览，空的给空串（界面显示「（空）」）
    expect(got[0].preview).toBe('v3')
    expect(charterHistoryRows(undefined)).toEqual([])
  })

  it('truncates a long preview with an ellipsis', () => {
    expect(foldText('a'.repeat(10), 5)).toBe('aaaa…')
    expect(foldText('  short  ', 60)).toBe('short')
    expect(foldText('multi\nline')).toBe('multi line')
  })
})

describe('the roster component', () => {
  // 行上「打开线程」走 router.push；SSR 里装一个 memory router 就够（不实际导航）。
  function render(props: Record<string, unknown>): Promise<string> {
    const app = createSSRApp({
      render: () =>
        h(SeatRoster as never, {
          fetchOpenPlan: async () => ({}),
          patchSeat: async () => ({}),
          fetchHistory: async () => ({}),
          waitingKeys: [],
          seats: [],
          ...props,
        }),
    })
    app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/workbench', component: { render: () => null } }] }))
    return renderToString(app)
  }

  const seats = [
    seat({ id: 'a', name: 'api', project_key: 'p1', purpose: '审 PR', current_ref: 'sid-1' }),
    seat({ id: 'b', name: 'docs', project_key: 'p2', purpose: '写文档', current_ref: '' , state: 'none' }),
  ]

  it('renders one section per project with the seat rows in name order', async () => {
    const html = await render({ seats })
    expect(html).toContain('data-test="seat-roster"')
    expect(html).toContain('data-project="p1"')
    expect(html).toContain('data-project="p2"')
    expect(html).toContain('data-test="seat-row-api"')
    expect(html).toContain('data-test="seat-row-docs"')
    expect(html.indexOf('seat-row-api')).toBeLessThan(html.indexOf('seat-row-docs'))
    expect(html).toContain('审 PR')
    expect(html).toContain('写文档')
  })

  it('turns a row into 「等我」 when its key shows up in the waiting snapshot', async () => {
    expect(await render({ seats })).toContain('data-state="running"')
    const waiting = await render({ seats, waitingKeys: ['session:sid-1'] })
    expect(waiting).toContain('data-state="waiting"')
    expect(waiting).toContain('data-test="seat-row-api"')
  })

  it('shows a failed acp position as its own dot, apart from an ended one', async () => {
    const rows = [
      seat({ id: 'f', name: 'weekly', kind: 'acp', current_ref: 'job-1', state: 'failed' }),
      seat({ id: 'e', name: 'api', kind: 'acp', current_ref: 'job-2', state: 'ended' }),
    ]
    const html = await render({ seats: rows })
    expect(html).toContain('data-state="failed"')
    expect(html).toContain('data-state="ended"')
  })

  it('respects the project filter and shows nothing when every seat is filtered out', async () => {
    const html = await render({ seats, project: 'p2' })
    expect(html).not.toContain('data-project="p1"')
    expect(await render({ seats, project: 'nope' })).not.toContain('data-test="seat-roster"')
  })
})
