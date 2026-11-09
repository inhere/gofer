import type { WorkbenchThread } from '../api/types'
import { t } from '../i18n'

// supervisionText labels a relay thread that is being supervised (「监督中」 plus the
// non-zero counts); '' when the thread carries no supervision.
export function supervisionText(thread: WorkbenchThread): string {
  const s = thread.supervising
  if (!s) return ''
  const parts: string[] = [t('workbench.row.supervising')]
  if (s.jobs) parts.push(t('workbench.row.supervisingJobs', { n: s.jobs }))
  if (s.subagents) parts.push(t('workbench.row.supervisingSubagents', { n: s.subagents }))
  if (s.watches) parts.push(t('workbench.row.supervisingWatches', { n: s.watches }))
  return parts.join(' · ')
}
