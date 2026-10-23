// Workflow 扇出步的对比视图数据整理（Z4）。纯函数，组件只负责渲染。
//  - latestFans：同一 fan 多次重试只取最新 attempt。
//  - buildColumns：fan 行 + job 详情 + 汇报尾部 → 一列一个 agent 的展示数据。
//  - awaitingPick：join=pick 的步是否停在"待择优"。
//  - mergeAvailability / parseMergeError：合并按钮的可用性与 409 解析。
import type { Job, JobStatus, WorkflowStatus, WorkflowStep } from '../api/types'
import { isLocalRunnerName } from './runnerDisplay'
import { t } from '../i18n'

const TERMINAL: ReadonlySet<string> = new Set(['done', 'failed', 'cancelled', 'timeout', 'rejected'])

export function isTerminalStatus(status: string | undefined): boolean {
  return !!status && TERMINAL.has(status)
}

// 一行是否属于扇出（fan_index>=1）。
export function isFanRow(row: WorkflowStep): boolean {
  return (row.fan_index ?? 0) >= 1
}

// 同一 fan_index 的多次 attempt 只留最新一次，按 fan_index 升序。
export function latestFans(rows: WorkflowStep[]): WorkflowStep[] {
  const byFan = new Map<number, WorkflowStep>()
  for (const row of rows) {
    if (!isFanRow(row)) continue
    const cur = byFan.get(row.fan_index!)
    if (!cur || (row.attempt ?? 1) >= (cur.attempt ?? 1)) byFan.set(row.fan_index!, row)
  }
  return [...byFan.values()].sort((a, b) => a.fan_index! - b.fan_index!)
}

export interface FanColumn {
  fanIndex: number
  jobId: string
  agent: string
  runner: string
  status: JobStatus | 'pending'
  // 秒；未开始 / 未知为 null
  durationSec: number | null
  diffSummary: string
  commitsAhead: number | null
  branch: string
  verify: { status: string; text: string } | null
  tail: string
  picked: boolean
  // 重试过几次（attempt-1 的最大值，>0 才显示）
  retries: number
  error: string
  // 本列所在 job 是否在远端执行（合并只支持本机 runner）
  remote: boolean
}

// 汇报尾部预览：取最后 maxLines 行、最多 maxChars 字符，去掉尾部空白。
export function tailPreview(text: string | undefined, maxLines = 8, maxChars = 700): string {
  if (!text) return ''
  const lines = text.replace(/\r\n/g, '\n').trimEnd().split('\n')
  let out = lines.slice(-maxLines).join('\n')
  if (out.length > maxChars) out = '…' + out.slice(out.length - maxChars)
  return out
}

function verifyBrief(job?: Job): FanColumn['verify'] {
  const v = job?.verify
  if (!v) return null
  const text =
    v.status === 'passed'
      ? t('util.compare.verify.passed')
      : v.status === 'failed'
        ? t('util.compare.verify.failed')
        : v.status === 'timeout'
          ? t('util.compare.verify.timeout')
          : t('util.compare.verify.skipped')
  return { status: v.status, text }
}

export function isRemoteJob(job: Pick<Job, 'runner' | 'source'> | undefined, runner?: string): boolean {
  const src = job?.source ?? ''
  if (src.startsWith('worker:') || src.startsWith('peer:')) return true
  const name = job?.runner ?? runner ?? ''
  return !isLocalRunnerName(name)
}

export function buildColumns(
  rows: WorkflowStep[],
  jobs: Record<string, Job | undefined>,
  tails: Record<string, string | undefined> = {},
  nowSec: number = Math.floor(Date.now() / 1000),
): FanColumn[] {
  const maxAttempt = new Map<number, number>()
  for (const row of rows) {
    if (isFanRow(row)) maxAttempt.set(row.fan_index!, Math.max(maxAttempt.get(row.fan_index!) ?? 1, row.attempt ?? 1))
  }
  return latestFans(rows).map((row) => {
    const job = row.job_id ? jobs[row.job_id] : undefined
    const status = ((job?.status ?? row.status) || 'pending') as JobStatus | 'pending'
    let durationSec: number | null = null
    if (job && job.started_at > 0) {
      const end = isTerminalStatus(status) && job.ended_at ? job.ended_at : nowSec
      durationSec = Math.max(0, end - job.started_at)
    }
    return {
      fanIndex: row.fan_index!,
      jobId: row.job_id ?? '',
      agent: job?.agent ?? '',
      runner: job?.runner ?? '',
      status,
      durationSec,
      diffSummary: (job?.diff_summary || row.diff_summary || '').trim(),
      commitsAhead: job?.commits_ahead ?? null,
      branch: job?.worktree_branch || row.worktree_branch || '',
      verify: verifyBrief(job),
      tail: tailPreview(row.job_id ? tails[row.job_id] : undefined),
      picked: !!row.picked,
      retries: Math.max(0, (maxAttempt.get(row.fan_index!) ?? 1) - 1),
      error: job?.error ?? '',
      remote: isRemoteJob(job),
    }
  })
}

export interface PickContext {
  workflowStatus: WorkflowStatus | undefined
  currentStep: number
  stepIndex: number
}

// join=pick 的步是否停在"待择优"：工作流还在跑、就是当前步、没有已选的、且每个 fan 都已终态。
export function awaitingPick(rows: WorkflowStep[], ctx: PickContext): boolean {
  const fans = latestFans(rows)
  if (fans.length === 0 || !fans.some((r) => r.join === 'pick')) return false
  if (fans.some((r) => r.picked)) return false
  if (ctx.workflowStatus !== 'running' || ctx.currentStep !== ctx.stepIndex) return false
  return fans.every((r) => isTerminalStatus(r.status))
}

export function pickedFan(rows: WorkflowStep[]): number {
  return latestFans(rows).find((r) => r.picked)?.fan_index ?? 0
}

export function isPickStep(rows: WorkflowStep[]): boolean {
  return latestFans(rows).some((r) => r.join === 'pick')
}

// 某列能不能被选：待择优且这一路成功（后端只允许选 done 的 fan）。
export function canPickColumn(col: FanColumn, awaiting: boolean): boolean {
  return awaiting && col.status === 'done'
}

// 合并按钮可用性：远端 runner 灰显并说明；没有受管 worktree 也不可用。
export function mergeAvailability(
  job: Pick<Job, 'runner' | 'source' | 'worktree_path'> | undefined,
): { ok: boolean; reason: string } {
  if (!job || !job.worktree_path) return { ok: false, reason: t('util.compare.noWorktree') }
  if (isRemoteJob(job)) return { ok: false, reason: t('util.compare.remoteNoMerge') }
  return { ok: true, reason: '' }
}

// 对比视图某一列能否合并：远程 runner 不行（原因给用户）；没有分支也不行。
export function columnMergeReason(col: Pick<FanColumn, 'remote' | 'branch'>): string {
  if (col.remote) return mergeAvailability({ runner: 'remote', source: 'worker:', worktree_path: col.branch || 'x' }).reason
  if (!col.branch) return t('util.compare.noWorktree')
  return ''
}

export interface MergeErrorInfo {
  kind: 'conflict' | 'main-not-ready' | 'unsupported' | 'gone' | 'dirty' | 'other'
  files: string[]
  message: string
}

// 把合并接口的失败翻成界面可展示的结构。后端冲突文案：
//   "worktree merge has conflicts: a.go, b.go"（409，且仓库已 abort + reset 复原）。
export function parseMergeError(status: number | undefined, message: string): MergeErrorInfo {
  const msg = message || ''
  if (status === 409) {
    const m = msg.match(/worktree merge has conflicts(?::\s*([^\n]*))?/)
    if (m) {
      const files = (m[1] ?? '')
        .split(',')
        .map((f) => f.replace(/\s+-\s*$/, '').trim())
        .filter(Boolean)
      return { kind: 'conflict', files, message: msg }
    }
    if (msg.includes('main checkout')) return { kind: 'main-not-ready', files: [], message: msg }
    if (msg.includes('local runner')) return { kind: 'unsupported', files: [], message: msg }
    if (msg.includes('not present')) return { kind: 'gone', files: [], message: msg }
    if (msg.includes('uncommitted')) return { kind: 'dirty', files: [], message: msg }
  }
  return { kind: 'other', files: [], message: msg }
}

// 面向用户的错误说明。
export function mergeErrorText(info: MergeErrorInfo): string {
  switch (info.kind) {
    case 'conflict':
      return t('util.compare.err.conflict')
    case 'main-not-ready':
      return t('util.compare.err.mainNotReady')
    case 'unsupported':
      return t('util.compare.err.unsupported')
    case 'gone':
      return t('util.compare.err.gone')
    case 'dirty':
      return t('util.compare.err.dirty')
    default:
      return info.message
  }
}

export interface MergeOptionsForm {
  squash: boolean
  cleanupOthers: boolean
}

// 对话框选项 → 接口 body。
export function mergeRequestBody(opts: MergeOptionsForm, canCleanup: boolean): { squash: boolean; cleanup_others: boolean } {
  return { squash: opts.squash, cleanup_others: canCleanup && opts.cleanupOthers }
}

// 合并成功后的一句话说明。
export function mergeResultText(squash: boolean, cleaned: string[] | undefined): string {
  const how = squash ? t('util.compare.mergedSquash') : t('util.compare.mergedMerge')
  const n = cleaned?.length ?? 0
  return n > 0 ? how + t('util.compare.cleanedTail', { n }) : how + t('util.compare.done')
}
