// 收件箱分栏的共享状态：/inbox 页与侧栏徽标用同一份数据。
//
// 数据源 GET /v1/inbox；订阅与 Today 相同的推送主题（pending / jobs / work / sessions / plans），
// 任何一个有变化都防抖后重拉；断线超过 15s 由 createLiveTopic 回落兜底轮询。
// 已读 / 清理写 POST /v1/inbox/state：成功后立刻改本地列表与计数，再重拉以服务端为准。
import { computed, ref } from 'vue'
import { ApiError } from '../api/client'
import { getInbox, postInboxState, type InboxColumn, type InboxResponse, type InboxStateAction } from '../api/inbox'
import { REFRESH_DEBOUNCE_MS, TODAY_TOPICS, includeExec, setIncludeExec } from './today'
import { applyStateLocally, inboxBadge, normalizeInbox, stateRequest } from '../utils/inbox'
import { createLiveTopic, type LiveTopicHandle } from '../utils/useLiveTopic'

export const inboxData = ref<InboxResponse | null>(null)
export const inboxError = ref('')
// server.inbox.enabled=false（404）或非 user 调用方（403）：功能不可用，不再轮询报错。
export const inboxUnavailable = ref(false)
// 「显示已清理」：开了才带 include_cleared=1。
export const showCleared = ref(false)
export const inboxBusy = ref(false)

// 侧栏徽标：只算「要处理」的未读（= 响应的 total_unread），仅知会不进。
export const inboxBadgeCount = computed(() => inboxBadge(inboxData.value))

let refreshSeq = 0

let recheckTimer: ReturnType<typeof setTimeout> | null = null

function scheduleRecheck(at: number | undefined): void {
  if (recheckTimer) clearTimeout(recheckTimer)
  recheckTimer = null
  if (!at || at <= 0) return
  const delay = Math.max(0, at * 1000 - Date.now())
  recheckTimer = setTimeout(() => {
    recheckTimer = null
    void refreshInbox()
  }, delay)
}

export async function refreshInbox(): Promise<void> {
  const seq = ++refreshSeq
  try {
    const data = await getInbox({ includeCleared: showCleared.value, includeExec: includeExec.value })
    // 更晚发起的请求（或写操作后的本地更新）已经生效时，丢弃过期响应。
    if (seq !== refreshSeq) return
    inboxData.value = normalizeInbox(data)
    scheduleRecheck(data.recheck_at)
    inboxError.value = ''
    inboxUnavailable.value = false
  } catch (e) {
    if (seq !== refreshSeq) return
    if (e instanceof ApiError && (e.status === 404 || e.status === 403)) {
      inboxUnavailable.value = true
      inboxData.value = null
    }
    inboxError.value = e instanceof Error ? e.message : String(e)
  }
}

let refreshTimer: ReturnType<typeof setTimeout> | null = null

export function scheduleInboxRefresh(delayMs = REFRESH_DEBOUNCE_MS): void {
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    refreshTimer = null
    void refreshInbox()
  }, delayMs)
}

export function setShowCleared(on: boolean): void {
  showCleared.value = on
  void refreshInbox()
}

// 「含 exec 待验收」与 Today 共用同一个偏好（design §3.8）：切换后两处都重拉。
export function setInboxIncludeExec(on: boolean): void {
  setIncludeExec(on)
  void refreshInbox()
}

let handles: LiveTopicHandle[] = []

// 进入导航壳时启动（App 里与 startToday 同一处），重复调用无副作用。
export function startInbox(): void {
  if (handles.length > 0) return
  handles = TODAY_TOPICS.map((topic, i) =>
    createLiveTopic(topic, {
      fetch: () => scheduleInboxRefresh(),
      onSnap: () => scheduleInboxRefresh(),
      initial: i === 0,
      debounceMs: REFRESH_DEBOUNCE_MS,
    }),
  )
  handles.forEach((h) => h.start())
}

export function stopInbox(): void {
  handles.forEach((h) => h.stop())
  handles = []
  if (recheckTimer) clearTimeout(recheckTimer)
  recheckTimer = null
}

// markInbox：写个人状态。keys 为空 = 按栏批量（带 column）。失败抛出，调用方提示。
export async function markInbox(
  action: InboxStateAction,
  opts: { keys?: string[]; column?: InboxColumn } = {},
): Promise<number> {
  inboxBusy.value = true
  try {
    const resp = await postInboxState(stateRequest(action, opts))
    if (inboxData.value) {
      // 本地先改，让列表与计数即时更新；之后的重拉以服务端为准。
      refreshSeq++
      inboxData.value = applyStateLocally(inboxData.value, action, { ...opts, showCleared: showCleared.value })
    }
    scheduleInboxRefresh(0)
    return resp.updated
  } finally {
    inboxBusy.value = false
  }
}
