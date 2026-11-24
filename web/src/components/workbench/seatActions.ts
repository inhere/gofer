// 名册行的四个动作（计划 T5「名册」「设为常驻」）：在终端继续、编辑、归档、设为常驻。
//
// 每个动作都只做「取数据 / 写一次 / 把结果交回」，界面状态留在组件里；接口通过 deps 传入，
// 所以可以直接用假的 deps 测「点了以后调了什么、复制了什么」，不必挂一层 DOM。
import type { SeatOpenPlan, SeatPatch, SeatView } from '../../api/types'
import { seatCommand, type SeatCommand, type SeatShell } from './seatRoster'

export interface ContinueDeps {
  fetchOpenPlan: (seatID: string) => Promise<SeatOpenPlan>
  // copy 返回是否复制成功（非安全上下文里剪贴板不可用）。
  copy: (text: string) => Promise<boolean>
  shell: SeatShell
}

// ContinueResult 是「在终端继续」弹窗要显示的东西：命令本身、在哪台机器 / 哪个目录里敲，
// 会话是否还在线，以及不能续时的原因。
export interface ContinueResult {
  // plan 原样留着：换 sh / PowerShell 写法时不必再问一次服务端
  plan: SeatOpenPlan
  command: SeatCommand
  copied: boolean
  runner: string
  cwd: string
  alive: boolean
  kind: 'acp' | 'terminal'
  threadID: string
  needsRestart: boolean
}

// continueInTerminal 取 open-plan 并把命令复制到剪贴板；没有命令时什么也不复制。
export async function continueInTerminal(deps: ContinueDeps, seatID: string): Promise<ContinueResult> {
  const plan = await deps.fetchOpenPlan(seatID)
  const command = seatCommand(plan, deps.shell)
  let copied = false
  if (command.command) copied = await deps.copy(command.command)
  return {
    plan,
    command,
    copied,
    runner: plan.runner ?? '',
    cwd: plan.cwd ?? '',
    alive: !!plan.alive,
    kind: plan.kind === 'acp' ? 'acp' : 'terminal',
    threadID: plan.thread_id ?? '',
    needsRestart: !!plan.needs_restart,
  }
}

export interface PatchDeps {
  patchSeat: (seatID: string, patch: SeatPatch) => Promise<SeatView>
}

// saveSeatEdit 提交用途 / 章程；只带改过的字段，没改就不发请求。
export function saveSeatEdit(deps: PatchDeps, seat: SeatView, draft: { purpose: string; charter: string }): Promise<SeatView> | null {
  const patch: SeatPatch = {}
  const purpose = draft.purpose.trim()
  const charter = draft.charter.trim()
  if (purpose !== (seat.purpose ?? '').trim()) patch.purpose = purpose
  if (charter !== (seat.charter ?? '').trim()) patch.charter = charter
  if (Object.keys(patch).length === 0) return null
  return deps.patchSeat(seat.id, patch)
}

// archiveSeat 归档（服务端 PATCH archived=true：位置让出，名字重新可用，历史保留）。
export function archiveSeat(deps: PatchDeps, seatID: string): Promise<SeatView> {
  return deps.patchSeat(seatID, { archived: true })
}

export interface BindDeps {
  // 座位被别的会话占着时 replace=true 接管（人在界面上确认过）。
  bindSeat: (seatID: string, ref: string, replace?: boolean) => Promise<SeatView>
}

// bindSessionToSeat 把一个终端会话放进座位 ——「设为常驻」。
export function bindSessionToSeat(deps: BindDeps, seatID: string, sessionID: string, replace = false): Promise<SeatView> {
  return deps.bindSeat(seatID, sessionID, replace)
}

export interface CandidateDeps {
  seatCandidates: (sessionID: string) => Promise<SeatView[]>
}

// loadSeatCandidates 拉这个会话能坐的座位；服务端已把「座位目录装得下会话目录」「座位空着或
// 原会话结束超过 7 天」筛过，这里只丢掉没有会话 id 的情况。
export async function loadSeatCandidates(deps: CandidateDeps, sessionID: string): Promise<SeatView[]> {
  if (!sessionID) return []
  return deps.seatCandidates(sessionID)
}
