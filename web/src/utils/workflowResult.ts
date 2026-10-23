// workflow 详情的结果表与模板编辑的纯逻辑（design 2026-10-21 §一·6 / §四，plan S6）。
// 只做「后端字段 → 给人看的一行」的换算与文案，不碰网络，方便单测。
import type { Job, JobStatus, Workflow, WorkflowStep, WorkflowTemplateWriteResult } from '../api/types'
import { fmtDateTime, fmtDuration } from '../api/time'
import { t } from '../i18n'
import { runnerLabel } from './runnerDisplay'

// 结果表一行 = 一个 step-job（重试每次一行、扇出每路一行）。
export interface ResultRow {
  // 稳定 key：step / attempt / fan 三元组
  key: string
  stepIndex: number
  // 步骤列文案：`2 · checks`
  stepLabel: string
  // 重试 / 扇出的徽标文案（a2 / f1，空则不显示）
  tag: string
  status: JobStatus | ''
  // runner 显示名（本机 runner 显示 server，G043）
  runner: string
  // 退出码文案：没有退出码（未起 / 内置步骤失败无码）时为空
  exitCode: string
  duration: string
  // 开始时间（本地时间串；未起的行为空）
  startedAt: string
  // 实际执行目录（ref: 步是临时检出目录）与检出 ref；未起的行为空
  cwd: string
  ref: string
  // 失败行（空数组 = 无摘录，不显示展开）
  excerpt: string[]
  failed: boolean
  jobId?: string
}

// 失败摘录是日志尾部的多行文本；按行切、去空行（后端已去重，这里只做展示整理）。
export function excerptLines(excerpt: string | undefined): string[] {
  if (!excerpt) return []
  return excerpt
    .split(/\r?\n/)
    .map((l) => l.trimEnd())
    .filter((l) => l.length > 0)
}

// 只有失败 / 超时 / 取消的行会带摘录（后端口径），其它状态里即便有文本也不展开。
export function isFailedStatus(status: JobStatus | '' | undefined): boolean {
  return status === 'failed' || status === 'timeout' || status === 'cancelled'
}

function stepName(st: WorkflowStep): string {
  return st.name || `step ${st.step_index}`
}

// 结果行的名字列：扇出的一路显示该路自己的 name（linux-race / web …），并保留步骤名做
// 上下文（`checks · linux-race`）；没有 fan 名（普通 fan_out / 单 job 步）时只有步骤名。
// 以前每路都只显示步骤名，同一个 step 的行看起来一模一样（gofer-j5xc）。
export function rowName(st: WorkflowStep): string {
  const base = stepName(st)
  const fan = st.fan_name
  return fan && fan !== base ? `${base} · ${fan}` : base
}

// workflow 的步骤行 → 结果表行（按 step / attempt / fan 顺序，后端已排好，这里再稳一次）。
export function resultRows(wf: Workflow | null | undefined): ResultRow[] {
  const steps = wf?.steps ?? []
  const rows = steps.slice().sort(
    (a, b) =>
      a.step_index - b.step_index ||
      (a.attempt ?? 1) - (b.attempt ?? 1) ||
      (a.fan_index ?? 0) - (b.fan_index ?? 0),
  )
  return rows.map((st) => {
    const fan = st.fan_index ?? 0
    const attempt = st.attempt ?? 1
    const tag: string[] = []
    if (attempt >= 2) tag.push(`a${attempt}`)
    if (fan >= 1) tag.push(`f${fan}`)
    const status = (st.status ?? '') as JobStatus | ''
    const failed = isFailedStatus(status)
    return {
      key: `${st.step_index}:${attempt}:${fan}:${st.job_id ?? st.name ?? ''}`,
      stepIndex: st.step_index,
      stepLabel: `${st.step_index} · ${rowName(st)}`,
      tag: tag.join(' '),
      status,
      runner: runnerLabel(st.runner) || '—',
      exitCode: typeof st.exit_code === 'number' ? String(st.exit_code) : '',
      duration: fmtDuration(typeof st.duration_sec === 'number' ? st.duration_sec : null),
      startedAt: st.started_at ? fmtDateTime(st.started_at) : '',
      cwd: st.cwd ?? '',
      ref: st.ref ?? '',
      excerpt: failed ? excerptLines(st.failure_excerpt) : [],
      failed,
      jobId: st.job_id,
    }
  })
}

// 有没有可展开的失败摘录（决定是否显示「摘录」按钮）。
export function hasExcerpt(row: ResultRow): boolean {
  return row.excerpt.length > 0
}

// 「只重跑失败的」只在终态、且确有失败行时可用（running 的服务端 400）。
export function canRerunFailed(wf: Workflow | null | undefined): boolean {
  if (!wf || wf.status === 'running') return false
  return resultRows(wf).some((r) => r.failed)
}

// mode=from 的 step 取值：后端接受步骤序号或名字，用序号最稳。
export function fromStepValue(row: ResultRow): string {
  return String(row.stepIndex)
}

// 结果表的失败 / 成功计数（标题旁一句「3 路失败」）。
export function failedCount(rows: ResultRow[]): number {
  return rows.filter((r) => r.failed).length
}

// ---- job ↔ workflow 关联（job 详情页）----

// job 在所属工作流里的位置文案：step-job 才有 workflow_id，普通 job 返回空。
// 步骤 / 重试 / fan 序号都从 job 自身字段取；fan 名不在 job 行上，由调用方从
// workflow 详情的步骤链里找出来传进来。
export function jobWorkflowLabel(
  job: Pick<Job, 'workflow_id' | 'step_index' | 'attempt' | 'fan_index'>,
  fanName?: string,
): string {
  if (!job.workflow_id) return ''
  const parts = [t('util.wf.step', { n: job.step_index ?? 1 })]
  if ((job.attempt ?? 1) >= 2) parts.push(t('util.wf.attempt', { n: job.attempt ?? 1 }))
  if ((job.fan_index ?? 0) >= 1) {
    const fan = job.fan_index ?? 1
    parts.push(fanName ? t('util.wf.fanNamed', { n: fan, name: fanName }) : t('util.wf.fan', { n: fan }))
  }
  return parts.join(' · ')
}

// 从 workflow 步骤链里找出这个 job 的那一行，返回它的 fan 名（没有则空）。
// 比对 step/attempt/fan 三元组：同一个 step 的重试 / 并行路各有自己的 job_id。
export function fanNameForJob(
  steps: WorkflowStep[] | undefined,
  job: Pick<Job, 'step_index' | 'attempt' | 'fan_index'>,
): string {
  const step = job.step_index ?? 0
  const attempt = job.attempt ?? 1
  const fan = job.fan_index ?? 0
  for (const st of steps ?? []) {
    if (st.step_index === step && (st.attempt ?? 1) === attempt && (st.fan_index ?? 0) === fan) {
      return st.fan_name ?? ''
    }
  }
  return ''
}

// ---- 模板编辑（D7）----

export type TemplateScope = 'global' | 'project'

// 编辑 / 新建 / 删除按钮是否显示：写模板要 config.write（服务端也拒）。
// whoami 未取到时按「有权限」处理（与 permissionGate 一致：由服务端 403 说话）。
export interface TemplateEditGate {
  editable: boolean
  reason: string
}

export function templateEditGate(holdsConfigWrite: boolean, whoamiKnown: boolean): TemplateEditGate {
  if (!whoamiKnown || holdsConfigWrite) return { editable: true, reason: '' }
  return { editable: false, reason: t('util.wf.edit.needWrite') }
}

// 内置模板本身只读：同名保存到全局 / 项目是「覆盖」，不是改内置。
export function templateReadOnlyNote(source: string | undefined): string {
  if (source === 'builtin') return t('util.wf.edit.builtinNote')
  return ''
}

// 保存前提示：与内置同名时说清「将覆盖内置模板 <name>」。
export function overridesBuiltinNotice(
  name: string,
  source: string | undefined,
  scope: TemplateScope,
): string {
  if (!name || source !== 'builtin') return ''
  const scopeText = t(scope === 'global' ? 'util.wf.edit.overrideGlobal' : 'util.wf.edit.overrideProject')
  return t('util.wf.edit.override', { name, scope: scopeText })
}

// shadows 说明：这一份模板盖住了哪些层。
export function shadowsNote(shadows: string[] | undefined): string {
  if (!shadows || shadows.length === 0) return ''
  return t('util.wf.edit.shadows', { layers: shadows.map(sourceLabel).join(' / ') })
}

function sourceLabel(source: string): string {
  switch (source) {
    case 'project':
      return t('util.wf.source.project')
    case 'global':
      return t('util.wf.source.global')
    case 'builtin':
      return t('util.wf.source.builtin')
    default:
      return source
  }
}

// 保存结果 → 页面提示。项目模板是未提交改动，必须醒目地说「提交到仓库才算数」并给出命令。
export interface SaveNotice {
  tone: 'ok' | 'warn'
  text: string
  command: string
}

export function templateSaveNotice(r: WorkflowTemplateWriteResult): SaveNotice {
  const where = r.path || t('util.wf.save.scopeName', { scope: r.scope })
  if (r.uncommitted) {
    return {
      tone: 'warn',
      text: t('util.wf.save.uncommitted', { where }),
      command: r.commit_command ?? '',
    }
  }
  const scope = r.scope === 'global' ? t('util.wf.source.global') : r.scope
  return {
    tone: 'ok',
    text: t('util.wf.save.saved', { scope, where }),
    command: '',
  }
}

// 删除结果 → 提示；删除后名字回落到下一层（全局 / 内置）要说清楚。
export function templateDeleteNotice(r: WorkflowTemplateWriteResult): string {
  const base = r.uncommitted ? t('util.wf.delete.uncommitted', { name: r.name }) : t('util.wf.delete.done', { name: r.name })
  if (!r.falls_back_to) return t('util.wf.delete.gone', { base })
  return t('util.wf.delete.fallback', { base, source: sourceLabel(r.falls_back_to) })
}
