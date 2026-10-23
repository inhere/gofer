// ---- 仓库选择器（UI 重设计 P6，issue gofer-hez1）----
// 接口里只有 tracker_id / project_key / rel_path / prefix：没有单独的「仓库名」字段，
// 所以目录名取 rel_path（server 记录的仓库根绝对路径）的最后一段，其余全在前端组合。
export interface TrackerRepoLike {
  tracker_id: string
  project_key: string
  rel_path: string
  prefix?: string
}

// Windows 记录的路径可能带反斜杠，两种分隔符都算。
export function repoPathSegments(relPath: string): string[] {
  return String(relPath ?? '').split(/[\\/]+/).filter((seg) => seg.length > 0)
}

export function repoDirName(relPath: string): string {
  const segs = repoPathSegments(relPath)
  return segs.length > 0 ? segs[segs.length - 1] : ''
}

// 信息条第一行：项目 · 前缀（缺项用 —，项目为空时标「未归属」）。
export function trackerRepoMeta(repo: { project_key: string; prefix?: string }): string {
  return `${repo.project_key || t('issues.tracker.repo.unassigned')} · ${repo.prefix || '—'}`
}

export function trackerRepoLabel(repo: { project_key: string; prefix?: string; rel_path: string }): string {
  return `${trackerRepoMeta(repo)} · ${repo.rel_path || '—'}`
}

// 下拉选项的主文字：项目 key / 仓库目录名——同一项目下多个仓库也能一眼区分。
export function trackerRepoTitle(repo: TrackerRepoLike): string {
  const project = repo.project_key || t('issues.tracker.repo.unassigned')
  const dir = repoDirName(repo.rel_path) || repo.prefix || '—'
  return `${project} / ${dir}`
}

// 下拉选项的次要文字：tracker id + 路径末两级（同级目录常撞名，留两级才好认）。
export function trackerRepoHint(repo: TrackerRepoLike): string {
  const tail = repoPathSegments(repo.rel_path).slice(-2).join('/')
  return tail ? `${repo.tracker_id} · ${tail}` : repo.tracker_id
}

export interface TrackerRepoOption {
  value: string
  title: string
  hint: string
  path: string
}

// 选项顺序：按项目（未归属的排最后），项目内按目录名、再按 tracker id——
// 同一项目的多个仓库紧挨在一起。
export function trackerRepoOptions(repos: TrackerRepoLike[]): TrackerRepoOption[] {
  const dir = (repo: TrackerRepoLike): string => repoDirName(repo.rel_path) || repo.prefix || ''
  return [...repos]
    .sort((a, b) =>
      Number(!a.project_key) - Number(!b.project_key) ||
      a.project_key.localeCompare(b.project_key) ||
      dir(a).localeCompare(dir(b)) ||
      a.tracker_id.localeCompare(b.tracker_id))
    .map((repo) => ({ value: repo.tracker_id, title: trackerRepoTitle(repo), hint: trackerRepoHint(repo), path: repo.rel_path }))
}

export function trackerIssueMatches(data: Record<string, any>, id: string, statuses: string[], type: string, tag: string, query: string): boolean {
  const q = query.trim().toLowerCase()
  return statuses.includes(data.status) && (!type || data.type === type) && (!tag || (data.tags ?? []).includes(tag)) && (!q || `${id} ${data.title ?? ''} ${data.description ?? ''}`.toLowerCase().includes(q))
}

export function fmtTrackerTime(value: string | number | undefined): string {
  const seconds = typeof value === 'number' ? value : value ? Date.parse(value) / 1000 : 0
  return fmtDateTime(seconds)
}

export function trackerMemoryMatches(data: { key?: string; content?: string; summary?: string }, id: string, query: string): boolean {
  const q = query.trim().toLowerCase()
  return !q || `${data.key ?? id} ${data.summary ?? ''} ${data.content ?? ''}`.toLowerCase().includes(q)
}

// ---- 记忆列表（P4）：类型、摘要、年龄、doctor 标记 ----

export type MemoryKind = 'rule' | 'note' | 'handoff'
export const MEMORY_KINDS: MemoryKind[] = ['rule', 'note', 'handoff']
// getter：文案随当前语言取（i18n 字典 memory.kind.*）。
export const MEMORY_KIND_LABEL: Record<MemoryKind, string> = {
  get rule() { return t('memory.kind.rule') },
  get note() { return t('memory.kind.note') },
  get handoff() { return t('memory.kind.handoff') },
}

// 与 tracker.EffectiveMemoryKind 一致：存的 kind，否则旧 `prime` 标签算 rule，否则 note。
export function memoryKind(data: { kind?: string; tags?: string[] }): MemoryKind {
  if (data.kind === 'rule' || data.kind === 'note' || data.kind === 'handoff') return data.kind
  return (data.tags ?? []).includes('prime') ? 'rule' : 'note'
}

const SUMMARY_MAX = 80

// 摘要：存的 summary，否则正文第一行（去掉 Markdown 前缀），≤80 字。
export function memorySummary(data: { summary?: string; content?: string }): string {
  const cap = (s: string) => ([...s].length > SUMMARY_MAX ? [...s].slice(0, SUMMARY_MAX).join('') + '…' : s)
  const stored = (data.summary ?? '').trim()
  if (stored) return cap(stored)
  for (const raw of String(data.content ?? '').split('\n')) {
    const line = raw.replace(/^[\s#>*\-]+/, '').trim()
    if (line) return cap(line)
  }
  return ''
}

// 年龄：今天 / N 天前（无法解析时为空）。
export function memoryAge(updatedAt: string | undefined, nowMs = Date.now()): string {
  const parsed = updatedAt ? Date.parse(updatedAt) : NaN
  if (Number.isNaN(parsed)) return ''
  const days = Math.floor((nowMs - parsed) / 86_400_000)
  return days <= 0 ? t('memory.age.today') : t('memory.age.days', { n: days })
}

const DOCTOR_LABEL: Record<string, string> = {
  'handoff-expired': 'memory.doctor.handoffExpired',
  'note-stale': 'memory.doctor.noteStale',
  'summary-missing': 'memory.doctor.summaryMissing',
  duplicate: 'memory.doctor.duplicate',
  'path-missing': 'memory.doctor.pathMissing',
  'commit-missing': 'memory.doctor.commitMissing',
}

export function doctorLabel(slug: string): string {
  const key = DOCTOR_LABEL[slug]
  return key && te(key) ? t(key) : slug
}

// 记忆列表筛选：类型（空 = 全部）与「只看有标记的」。
export function memoryKindMatches(
  data: { kind?: string; tags?: string[] },
  kinds: string[],
  flaggedOnly = false,
  flagged = false,
): boolean {
  if (flaggedOnly && !flagged) return false
  return kinds.length === 0 || kinds.includes(memoryKind(data))
}
import { fmtAgo, fmtDateTime } from '../api/time'
import { t, te } from '../i18n'

// 同步状态面板的一行：主机名 · 调用方 · 多久前同步 · 摘要（缺项用 —）。
export function syncClientLine(
  c: { client_host: string; caller: string; last_sync_at: number; summary: string },
  nowSec?: number,
): string {
  const ago = c.last_sync_at > 0 ? t('memory.sync.ago', { ago: fmtAgo(c.last_sync_at, nowSec) }) : '—'
  return [c.client_host || 'unknown', c.caller || '—', ago, c.summary || '—'].join(' · ')
}
