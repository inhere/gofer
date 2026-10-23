// 「记忆」页（/memory，issue gofer-hez1）的纯逻辑：作用域 / 项目 / 关键字的 URL 契约、
// 接口结果归一成列表行、筛选，以及旧链接 `/issues?tab=memories` 的重定向。
// 渲染在 views/Memory.vue，列表行在 components/MemoryList.vue（两处共用同一份取数与筛选）。
import type { MemoryDoctorFinding, ScopedMemory, TrackerMemory } from '../api/types'
import { memoryKindMatches, trackerMemoryMatches } from './trackerView'

// 记忆的三档作用域：随仓库（.gofer/tracker）、存于 server 的项目、存于 server 的全局。
export type MemoryScope = 'repo' | 'project' | 'global'

export type MemoryRow = TrackerMemory & { data: TrackerMemory['body'] }

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
  return raw === 'project' || raw === 'global' ? raw : 'repo'
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
      }
      return { id: item.key, rev: 1, updated_at: item.updated_at, body, data: body }
    })
}

// memoryDoctorOf：服务端 doctor 标记（只有仓库记忆有）。
export function memoryDoctorOf(result: MemoryResult): Record<string, MemoryDoctorFinding[]> {
  return result.doctor ?? {}
}

// filterMemories：关键字（key / 摘要 / 正文）+ 类型 chip + 「只看有标记的」。
export function filterMemories(
  rows: MemoryRow[],
  query: string,
  kinds: string[],
  flaggedOnly: boolean,
  doctor: Record<string, MemoryDoctorFinding[]>,
): MemoryRow[] {
  return rows.filter(
    (row) =>
      trackerMemoryMatches(row.data, row.id, query) &&
      memoryKindMatches(row.data, kinds, flaggedOnly, (doctor[memoryRowKey(row)]?.length ?? 0) > 0),
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
