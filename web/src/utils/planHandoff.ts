import type { PlanHandoff } from '../api/types'

export function mergeHandoffs(latest: PlanHandoff | null, history: PlanHandoff[]): PlanHandoff[] {
  const byVersion = new Map<number, PlanHandoff>()
  for (const item of history) byVersion.set(item.version, item)
  if (latest) byVersion.set(latest.version, latest)
  return [...byVersion.values()].sort((a, b) => b.version - a.version)
}

export function isHistoricalVersion(selectedVersion: number, latestVersion: number): boolean {
  return selectedVersion > 0 && selectedVersion < latestVersion
}

export function handoffEditorDraft(mode: 'edit' | 'new', latest: PlanHandoff | null): string {
  return mode === 'edit' ? latest?.body ?? '' : ''
}
