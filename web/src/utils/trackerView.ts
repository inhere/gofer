export function trackerRepoLabel(repo: { project_key: string; prefix: string; rel_path: string }): string {
  return `${repo.project_key || t('issues.tracker.repo.unassigned')} · ${repo.prefix || '—'} · ${repo.rel_path || '—'}`
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
