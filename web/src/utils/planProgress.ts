// Plan progress shared by the plan list and plan detail views.
// The server's `completion` is the single progress figure (todos first, jobs as the
// fallback — jobstore.RollupPlanCompletion). An older server sends no `completion`
// (undefined means "not supported", NOT "zero progress"): then the legacy job-count
// display is used unchanged.
import type { Plan, PlanCounts } from '../api/types'

export interface ProgressSegment {
  cls: string
  pct: number
}

// Job-count bar: done / running / failed / queued share of all jobs.
export function jobSegments(c?: PlanCounts): ProgressSegment[] {
  if (!c || c.total <= 0) {
    return []
  }
  const pct = (n: number) => (n / c.total) * 100
  return [
    { cls: 'seg--done', pct: pct(c.done) },
    { cls: 'seg--run', pct: pct(c.running) },
    { cls: 'seg--fail', pct: pct(c.failed) },
    { cls: 'seg--queue', pct: pct(c.queued) },
  ].filter((s) => s.pct > 0)
}

// Progress bar: todo-based plans show the complete (done + skipped) and doing
// shares; job-based plans and older servers keep the job-count bar.
export function progressSegments(p: Plan): ProgressSegment[] {
  const c = p.completion
  if (!c || c.basis === 'jobs') {
    return jobSegments(p.counts)
  }
  if (c.total <= 0) {
    return []
  }
  const doing = p.todo_counts?.doing ?? 0
  return [
    { cls: 'seg--done', pct: (c.done / c.total) * 100 },
    { cls: 'seg--run', pct: (doing / c.total) * 100 },
  ].filter((s) => s.pct > 0)
}

// Main figure: "5/6", or "—" when there is nothing to measure.
export function progressText(p: Plan): string {
  const c = p.completion
  if (!c) {
    return p.counts ? `${p.counts.done}/${p.counts.total}` : '—'
  }
  return c.percent == null ? '—' : `${c.done}/${c.total}`
}

// Compact job summary, e.g. "job 11（✓10 ✗1）".
function jobSummary(c?: PlanCounts): string {
  if (!c || c.total <= 0) {
    return ''
  }
  const parts = [`✓${c.done}`]
  if (c.failed) {
    parts.push(`✗${c.failed}`)
  }
  if (c.running) {
    parts.push(`▶${c.running}`)
  }
  if (c.queued) {
    parts.push(`…${c.queued}`)
  }
  return `job ${c.total}（${parts.join(' ')}）`
}

// Secondary line naming the basis and the other dimension, e.g.
// "待办 5/6（含跳过 1）· 进行中 1 · job 11（✓10 ✗1）".
export function progressDetail(p: Plan): string {
  const c = p.completion
  if (!c) {
    const j = p.counts
    return j && j.total > 0
      ? `done ${j.done} · running ${j.running} · failed ${j.failed} · queued ${j.queued}`
      : ''
  }
  const jobs = jobSummary(p.counts)
  if (c.basis === 'todos') {
    const t = p.todo_counts
    let todo = `待办 ${c.done}/${c.total}`
    if (t?.skipped) {
      todo += `（含跳过 ${t.skipped}）`
    }
    if (t?.doing) {
      todo += ` · 进行中 ${t.doing}`
    }
    return jobs ? `${todo} · ${jobs}` : todo
  }
  return jobs
}
