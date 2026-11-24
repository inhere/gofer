// 工作台「常驻会话」名册的纯逻辑（设计稿 2026-11-18-resident-sessions §4、计划 T5）：
// 状态点怎么从 seat 的 state +「等我」推出来、按项目分组且顺序稳定、回座位要敲哪条命令。
// 渲染在 SeatRoster.vue，取数在 SeatRosterPanel.vue；这里只放可穷举的单测逻辑。
import type { SeatCharterRow, SeatOpenPlan, SeatView, WorkbenchThread } from '../../api/types'

// SeatDot 是行上那个状态点。「等我」既可能来自 seat 自己的 waiting 字段（服务端算的），
// 也可能来自推送的「等我」快照（useWaiting，实时的那一份）——后者更及时，两者取并集。
//
// 接口的 state：none / running / awaiting / offline / ended / failed。failed 只看 acp 位置
// （job 以失败类收尾），终端会话结束一律是 ended。
export type SeatDot = 'waiting' | 'running' | 'awaiting' | 'offline' | 'ended' | 'failed' | 'none'

// seatWaitingKey 是座位在「等我」列表里的主体 key：终端座位是会话主体，acp 座位是 job 主体
// （与 internal/inbox/waiting.go 的 signalsOf 同一口径）。空位没有 key。
export function seatWaitingKey(seat: Pick<SeatView, 'kind' | 'current_ref'>): string {
  const ref = seat.current_ref ?? ''
  if (!ref) return ''
  return seat.kind === 'acp' ? `job:${ref}` : `session:${ref}`
}

// seatWaiting 是座位是否正等着你：服务端的 waiting 字段或「等我」快照里出现了它的 key。
export function seatWaiting(
  seat: Pick<SeatView, 'kind' | 'current_ref' | 'waiting'>,
  waitingKeys: ReadonlySet<string>,
): boolean {
  if (seat.waiting) return true
  const key = seatWaitingKey(seat)
  return key !== '' && waitingKeys.has(key)
}

// seatDot 把 state +「等我」映射成状态点。「等我」优先于 state：停在 awaiting 且在等你的座位
// 显示「等我」，不在等的 awaiting 只是「空闲」（会话停在输入上，但没到要人处理）。
export function seatDot(
  seat: Pick<SeatView, 'kind' | 'current_ref' | 'waiting' | 'state'>,
  waitingKeys: ReadonlySet<string> = new Set(),
): SeatDot {
  if (seatWaiting(seat, waitingKeys)) return 'waiting'
  switch (seat.state) {
    case 'running':
      return 'running'
    case 'awaiting':
      return 'awaiting'
    case 'offline':
      return 'offline'
    case 'ended':
      return 'ended'
    case 'failed':
      return 'failed'
    default:
      return 'none'
  }
}

export interface SeatProjectGroup {
  project_key: string
  seats: SeatView[]
}

// isSeatArchived：归档的座位默认不显示（接口用 include_archived=1 才给）。
export function isSeatArchived(seat: Pick<SeatView, 'archived_at'>): boolean {
  return (seat.archived_at ?? 0) > 0
}

// seatName 是排序 / 比较用的键：同一项目内名字唯一，跨项目可能重名，所以带上项目。
export function seatName(seat: Pick<SeatView, 'name'>): string {
  return (seat.name ?? '').trim().toLocaleLowerCase()
}

// groupSeats 按项目（工作区）分组。组内按名字，组间也按项目名 —— 顺序只由名字决定，
// 打开某个座位、它回了一句、状态变了都不会让它换位置（用户要求：顺序稳定）。
export function groupSeats(seats: readonly SeatView[], opts: { project?: string; archived?: boolean } = {}): SeatProjectGroup[] {
  const groups = new Map<string, SeatView[]>()
  for (const seat of seats) {
    if (isSeatArchived(seat) !== !!opts.archived) continue
    if (opts.project && seat.project_key !== opts.project) continue
    const bucket = groups.get(seat.project_key)
    if (bucket) bucket.push(seat)
    else groups.set(seat.project_key, [seat])
  }
  const out: SeatProjectGroup[] = []
  for (const [project_key, rows] of groups) {
    rows.sort((a, b) => compareSeatNames(a, b))
    out.push({ project_key, seats: rows })
  }
  out.sort((a, b) => a.project_key.localeCompare(b.project_key))
  return out
}

function compareSeatNames(a: SeatView, b: SeatView): number {
  const left = seatName(a)
  const right = seatName(b)
  if (left === right) return 0
  return left < right ? -1 : 1
}

// 会话线程 id 的前缀（internal/workbench 的 ParseThreadID）：s: agent 会话、j: 一次性 job、
// r: 终端中继会话。只有前两种带会话 id，能当 seat 的候选查询主体。
export function sessionIDOfThread(threadID: string): string {
  const value = (threadID ?? '').trim()
  if (value.startsWith('s:') || value.startsWith('r:')) return value.slice(2)
  return ''
}

export type SeatShell = 'sh' | 'powershell'

// detectShell 按浏览器平台选默认写法：Windows 给 PowerShell，其余给 sh（用户可以自己切换）。
export function detectShell(platform: string | undefined): SeatShell {
  return /win/i.test(platform ?? '') ? 'powershell' : 'sh'
}

export interface SeatCommand {
  // resume：续上原会话；new：位置空着或原会话续不上，新开一个；none：没有可用命令
  kind: 'resume' | 'new' | 'none'
  shell: SeatShell
  command: string
  // 不能续时的原因 key（vacant / unknown_agent / no_resume_template / no_project / needs_restart）
  reason: string
}

// seatCommand 取「在终端继续」要复制的命令：能续就用续接命令，否则退到新开命令；
// 两者都没有时 kind=none，界面显示 reason。alive（终端会话还在线）不影响取舍，由界面提示。
export function seatCommand(plan: SeatOpenPlan | null | undefined, shell: SeatShell): SeatCommand {
  if (!plan) return { kind: 'none', shell, command: '', reason: '' }
  const resume = shell === 'powershell' ? plan.powershell : plan.sh
  const fresh = shell === 'powershell' ? plan.new_powershell : plan.new_sh
  if (plan.resumable && resume) return { kind: 'resume', shell, command: resume, reason: '' }
  if (fresh) return { kind: 'new', shell, command: fresh, reason: plan.reason ?? '' }
  return { kind: 'none', shell, command: '', reason: plan.reason ?? '' }
}

// canOpenThread：名册行点开线程的入口。终端的线程由 seat 自己给（r:<sid>），acp 座位以
// open-plan 给的 thread_id 为准（会话重启过时 seat 行上的可能还没刷新）。
export function canOpenThread(seat: Pick<SeatView, 'current_ref' | 'thread_id'>): boolean {
  return !!seat.thread_id || !!seat.current_ref
}

// seatThreadID 是打开线程用的 id：优先 seat 给的 thread_id，缺了按终端座位自己拼。
export function seatThreadID(seat: Pick<SeatView, 'kind' | 'current_ref' | 'thread_id'>): string {
  if (seat.thread_id) return seat.thread_id
  if (seat.kind === 'terminal' && seat.current_ref) return `r:${seat.current_ref}`
  return ''
}

// seatHints 是行菜单里那几项的可用性：空位没有可打开的线程，也没有「在终端继续」之外的写操作；
// 归档总是可用（确认后 PATCH archived）。
export function seatMenuState(seat: SeatView): { openThread: boolean; continueTerminal: boolean; edit: boolean; archive: boolean } {
  return {
    openThread: canOpenThread(seat),
    continueTerminal: seat.kind === 'terminal' || seat.kind === 'acp',
    edit: true,
    archive: true,
  }
}

// seatPurposeOf / seatCharterLabel 之类的小展示 helper --------------------------------

// seatRefLabel 是「坐着的会话」那一行：终端座位给会话 id，acp 座位给 job id，空位给空串。
export function seatRefLabel(seat: Pick<SeatView, 'current_ref' | 'kind'>): string {
  const ref = seat.current_ref ?? ''
  if (!ref) return ''
  return ref.length > 14 ? `${ref.slice(0, 8)}…${ref.slice(-4)}` : ref
}

// seatCharterByLabel 由谁改的章程：user 是本人，session:<sid> 是从那个会话里改的（服务端记的）。
export function charterBySession(by: string | undefined): string {
  const value = (by ?? '').trim()
  return value.startsWith('session:') ? value.slice('session:'.length) : ''
}

// charterHistoryRows 把接口给的章程历史排成编辑弹窗里要显示的那些行：最近的在上（同秒按
// 行 id 倒序），preview 是一行折叠预览，body 是完整正文（展开时看）。
export interface SeatHistoryRow {
  id: number
  at: number
  by: string
  purpose: string
  body: string
  preview: string
}

export function charterHistoryRows(
  rows: readonly SeatCharterRow[] | undefined,
  max = 60,
): SeatHistoryRow[] {
  const out = (rows ?? []).map((r) => {
    const purpose = (r.purpose ?? '').trim()
    const body = (r.charter ?? '').trim()
    return { id: r.id, at: r.at, by: r.by ?? '', purpose, body, preview: foldText(body || purpose, max) }
  })
  out.sort((a, b) => b.at - a.at || b.id - a.id)
  return out
}

// foldText 把正文折成一行预览：换行与连续空白压成一个空格，超长用 … 截断（展开看全文）。
export function foldText(text: string, max = 60): string {
  const one = (text ?? '').replace(/\s+/g, ' ').trim()
  if (max <= 0 || one.length <= max) return one
  return `${one.slice(0, max - 1)}…`
}

// seatThreadMatches 判断工作台线程是不是某个座位的（会话行上用）。
export function seatOfThread(seats: readonly SeatView[], threadID: string): SeatView | undefined {
  if (!threadID) return undefined
  return seats.find((seat) => seatThreadID(seat) === threadID)
}

// 会话行上的「设为 X？」提示：只在当前打开的会话上出现，指向它的第一个候选座位。
export interface SeatHint {
  seatID: string
  name: string
}

export function seatHintOf(candidates: readonly SeatView[], dismissed: ReadonlySet<string>, threadID: string): SeatHint | null {
  if (threadID && dismissed.has(threadID)) return null
  for (const seat of candidates) {
    if (isSeatArchived(seat)) continue
    if (dismissed.has(seat.id)) continue
    return { seatID: seat.id, name: seat.name }
  }
  return null
}

// hintDismissalKey 是「关掉这个提示」记在本地的那一条：一次只关一个座位（同一座位以后不再提示）。
export const SEAT_HINT_DISMISSED_KEY = 'gofer.workbench.seatHintDismissed'

export function readDismissedSeats(storage: Pick<Storage, 'getItem'> | undefined): Set<string> {
  try {
    const raw = storage?.getItem(SEAT_HINT_DISMISSED_KEY) ?? ''
    return new Set(raw.split(',').map((s) => s.trim()).filter(Boolean))
  } catch {
    return new Set()
  }
}

export function rememberDismissedSeat(
  storage: Pick<Storage, 'getItem' | 'setItem'> | undefined,
  seatID: string,
): Set<string> {
  const set = readDismissedSeats(storage)
  set.add(seatID)
  try {
    storage?.setItem(SEAT_HINT_DISMISSED_KEY, [...set].join(','))
  } catch {
    // 没有本地存储时只在本页生效
  }
  return set
}

// isSessionThread 只看线程 id：job 线程（j: 前缀）不是会话，没有 seat 候选。
export function isSessionThread(thread: Pick<WorkbenchThread, 'id' | 'kind'>): boolean {
  if (sessionIDOfThread(thread.id)) return true
  return thread.kind === 'relay'
}
