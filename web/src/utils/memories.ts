// 「记忆」页（/memory，issue gofer-hez1）的纯逻辑：作用域 / 项目 / 关键字的 URL 契约、
// 接口结果归一成列表行、筛选，以及旧链接 `/issues?tab=memories` 的重定向。
// 渲染在 views/Memory.vue，列表行在 components/MemoryList.vue（两处共用同一份取数与筛选）。
import type { MemoryDoctorFinding, ScopedMemory, TrackerMemory } from '../api/types'
import { clampPage, pageCountOf, readPageQuery } from './issuePaging'
import { memoryKindMatches, trackerMemoryMatches } from './trackerView'
import { t } from '../i18n'

// 记忆的作用域：随仓库（.gofer/tracker）、存于 server 的项目、存于 server 的全局；
// `all` 是「全部项目」的读视图——前端并发取上面三路再合并（每条带来源）。
export type MemoryScope = 'repo' | 'project' | 'global' | 'all'

export type MemoryRow = TrackerMemory & { data: TrackerMemory['body'] }

// 一条记忆的来源。合并视图里同名的 key 可能同时存在于全局 / 某个项目 / 某个仓库，
// 那是不同的记忆，所以去重与 doctor 查表都以「来源 + key」为键。
export type MemorySource =
  | { scope: 'repo'; trackerId: string; label: string }
  | { scope: 'project'; project: string }
  | { scope: 'global' }

export type SourcedMemoryRow = MemoryRow & { source?: MemorySource }

// 记忆列表的一次响应：仓库记忆与 server 记忆的字段不同，doctor 只有仓库记忆有。
export type MemoryResult = {
  memories?: Array<TrackerMemory | ScopedMemory>
  doctor?: Record<string, MemoryDoctorFinding[]>
}

function queryValue(value: unknown): string {
  return Array.isArray(value) ? String(value[0] ?? '') : String(value ?? '')
}

// readMemoryScope：?memory_scope= 里认不出来的值（旧链接、手改的 URL）回落到仓库。
export function readMemoryScope(query: Record<string, unknown> | undefined): MemoryScope {
  const raw = queryValue(query?.memory_scope)
  return raw === 'project' || raw === 'global' || raw === 'all' ? raw : 'repo'
}

// readMemoryProject：项目作用域下的 ?memory_project=（空串 = 还没选）。
export function readMemoryProject(query: Record<string, unknown> | undefined): string {
  return queryValue(query?.memory_project)
}

// readMemoryKey：深链要打开的那条记忆（今天页的记忆整理卡带过来）。
export function readMemoryKey(query: Record<string, unknown> | undefined): string {
  return queryValue(query?.memory)
}

// readMemorySearch：搜索关键字（?q=，可分享、后退可复原）。
export function readMemorySearch(query: Record<string, unknown> | undefined): string {
  return queryValue(query?.q)
}

// memoryQueryUpdate：把作用域（与项目）写进 query，其余参数原样保留。
// 非项目作用域去掉 memory_project，链接才干净（与旧记忆页签一致）。
export function memoryQueryUpdate(
  base: Record<string, unknown> | undefined,
  scope: MemoryScope,
  project: string,
): Record<string, string> {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries(base ?? {})) {
    if (typeof v === 'string' && v) next[k] = v
  }
  next.memory_scope = scope
  if (scope === 'project' && project) next.memory_project = project
  else delete next.memory_project
  return next
}

// readMemoryPage：列表页码（?page=，与 Issues / 工作流页同一口径）；坏值回落到第 1 页。
export const readMemoryPage = readPageQuery

// pageQueryUpdate：页码写进 ?page=（第 1 页不写，链接保持干净），其余参数保留。
// 实现与其它列表页共用（utils/issuePaging）。
export { pageQueryUpdate } from './issuePaging'

// 每页条数记在本机（与 Issues 页同样不写 URL），选项与默认值沿用同一套。
export const MEMORY_PAGE_SIZE_KEY = 'gofer.memory.pagesize'

export function memoryPageCount(total: number, pageSize: number): number {
  return pageCountOf(total, pageSize)
}

// memoryPageSlice：平铺列表切页（记忆没有父子层级，按行切）。
export function memoryPageSlice<T>(rows: T[], pageSize: number, page: number): T[] {
  const size = Math.max(1, Math.floor(pageSize))
  const cur = clampPage(page, memoryPageCount(rows.length, size))
  return rows.slice((cur - 1) * size, cur * size)
}

// legacyMemoryQuery：旧的记忆页签地址（/issues?tab=memories&memory_scope=…&tracker=…&memory=…）
// 要带全部参数跳 /memory；其余情况返回 null（不是记忆链接）。
export function legacyMemoryQuery(query: Record<string, unknown> | undefined): Record<string, string> | null {
  if (queryValue(query?.tab) !== 'memories') return null
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries(query ?? {})) {
    if (k === 'tab') continue
    if (typeof v === 'string' && v) next[k] = v
  }
  return next
}

// memoryRowKey：一条记忆的稳定标识（server 记忆用 key，仓库记忆也优先用 body.key）。
export function memoryRowKey(row: Pick<MemoryRow, 'id' | 'data'>): string {
  return row.data.key || row.id
}

// memoryRowsOf：接口结果 → 列表行。两种响应的字段名不同，统一成 `data` 供 MemoryList 渲染。
// 已删除的记忆不列出（与迁移前同一口径）。
export function memoryRowsOf(result: MemoryResult): MemoryRow[] {
  return (result.memories ?? [])
    .filter((item) => !item.deleted)
    .map((item) => {
      if ('body' in item) return { ...item, data: item.body }
      const body = {
        key: item.key,
        content: item.content,
        tags: item.tags,
        updated_at: item.updated_at,
        by: item.updated_by,
        kind: item.kind,
        summary: item.summary,
        title: item.title,
        source: item.source,
        created_unknown: item.created_unknown,
        expires_at: item.expires_at,
        flags: item.flags,
      }
      return { id: item.key, rev: 1, updated_at: item.updated_at, body, data: body }
    })
}

// memoryDoctorOf：服务端 doctor 标记（只有仓库记忆有）。
export function memoryDoctorOf(result: MemoryResult): Record<string, MemoryDoctorFinding[]> {
  return result.doctor ?? {}
}

// memorySourceKey：一条记忆在合并视图里的唯一键（来源 + key）。
// 没有来源的行（单一作用域）退化成 memoryRowKey，与迁移前同一口径。
function sourceKeyOf(source: MemorySource, key: string): string {
  if (source.scope === 'repo') return `repo:${source.trackerId}:${key}`
  if (source.scope === 'project') return `project:${source.project}:${key}`
  return `global:${key}`
}

export function memorySourceKey(row: SourcedMemoryRow): string {
  const key = memoryRowKey(row)
  return row.source ? sourceKeyOf(row.source, key) : key
}

// memorySourceLabel：列表 / 抽屉里显示的来源文字。
export function memorySourceLabel(source: MemorySource | undefined): string {
  if (!source) return ''
  if (source.scope === 'repo') return t('memory.source.repo', { name: source.label })
  if (source.scope === 'project') return t('memory.source.project', { name: source.project })
  return t('memory.source.global')
}

// 一次取数（一路来源）。
export interface MemoryBatch {
  source: MemorySource
  result: MemoryResult
}

function memoryTime(row: MemoryRow): number {
  const raw = row.data.updated_at || row.updated_at
  const parsed = raw ? Date.parse(raw) : NaN
  return Number.isNaN(parsed) ? 0 : parsed
}

// sortMemories：最近更新的排前面（时间相同按 key，保证顺序稳定）。
export function sortMemories<T extends MemoryRow>(rows: T[]): T[] {
  return [...rows].sort((a, b) => memoryTime(b) - memoryTime(a) || memoryRowKey(a).localeCompare(memoryRowKey(b)))
}

// mergeMemoryBatches：「全部项目」范围把各路响应合成一张表。
// 去重键是「来源 + key」：全局 / 每个项目 / 每个仓库各有一份同名 key 是不同记忆。
export function mergeMemoryBatches(batches: MemoryBatch[]): SourcedMemoryRow[] {
  const seen = new Set<string>()
  const out: SourcedMemoryRow[] = []
  for (const batch of batches) {
    for (const row of memoryRowsOf(batch.result)) {
      const item: SourcedMemoryRow = { ...row, source: batch.source }
      const key = memorySourceKey(item)
      if (seen.has(key)) continue
      seen.add(key)
      out.push(item)
    }
  }
  return sortMemories(out)
}

// mergeMemoryDoctor：各路的 doctor 标记也要带上来源前缀，否则同名 key 会串。
export function mergeMemoryDoctor(batches: MemoryBatch[]): Record<string, MemoryDoctorFinding[]> {
  const out: Record<string, MemoryDoctorFinding[]> = {}
  for (const batch of batches) {
    for (const [key, findings] of Object.entries(memoryDoctorOf(batch.result))) {
      out[sourceKeyOf(batch.source, key)] = findings
    }
  }
  return out
}

// ---- 详情抽屉的编辑态（内容 / 摘要 / 标签）----

export interface MemoryEdit {
  content: string
  summary: string
  tags: string[]
}

export function memoryEditOf(row: MemoryRow): MemoryEdit {
  return {
    content: String(row.data.content ?? ''),
    summary: String(row.data.summary ?? ''),
    tags: [...(row.data.tags ?? [])],
  }
}

// normalizeMemoryTags：去空白、去重、保序（服务端会再排序，这里只保证提交的口径）。
export function normalizeMemoryTags(tags: string[]): string[] {
  const out: string[] = []
  for (const raw of tags) {
    const tag = String(raw ?? '').trim()
    if (tag && !out.includes(tag)) out.push(tag)
  }
  return out
}

export function addMemoryTag(tags: string[], tag: string): string[] {
  return normalizeMemoryTags([...tags, tag])
}

export function removeMemoryTag(tags: string[], tag: string): string[] {
  return tags.filter((item) => item !== tag)
}

// memoryUpdateBody：抽屉保存的请求体（仓库记忆与 scoped 记忆共用：content / summary / tags）。
export function memoryUpdateBody(edit: MemoryEdit): { content: string; summary: string; tags: string[] } {
  return { content: edit.content, summary: edit.summary.trim(), tags: normalizeMemoryTags(edit.tags) }
}

// filterMemories：关键字（key / 摘要 / 正文）+ 类型 chip + 「只看有标记的」。
// 关键字搜索在合并视图上同样生效（搜索的是已取回的全部范围）。
export function filterMemories<T extends SourcedMemoryRow>(
  rows: T[],
  query: string,
  kinds: string[],
  flaggedOnly: boolean,
  doctor: Record<string, MemoryDoctorFinding[]>,
): T[] {
  return rows.filter(
    (row) =>
      trackerMemoryMatches(row.data, row.id, query) &&
      memoryKindMatches(row.data, kinds, flaggedOnly, (doctor[memorySourceKey(row)]?.length ?? 0) > 0),
  )
}

// searchQueryUpdate：搜索关键字写进 ?q=（空串删掉），其余参数保留。
export function searchQueryUpdate(base: Record<string, unknown> | undefined, text: string): Record<string, string> {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries(base ?? {})) {
    if (typeof v === 'string' && v && k !== 'q') next[k] = v
  }
  if (text) next.q = text
  return next
}
