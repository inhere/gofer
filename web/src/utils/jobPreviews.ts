// JobDetail「预览」区的逻辑：纯函数 + 一个不碰 DOM 的控制器（接口与「打开新标签页」由调用方注入，
// 测试里换成假的）。组件在 components/JobPreviews.vue。
//
// 安全约束（设计稿 §3.6 / §5 Web）：
//  - 创建响应的 url 是一次性 bearer URL，只放在内存里（openUrls），不写任何 storage；
//  - 打开 = 用新标签页访问这个 url（noopener），不经 request()，所以不会被加 Bearer；
//  - 用过一次就丢掉（token 只能换一次 cookie，再开只会 404）；
//  - 界面上只显示 host:port 的截断形式，不显示 token。
import { ref, type Ref } from 'vue'
import { ApiError } from '../api/client'
import {
  createJobPreview,
  listJobPreviews,
  renewJobPreview,
  revokeJobPreview,
  type JobPreview,
} from '../api/preview'
import type { StatusFamily } from './statusFamily'

// TTL 可选项（分钟）。服务端会把超过 server.preview.max_ttl_sec 的值截到上限。
export const PREVIEW_TTL_MINUTES: readonly number[] = [15, 30, 60, 240, 480]
export const PREVIEW_DEFAULT_TTL_MINUTES = 30

// 能创建预览的 job 状态（与服务端 previewRunning 一致）。
const RUNNING_STATES = new Set(['queued', 'running', 'awaiting_input', 'waiting_input', 'recovering'])

export function previewJobRunning(status: string | undefined | null): boolean {
  return !!status && RUNNING_STATES.has(status.toLowerCase())
}

// 端口输入 → 1..65535 的整数；其余返回 null。
export function parsePreviewPort(raw: string | number): number | null {
  const s = String(raw).trim()
  if (!/^\d{1,5}$/.test(s)) return null
  const n = Number(s)
  return n >= 1 && n <= 65535 ? n : null
}

// host:port 的截断显示：过长时中间用 … 省略，保留头尾（端口一定留着）。
export function previewHostLabel(hostname: string, port: number, max = 28): string {
  const full = `${hostname}:${port}`
  if (full.length <= max) return full
  const tail = `:${port}`
  const room = Math.max(4, max - tail.length - 1)
  const head = Math.ceil(room / 2)
  const rest = room - head
  return `${hostname.slice(0, head)}…${rest > 0 ? hostname.slice(-rest) : ''}${tail}`
}

// 创建响应的 url → 用于显示的 host:port（丢掉路径、query 里的 token）。解析不了返回空串。
export function previewUrlHostLabel(url: string, max = 28): string {
  try {
    const u = new URL(url)
    return previewHostLabel(u.hostname, Number(u.port) || (u.protocol === 'https:' ? 443 : 80), max)
  } catch {
    return ''
  }
}

// 租约状态 → 状态族（StatusPill）：active 在跑，expired 超时，其余已结束的归 idle。
export function previewStateFamily(state: string | undefined): StatusFamily {
  switch ((state ?? '').toLowerCase()) {
    case 'active':
      return 'run'
    case 'expired':
      return 'time'
    default:
      return 'idle'
  }
}

export function previewActive(p: JobPreview): boolean {
  return (p.state ?? '').toLowerCase() === 'active'
}

// 接口错误的展示文案：ApiError.detail 已经按 err.<code> 本地化（api/client.ts raiseForStatus），
// 没有 detail 时退回 message；非 Error 值转字符串。
export function previewErrorText(e: unknown): string {
  if (e instanceof ApiError) return e.detail || e.message
  return e instanceof Error ? e.message : String(e)
}

export interface PreviewApi {
  list: (jobId: string) => Promise<JobPreview[]>
  create: (jobId: string, body: { port: number; ttl_sec?: number }) => Promise<JobPreview>
  revoke: (jobId: string, previewId: string) => Promise<JobPreview>
  renew: (jobId: string, previewId: string, ttlSec: number) => Promise<JobPreview>
}

export const defaultPreviewApi: PreviewApi = {
  list: listJobPreviews,
  create: createJobPreview,
  revoke: revokeJobPreview,
  renew: renewJobPreview,
}

// 新标签页打开一次性 URL。noopener：预览应用拿不到控制台窗口；noreferrer：Referer 里不带控制台地址。
export function openInNewTab(url: string): void {
  window.open(url, '_blank', 'noopener,noreferrer')
}

export type PreviewNotice = 'created' | 'renewed' | 'revoked'

export interface PreviewController {
  items: Ref<JobPreview[]>
  loading: Ref<boolean>
  creating: Ref<boolean>
  error: Ref<string>
  notice: Ref<PreviewNotice | ''>
  busy: Ref<Set<string>>
  // 正在二次确认撤销的预览 id
  pendingRevoke: Ref<string>
  // 预览 id → 创建时拿到的一次性 URL（仅内存）
  openUrls: Ref<Record<string, string>>
  load: () => Promise<void>
  create: (port: number, ttlSec: number, openNow?: boolean) => Promise<boolean>
  askRevoke: (id: string) => void
  cancelRevoke: () => void
  confirmRevoke: () => Promise<boolean>
  renew: (id: string, ttlSec: number) => Promise<boolean>
  open: (id: string) => boolean
}

export interface PreviewDeps {
  api?: PreviewApi
  openTab?: (url: string) => void
}

export function useJobPreviews(jobId: () => string, deps: PreviewDeps = {}): PreviewController {
  const api = deps.api ?? defaultPreviewApi
  const openTab = deps.openTab ?? openInNewTab

  const items = ref<JobPreview[]>([])
  const loading = ref(false)
  const creating = ref(false)
  const error = ref('')
  const notice = ref<PreviewNotice | ''>('')
  const busy = ref<Set<string>>(new Set())
  const pendingRevoke = ref('')
  const openUrls = ref<Record<string, string>>({})

  function setBusy(id: string, on: boolean): void {
    const next = new Set(busy.value)
    if (on) next.add(id)
    else next.delete(id)
    busy.value = next
  }

  function dropUrl(id: string): void {
    if (!(id in openUrls.value)) return
    const next = { ...openUrls.value }
    delete next[id]
    openUrls.value = next
  }

  async function load(): Promise<void> {
    loading.value = true
    try {
      items.value = await api.list(jobId())
      error.value = ''
      // 已经不在列表里或已失效的预览，其一次性 URL 没有用了
      for (const id of Object.keys(openUrls.value)) {
        const p = items.value.find((x) => x.id === id)
        if (!p || !previewActive(p)) dropUrl(id)
      }
    } catch (e) {
      error.value = previewErrorText(e)
    } finally {
      loading.value = false
    }
  }

  async function create(port: number, ttlSec: number, openNow = false): Promise<boolean> {
    if (creating.value) return false
    creating.value = true
    error.value = ''
    notice.value = ''
    try {
      const p = await api.create(jobId(), { port, ttl_sec: ttlSec })
      if (p.url) openUrls.value = { ...openUrls.value, [p.id]: p.url }
      notice.value = 'created'
      // 不把 url 留在列表项里：列表只放非 secret 的字段
      const { url: _url, ...row } = p
      void _url
      items.value = [{ state: 'active', job_id: jobId(), ...row }, ...items.value.filter((x) => x.id !== p.id)]
      if (openNow) open(p.id)
      await load()
      return true
    } catch (e) {
      error.value = previewErrorText(e)
      return false
    } finally {
      creating.value = false
    }
  }

  function askRevoke(id: string): void {
    pendingRevoke.value = id
  }

  function cancelRevoke(): void {
    pendingRevoke.value = ''
  }

  async function confirmRevoke(): Promise<boolean> {
    const id = pendingRevoke.value
    if (!id) return false
    pendingRevoke.value = ''
    setBusy(id, true)
    error.value = ''
    notice.value = ''
    try {
      await api.revoke(jobId(), id)
      dropUrl(id)
      notice.value = 'revoked'
      await load()
      return true
    } catch (e) {
      error.value = previewErrorText(e)
      return false
    } finally {
      setBusy(id, false)
    }
  }

  async function renew(id: string, ttlSec: number): Promise<boolean> {
    setBusy(id, true)
    error.value = ''
    notice.value = ''
    try {
      await api.renew(jobId(), id, ttlSec)
      notice.value = 'renewed'
      await load()
      return true
    } catch (e) {
      error.value = previewErrorText(e)
      return false
    } finally {
      setBusy(id, false)
    }
  }

  function open(id: string): boolean {
    const url = openUrls.value[id]
    if (!url) return false
    // 一次性：打开后就丢掉，不留在内存里等第二次点击（第二次只会 404）。
    dropUrl(id)
    openTab(url)
    return true
  }

  return { items, loading, creating, error, notice, busy, pendingRevoke, openUrls, load, create, askRevoke, cancelRevoke, confirmRevoke, renew, open }
}
