export function trackerRepoLabel(repo: { project_key: string; prefix: string; rel_path: string }): string {
  return `${repo.project_key || '未归属'} · ${repo.prefix || '—'} · ${repo.rel_path || '—'}`
}

export function trackerIssueMatches(data: Record<string, any>, id: string, statuses: string[], type: string, tag: string, query: string): boolean {
  const q = query.trim().toLowerCase()
  return statuses.includes(data.status) && (!type || data.type === type) && (!tag || (data.tags ?? []).includes(tag)) && (!q || `${id} ${data.title ?? ''} ${data.description ?? ''}`.toLowerCase().includes(q))
}

export function fmtTrackerTime(value: string | number | undefined): string {
  const seconds = typeof value === 'number' ? value : value ? Date.parse(value) / 1000 : 0
  return fmtDateTime(seconds)
}
import { fmtDateTime } from '../api/time'
