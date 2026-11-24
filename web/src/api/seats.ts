// 常驻会话（seat）的接口（docs/reference/http-api.md「Seats」）：
// GET /v1/seats 列表（座位 + 由坐在里面的会话推出的 state / waiting / unread / thread_id）、
// GET/PATCH 单个、bind / unbind、open-plan（回座位要敲的命令）、history（绑定与章程历史），
// 以及会话的候选座位。
// CLI 侧的写操作（content 里的 charter 等）仍走各自接口，这里只做名册用到的部分。
import { request } from './client'
import type { SeatHistory, SeatOpenPlan, SeatPatch, SeatView } from './types'

interface SeatsResp {
  seats: SeatView[]
}

export interface SeatListOpts {
  project?: string
  includeArchived?: boolean
  // all=1：别人的座位一起列（需要 seat.manage.others，没有权限时服务端仍只给本人的）
  all?: boolean
}

export function listSeats(opts: SeatListOpts = {}): Promise<SeatView[]> {
  const q = new URLSearchParams()
  if (opts.project) q.set('project', opts.project)
  if (opts.includeArchived) q.set('include_archived', '1')
  if (opts.all) q.set('all', '1')
  const qs = q.toString()
  return request<SeatsResp>(`/v1/seats${qs ? `?${qs}` : ''}`).then((resp) => resp.seats ?? [])
}

export function getSeat(id: string): Promise<SeatView> {
  return request<SeatView>(`/v1/seats/${encodeURIComponent(id)}`)
}

export function patchSeat(id: string, patch: SeatPatch): Promise<SeatView> {
  return request<SeatView>(`/v1/seats/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  })
}

// bindSeat 把 ref（终端会话 id / acp 座位的 job id）放进座位；座位已被别的会话占着时
// 服务端 409，除非 replace 为真。
export function bindSeat(id: string, ref: string, replace = false): Promise<SeatView> {
  return request<SeatView>(`/v1/seats/${encodeURIComponent(id)}/bind`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ref, replace }),
  })
}

export function unbindSeat(id: string): Promise<SeatView> {
  return request<SeatView>(`/v1/seats/${encodeURIComponent(id)}/unbind`, { method: 'POST' })
}

export function seatOpenPlan(id: string): Promise<SeatOpenPlan> {
  return request<SeatOpenPlan>(`/v1/seats/${encodeURIComponent(id)}/open-plan`)
}

// seatHistory 是位置的绑定记录与章程每次修改（编辑弹窗里的历史）；权限与看单个位置相同。
export function seatHistory(id: string): Promise<SeatHistory> {
  return request<SeatHistory>(`/v1/seats/${encodeURIComponent(id)}/history`)
}

// seatCandidates 会先把会话读出来：未知会话 404，别人的会话要 seat.manage.others。
export function seatCandidates(sessionID: string): Promise<SeatView[]> {
  return request<SeatsResp>(`/v1/sessions/${encodeURIComponent(sessionID)}/seat-candidates`).then(
    (resp) => resp.seats ?? [],
  )
}
