// 定时任务「运行健康 + 前置检查 + 跨轮上下文」的表单辅助（设计 2026-11-12 §1/§2/§3）：
// 把表单状态换成创建 / PATCH 的提交体，并把服务端的 403（设置前置检查需要 schedule.precheck）
// 与 409（上一轮还没结束、overlap=skip）翻成人话。纯函数，便于单测；控件在 Schedules.vue /
// NewSchedule.vue。
import { ApiError } from '../api/client'
import type { CreateScheduleReq, PatchScheduleReq, Schedule, SchedulePrecheck } from '../api/types'
import { t } from '../i18n'

export type ScheduleOverlap = 'skip' | 'allow'
export type ScheduleContextMode = 'on' | 'off'

// 表单里的健康字段：数字用字符串（<input> 的原文，空 = 后端默认），argv 多行文本一行一个参数。
export interface ScheduleHealthForm {
  overlap: ScheduleOverlap
  maxRuns: string
  maxFailures: string
  context: ScheduleContextMode
  precheckArgv: string
  precheckTimeout: string
  precheckSkipUnchanged: boolean
}

// 新建调度的默认值（与后端 handleCreateSchedule 一致）：叠跑跳过、连续失败 3 次暂停、
// 不限次数、跨轮上下文开、无前置检查。
export const NEW_SCHEDULE_HEALTH: ScheduleHealthForm = {
  overlap: 'skip',
  maxRuns: '0',
  maxFailures: '3',
  context: 'on',
  precheckArgv: '',
  precheckTimeout: '',
  precheckSkipUnchanged: false,
}

// argvLines：argv 数组回显成多行文本（一行一个参数，参数里的空格因此不需要转义）。
export function argvLines(argv: string[] | undefined): string {
  return (argv ?? []).join('\n')
}

// parseArgvLines：把多行文本切成 argv。只按行切、不分割空格——前置检查不经 shell，
// 一个参数就是一个元素；只有空白的行丢弃。
export function parseArgvLines(raw: string): string[] {
  return raw
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line !== '')
}

// countValue：数字输入框的原文 → 非负整数；空 / 非法 / 负数一律 0（0 在这些字段上都表示「不限」）。
export function countValue(raw: string): number {
  const n = Number(String(raw).trim() || '0')
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
}

// scheduleHealthForm：编辑时把 schedule 回填进表单；没有运行健康字段的旧行按后端默认回显。
export function scheduleHealthForm(s: Schedule | null | undefined): ScheduleHealthForm {
  if (!s) return { ...NEW_SCHEDULE_HEALTH }
  return {
    overlap: s.overlap === 'skip' ? 'skip' : 'allow',
    maxRuns: String(s.max_runs ?? 0),
    maxFailures: String(s.max_failures ?? 0),
    context: s.context === 'off' ? 'off' : 'on',
    precheckArgv: argvLines(s.precheck?.argv),
    precheckTimeout: s.precheck?.timeout_sec ? String(s.precheck.timeout_sec) : '',
    precheckSkipUnchanged: s.precheck?.skip_unchanged === true,
  }
}

// schedulePrecheck：表单里的前置检查声明；argv 为空 = 没有前置检查（返回 undefined）。
// timeout_sec 省略时由后端取默认值，所以空输入不写这个字段。
export function schedulePrecheck(form: ScheduleHealthForm): SchedulePrecheck | undefined {
  const argv = parseArgvLines(form.precheckArgv)
  if (argv.length === 0) return undefined
  const out: SchedulePrecheck = { argv }
  const timeout = countValue(form.precheckTimeout)
  if (timeout > 0) out.timeout_sec = timeout
  out.skip_unchanged = form.precheckSkipUnchanged
  return out
}

// createHealthFields 是 POST /v1/schedules 里与运行健康有关的字段（无前置检查时不下发 precheck）。
export function createHealthFields(
  form: ScheduleHealthForm,
): Pick<CreateScheduleReq, 'overlap' | 'max_runs' | 'max_failures' | 'context' | 'precheck'> {
  const out: Pick<CreateScheduleReq, 'overlap' | 'max_runs' | 'max_failures' | 'context' | 'precheck'> = {
    overlap: form.overlap,
    max_runs: countValue(form.maxRuns),
    max_failures: countValue(form.maxFailures),
    context: form.context,
  }
  const precheck = schedulePrecheck(form)
  if (precheck) out.precheck = precheck
  return out
}

// precheckEdited 判断前置检查是否真的改过：没改就不进 PATCH 体——设置它要 schedule.precheck
// 权限，只改标题 / 超时的人不该被这条要求挡住。
export function precheckEdited(form: ScheduleHealthForm, baseline: ScheduleHealthForm): boolean {
  return (
    argvLines(parseArgvLines(form.precheckArgv)) !== argvLines(parseArgvLines(baseline.precheckArgv)) ||
    countValue(form.precheckTimeout) !== countValue(baseline.precheckTimeout) ||
    form.precheckSkipUnchanged !== baseline.precheckSkipUnchanged
  )
}

// patchHealthFields 是 PATCH /v1/schedules/{id} 的健康字段：前置检查改过才下发，
// 清空时发 null（后端对 null 不需要权限）。
export function patchHealthFields(
  form: ScheduleHealthForm,
  baseline: ScheduleHealthForm,
): Partial<PatchScheduleReq> {
  const out: Partial<PatchScheduleReq> = {
    overlap: form.overlap,
    max_runs: countValue(form.maxRuns),
    max_failures: countValue(form.maxFailures),
    context: form.context,
  }
  if (precheckEdited(form, baseline)) {
    out.precheck = schedulePrecheck(form) ?? null
  }
  return out
}

// apiStatus 取 HTTP 状态码；不是 ApiError（网络错误等）返回 0。
export function apiStatus(e: unknown): number {
  return e instanceof ApiError ? e.status : 0
}

// scheduleApiError 把两个和这批字段有关的拒绝翻成人话：403 = 没有 schedule.precheck 权限
// （只有带前置检查的请求会被这么拒），409 = 上一轮还没结束而调度设的是 skip。
// 其它错误返回 ''，调用方原样显示错误消息。
export function scheduleApiError(e: unknown, opts: { sendsPrecheck?: boolean } = {}): string {
  const status = apiStatus(e)
  if (status === 403 && opts.sendsPrecheck) {
    return t('schedules.errors.precheckForbidden')
  }
  if (status === 409) {
    return t('schedules.errors.overlapSkipped')
  }
  return ''
}
