// 收件箱分栏的纯函数（页面 views/Inbox.vue 与 store/inbox.ts 共用，单测直接覆盖）。
// 栏位由服务端判定（column），这里只做展示投影：分栏取条目、计数、请求体、本地即时更新。
import type { InboxColumn, InboxItem, InboxResponse, InboxStateAction, InboxStateRequest } from '../api/inbox'
import type { StatusFamily } from './statusFamily'

export const INBOX_COLUMNS: readonly InboxColumn[] = ['action', 'fyi']

export function emptyInbox(): InboxResponse {
  return { columns: { action: [], fyi: [] }, unread: { action: 0, fyi: 0 }, total_unread: 0, generated_at: 0 }
}

// normalizeInbox：容忍缺字段的响应（旧 server / 空栏）——栏位缺失按空列表算。
export function normalizeInbox(data: Partial<InboxResponse> | null | undefined): InboxResponse {
  const cols = data?.columns
  const action: InboxItem[] = cols?.action ?? []
  const fyi: InboxItem[] = cols?.fyi ?? []
  const out: InboxResponse = {
    columns: { action, fyi },
    unread: { action: countUnread(action), fyi: countUnread(fyi) },
    total_unread: 0,
    generated_at: data?.generated_at ?? 0,
  }
  out.total_unread = out.unread.action
  return out
}

export function countUnread(items: readonly InboxItem[]): number {
  return items.filter((it) => it.state === 'unread').length
}

// inboxBadge：顶栏 / 侧栏徽标——只算「要处理」栏的未读（仅知会不进徽标）。
export function inboxBadge(data: InboxResponse | null | undefined): number {
  const n = data?.total_unread
  return typeof n === 'number' && Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
}

// 本栏里已看过（seen）的条目 key：「清理已读」只清这些，不动未读。
export function seenKeys(items: readonly InboxItem[]): string[] {
  return items.filter((it) => it.state === 'seen').map((it) => it.key)
}

export function unreadKeys(items: readonly InboxItem[]): string[] {
  return items.filter((it) => it.state === 'unread').map((it) => it.key)
}

// stateRequest：POST /v1/inbox/state 的请求体。keys 为空就不带（= 按栏批量）；批量总带 column。
export function stateRequest(action: InboxStateAction, opts: { keys?: string[]; column?: InboxColumn } = {}): InboxStateRequest {
  const body: InboxStateRequest = { action }
  if (opts.keys && opts.keys.length > 0) body.keys = [...opts.keys]
  if (opts.column) body.column = opts.column
  return body
}

// applyStateLocally：写成功后立刻改本地列表与计数（随后的重拉会以服务端为准）。
// 与服务端一致：clear 的条目在不显示已清理时从列表里消失，显示时标 cleared；
// seen 不降级 cleared 的条目；restore 把 cleared 变回 seen。
export function applyStateLocally(
  data: InboxResponse,
  action: InboxStateAction,
  opts: { keys?: string[]; column?: InboxColumn; showCleared?: boolean } = {},
): InboxResponse {
  const keys = opts.keys && opts.keys.length > 0 ? new Set(opts.keys) : null
  const hit = (it: InboxItem): boolean => (keys ? keys.has(it.key) : !opts.column || it.column === opts.column)
  const next: InboxResponse = { ...data, columns: { action: [], fyi: [] }, unread: { action: 0, fyi: 0 }, total_unread: 0 }
  for (const col of INBOX_COLUMNS) {
    const out: InboxItem[] = []
    for (const it of data.columns[col] ?? []) {
      let cur = it
      if (hit(it)) {
        if (action === 'seen' && it.state === 'unread') cur = { ...it, state: 'seen' }
        else if (action === 'clear' && it.state !== 'cleared') {
          if (!opts.showCleared) continue
          cur = { ...it, state: 'cleared' }
        } else if (action === 'restore' && it.state === 'cleared') cur = { ...it, state: 'seen' }
      }
      out.push(cur)
    }
    next.columns[col] = out
    next.unread[col] = countUnread(out)
  }
  next.total_unread = next.unread.action
  return next
}

// itemFamily：条目标签的状态族——失败待决定 = fail，仅知会 = idle（不抢注意力），其余要处理 = need。
export function itemFamily(item: Pick<InboxItem, 'kind' | 'column'>): StatusFamily {
  if (item.kind === 'job.failed') return 'fail'
  if (item.column === 'fyi') return 'idle'
  return 'need'
}

// 已知的条目类型（i18n：inbox.kind.<kind>）；不认识的回落 inbox.kind.other。
export const INBOX_KINDS: readonly string[] = [
  'interaction',
  'decision',
  'relay',
  'permission',
  'review',
  'approval',
  'work',
  'suggestion',
  'merge',
  'plan_blocked',
  'memory',
  'capture',
  'mail_held',
  'worker_enroll',
  'job.terminal',
  'job.failed',
]

export function kindLabelKey(kind: string): string {
  return INBOX_KINDS.includes(kind) ? `inbox.kind.${kind}` : 'inbox.kind.other'
}

// itemLink：「打开」的站内目标。优先服务端的 deep_link（只收站内路径），否则按 refs / kind 推；没有则空串。
export function itemLink(item: Pick<InboxItem, 'kind' | 'deep_link' | 'refs'>): string {
  const dl = item.deep_link ?? ''
  if (dl.startsWith('/') && !dl.startsWith('//')) return dl
  const refs = item.refs ?? {}
  if (item.kind === 'mail_held') return '/mail?state=held'
  if (item.kind === 'capture') return '/work?tab=capture'
  if (item.kind === 'worker_enroll') return '/runners#enrollments'
  if (refs.plan_id) return `/plans/${encodeURIComponent(refs.plan_id)}`
  if (refs.job_id) return `/jobs/${encodeURIComponent(refs.job_id)}`
  if (refs.work_item_id) return '/work'
  if (refs.session_id) return '/sessions'
  return ''
}
