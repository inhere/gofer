// N3「今天」的共享状态：/today 页与全局「待我决策」浮层用同一份队列、同一个撤销窗口。
//
// 数据源 GET /v1/today；订阅推送主题 pending / jobs / work / sessions / plans，任何一个有
// 变化都防抖 1s 后重拉一次；断线超过 15s 由 createLiveTopic 回落 30s 兜底轮询。
import { computed, ref } from 'vue'
import type { Router } from 'vue-router'
import { withKeepalive } from '../api/client'
import { getToday, listTodayHandled, recordTodayAction, type TodayAction, type TodayCard, type TodayResponse } from '../api/today'
import { snoozeTodayCard, unsnoozeTodayCard } from '../api/today'
import { snoozeDoneLabel, type SnoozeOption } from '../utils/todaySnooze'
import { createLiveTopic, type LiveTopicHandle } from '../utils/useLiveTopic'
import {
  INCLUDE_EXEC_KEY,
  LAST_OPEN_KEY,
  TODAY_SEEN_SEC,
  UNDO_MS,
  createChordDetector,
  doneLabel,
  isTypingTarget,
  runCardAction,
  sendImmediately,
  settleHidden,
} from '../utils/today'
import { createUndoQueue } from '../utils/undoQueue'

export const TODAY_TOPICS = ['pending', 'jobs', 'work', 'sessions', 'plans'] as const
export const REFRESH_DEBOUNCE_MS = 1000

function readFlag(key: string): boolean {
  try {
    return globalThis.localStorage?.getItem(key) === '1'
  } catch {
    return false
  }
}

function writeValue(key: string, value: string): void {
  try {
    globalThis.localStorage?.setItem(key, value)
  } catch {
    // 隐私模式等：不记也能用
  }
}

export const todayData = ref<TodayResponse | null>(null)
export const todayError = ref('')
export const includeExec = ref(readFlag(INCLUDE_EXEC_KEY))
// 「自上次打开」水位：一次首页访问开始时读 localStorage 里上一次访问的开始时间；
// 访问结束（离开 / 切走）且看过足够久才写入本次的开始时间，见 beginTodayVisit / endTodayVisit。
export const todaySince = ref(0)
// 已操作（等撤销窗口、写请求在途或刚提交还没重拉）的卡。
export const hiddenKeys = ref<Set<string>>(new Set())
// 写操作已成功的卡 → 成功时已发起的刷新次数；之后发起的刷新若仍返回这张卡就重新显示（settleHidden）。
const committedKeys = new Map<string, number>()
let refreshSeq = 0
export const overlayOpen = ref(false)
export const handledOpen = ref(false)
export const snoozedOpen = ref(false)
export const actionError = ref('')
export const handledTodayCount = ref(0)

export const undoQueue = createUndoQueue(UNDO_MS)

export const visibleCards = computed<TodayCard[]>(() =>
  (todayData.value?.decisions ?? []).filter((c) => !hiddenKeys.value.has(c.key)),
)
export const decisionCount = computed(() => visibleCards.value.length)

export function setIncludeExec(on: boolean): void {
  includeExec.value = on
  writeValue(INCLUDE_EXEC_KEY, on ? '1' : '0')
  scheduleRefresh(0)
}

let visitStart = 0

// beginTodayVisit：首页变为可见（挂载 / 切回标签页）时调用——水位取上一次访问的开始时间
// （没有则 0 = 当天 0 点），并记下本次访问的开始时间。已在访问中则不变。
export function beginTodayVisit(nowSec = Math.floor(Date.now() / 1000)): number {
  if (visitStart > 0) return todaySince.value
  let prev = 0
  try {
    prev = Number(globalThis.localStorage?.getItem(LAST_OPEN_KEY) ?? 0) || 0
  } catch {
    prev = 0
  }
  todaySince.value = prev
  visitStart = nowSec
  return prev
}

// endTodayVisit：离开首页 / 页面隐藏 / 卸载时调用。可见满 TODAY_SEEN_SEC 才算看过，
// 这时才把水位推进到本次访问的开始时间；路过一下不改水位，下次仍从上一次访问算起。
export function endTodayVisit(nowSec = Math.floor(Date.now() / 1000)): void {
  if (visitStart > 0 && nowSec - visitStart >= TODAY_SEEN_SEC) writeValue(LAST_OPEN_KEY, String(visitStart))
  visitStart = 0
}

export async function refreshToday(): Promise<void> {
  const seq = ++refreshSeq
  try {
    const data = await getToday({ since: todaySince.value, includeExec: includeExec.value })
    const keys = new Set(data.decisions.map((c) => c.key))
    hiddenKeys.value = settleHidden(hiddenKeys.value, committedKeys, keys, undoQueue.current.value?.key, seq)
    todayData.value = data
    todayError.value = ''
  } catch (e) {
    todayError.value = e instanceof Error ? e.message : String(e)
  }
}

let refreshTimer: ReturnType<typeof setTimeout> | null = null

export function scheduleRefresh(delayMs = REFRESH_DEBOUNCE_MS): void {
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    refreshTimer = null
    void refreshToday()
  }, delayMs)
}

let handles: LiveTopicHandle[] = []

// 进入导航壳时启动（App 里与 workNeedsMe 同一处），重复调用无副作用。
export function startToday(): void {
  if (handles.length > 0) return
  handles = TODAY_TOPICS.map((topic, i) =>
    createLiveTopic(topic, {
      fetch: () => scheduleRefresh(),
      onSnap: () => scheduleRefresh(),
      initial: i === 0,
      debounceMs: REFRESH_DEBOUNCE_MS,
    }),
  )
  handles.forEach((h) => h.start())
}

export function stopToday(): void {
  handles.forEach((h) => h.stop())
  handles = []
  undoQueue.flush(true)
}

function hide(key: string): void {
  committedKeys.delete(key)
  hiddenKeys.value = new Set(hiddenKeys.value).add(key)
}

function unhide(key: string): void {
  committedKeys.delete(key)
  const next = new Set(hiddenKeys.value)
  next.delete(key)
  hiddenKeys.value = next
}

// actOnCard：卡片立刻收起；写操作进撤销窗口（会在 30 秒内超时的立即发送）；写成功后才记
// today.action 审计（页面卸载时两步都带 keepalive，审计在写成功后再发）并重拉——重拉若仍
// 返回这张卡就重新显示。失败则卡片回到队列并提示。
export function actOnCard(card: TodayCard, action: TodayAction, text = '', viaAdvice = false): void {
  actionError.value = ''
  hide(card.key)
  const run = runCardAction(card, action, text)
  const audit = {
    card_key: card.key,
    action_id: action.id === 'answer' ? `answer:${action.value ?? ''}` : action.id,
    advice_action_id: viaAdvice ? card.advice?.action_id : undefined,
    title: card.title,
    label: action.label,
    kind: card.kind,
  }
  const label = doneLabel(action, viaAdvice)
  const entry = {
    key: card.key,
    label,
    run: (keepalive: boolean) =>
      keepalive
        ? withKeepalive(run).then(() => withKeepalive(() => recordTodayAction(audit)).catch(() => null))
        : run().then(() => recordTodayAction(audit).catch(() => null)),
    onUndo: () => unhide(card.key),
    onDone: () => {
      handledTodayCount.value++
      if (hiddenKeys.value.has(card.key)) committedKeys.set(card.key, refreshSeq)
      scheduleRefresh()
    },
    onError: (e: unknown) => {
      unhide(card.key)
      actionError.value = `${card.title}：${e instanceof Error ? e.message : String(e)}`
    },
  }
  if (sendImmediately(card, Math.floor(Date.now() / 1000))) undoQueue.now(entry)
  else undoQueue.push(entry)
}

// snoozeCard：「稍后」和其他操作走同一个撤销窗口；到点才 POST /v1/today/snooze（服务端
// 顺带记 today.action 审计，这里不再另记）。不计入「今天处理了 N 张」。
export function snoozeCard(card: TodayCard, opt: SnoozeOption): void {
  actionError.value = ''
  hide(card.key)
  const body = { card_key: card.key, until_at: opt.until_at, until_job_id: opt.until_job_id }
  const entry = {
    key: card.key,
    label: snoozeDoneLabel(opt),
    run: (keepalive: boolean) => (keepalive ? withKeepalive(() => snoozeTodayCard(body)) : snoozeTodayCard(body)),
    onUndo: () => unhide(card.key),
    onDone: () => {
      if (hiddenKeys.value.has(card.key)) committedKeys.set(card.key, refreshSeq)
      scheduleRefresh(0)
    },
    onError: (e: unknown) => {
      unhide(card.key)
      actionError.value = `${card.title}：${e instanceof Error ? e.message : String(e)}`
    },
  }
  if (sendImmediately(card, Math.floor(Date.now() / 1000))) undoQueue.now(entry)
  else undoQueue.push(entry)
}

// unsnoozeCard：「放回队列」，立即生效。
export async function unsnoozeCard(cardKey: string): Promise<void> {
  await unsnoozeTodayCard(cardKey)
  await refreshToday()
}

export async function loadHandledToday(): Promise<void> {
  try {
    const r = await listTodayHandled(1)
    const d = new Date()
    const midnight = Math.floor(new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() / 1000)
    handledTodayCount.value = r.handled.filter((h) => h.at >= midnight && h.action_id !== 'snooze').length
  } catch {
    // 只是一行提示
  }
}

let installed = false

// 全局：页面卸载 / 换路由时立即提交撤销窗口里的操作；z 撤销；g d 打开浮层；Esc 关浮层。
export function installTodayGlobals(router: Router): void {
  if (installed || typeof window === 'undefined') return
  installed = true
  window.addEventListener('pagehide', () => undoQueue.flush(true))
  router.afterEach((to, from) => {
    if (to.path !== from.path) undoQueue.flush(false)
  })
  const chord = createChordDetector()
  window.addEventListener('keydown', (ev) => {
    if (ev.ctrlKey || ev.metaKey || ev.altKey) return
    const typing = isTypingTarget(ev.target)
    if (!typing && ev.key === 'z' && undoQueue.current.value) {
      ev.preventDefault()
      undoQueue.undo()
      return
    }
    if (ev.key === 'Escape' && (overlayOpen.value || handledOpen.value || snoozedOpen.value)) {
      overlayOpen.value = false
      handledOpen.value = false
      snoozedOpen.value = false
      return
    }
    if (chord(ev.key, typing)) {
      ev.preventDefault()
      overlayOpen.value = true
    }
  })
}
