// 收集箱（design 2026-10-16 §3）的纯逻辑：kind 标签、随手记表单校验与请求体、
// 「待整理」区页签路由（?tab=capture）、看板请求的状态过滤。组件只负责渲染。
import type { WorkItem } from '../api/types'

export type CaptureKind = 'idea' | 'issue' | 'request' | 'note'

export const CAPTURE_KINDS: Array<{ key: CaptureKind; label: string }> = [
  { key: 'idea', label: '想法' },
  { key: 'issue', label: '问题' },
  { key: 'request', label: '需求' },
  { key: 'note', label: '记录' },
]

export const DEFAULT_CAPTURE_KIND: CaptureKind = 'note'
export const CAPTURE_TEXT_MAX = 2000
export const CAPTURE_STATUS = 'captured'
export const CAPTURE_BOX_LINK = '/work?tab=capture'

export function captureKindLabel(kind: string | undefined): string {
  const k = CAPTURE_KINDS.find((x) => x.key === kind)
  return k ? k.label : kind ? kind : '记录'
}

export interface CaptureForm {
  text: string
  kind: string
  project: string
}

export interface CaptureRequest {
  text: string
  kind: CaptureKind
  project?: string
}

export interface CaptureBuild {
  req: CaptureRequest | null
  error: string
}

// buildCaptureRequest：校验并构造 POST /v1/work-items/capture 的请求体；project 为空则省略。
export function buildCaptureRequest(f: CaptureForm): CaptureBuild {
  const text = f.text.trim()
  if (!text) return { req: null, error: '先写点内容' }
  if ([...text].length > CAPTURE_TEXT_MAX) return { req: null, error: `内容最多 ${CAPTURE_TEXT_MAX} 个字符` }
  const kind = CAPTURE_KINDS.find((k) => k.key === f.kind)?.key
  if (!kind) return { req: null, error: '请选择类型' }
  const req: CaptureRequest = { text, kind }
  const project = f.project.trim()
  if (project) req.project = project
  return { req, error: '' }
}

// ---- 页签 ----

export type TriageTab = 'drafts' | 'capture'

// triageTabFromQuery：?tab=capture 直接打开收集箱，其余（含缺省）是草稿。
export function triageTabFromQuery(tab: unknown): TriageTab {
  const v = Array.isArray(tab) ? tab[0] : tab
  return v === 'capture' ? 'capture' : 'drafts'
}

// triageQuery：切换页签后的 query：收集箱写 tab=capture，草稿去掉 tab；其余参数保留。
export function triageQuery(query: Record<string, unknown>, tab: TriageTab): Record<string, unknown> {
  const next = { ...query }
  if (tab === 'capture') next.tab = 'capture'
  else delete next.tab
  return next
}

// ---- 看板排除 ----

// 工作看板的列表请求不带 status：服务端默认不返回 captured。
export function boardListOpts(showClosed: boolean): { closed: boolean; limit: number } {
  return { closed: showClosed, limit: 1000 }
}

export function capturedListOpts(): { status: string[]; limit: number } {
  return { status: [CAPTURE_STATUS], limit: 1000 }
}

// 防御：即使服务端返回了 captured 条目，看板 / 草稿区也不显示。
export function excludeCaptured(items: WorkItem[]): WorkItem[] {
  return items.filter((it) => (it.status as string) !== CAPTURE_STATUS)
}

export function onlyCaptured(items: WorkItem[]): WorkItem[] {
  return items.filter((it) => (it.status as string) === CAPTURE_STATUS).sort((a, b) => b.created_at - a.created_at)
}

// 收集箱条目的来源说明；没有来源返回空串。
export function captureSource(it: Pick<WorkItem, 'capture_source_session' | 'capture_source_job'>): string {
  const parts: string[] = []
  if (it.capture_source_session) parts.push(`会话 ${it.capture_source_session.slice(0, 8)}`)
  if (it.capture_source_job) parts.push(`job ${it.capture_source_job.slice(0, 10)}`)
  return parts.join(' / ')
}
