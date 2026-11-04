// 用户管理页（设置 → 用户）的纯逻辑：表单 → 上限块、行状态 → 可用动作、上限摘要。
// 组件只取数与渲染，判定都放这里，便于单测（口径与后端 internal/config/caller_admin.go、
// internal/httpapi/callers_handler.go 一致）。
import { t } from '../i18n'
import { ceilingSummary } from './ceiling'
import type { AuthCeiling } from './permissions'
import type { CallerCeilingView, CallerView } from '../api/types'

// 上限的两种形态：给一块上限，或明确「不设上限」（服务端要求二选一，留空是错误而非不限）。
export type CeilingMode = 'limited' | 'none'

// CallerCeilingForm 是上限表单：四个 glob 列表按逗号 / 空白 / 换行拆成数组，三个开关是
// 「写不写这条限制」（不勾 = 这一维按严格默认处理，见后端 ceiling.go）。
export interface CallerCeilingForm {
  projects: string
  runners: string
  workers: string
  agents: string
  allowExec: boolean
  globalMemory: boolean
  readOnly: boolean
}

export function emptyCeilingForm(): CallerCeilingForm {
  return { projects: '', runners: '', workers: '', agents: '', allowExec: false, globalMemory: false, readOnly: false }
}

// splitPatterns 把一行 / 多行文本拆成 glob 列表（逗号、空格、换行都算分隔符）。
export function splitPatterns(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

// ceilingFromForm 把表单折成请求体里的 ceiling；四个列表与三个开关都没填时返回 null
// （服务端把「空上限块」当错误，界面上要改用 no_ceiling 明确表示不设上限）。
export function ceilingFromForm(f: CallerCeilingForm): CallerCeilingView | null {
  const out: CallerCeilingView = {}
  const projects = splitPatterns(f.projects)
  const runners = splitPatterns(f.runners)
  const workers = splitPatterns(f.workers)
  const agents = splitPatterns(f.agents)
  if (projects.length > 0) out.projects = projects
  if (runners.length > 0) out.runners = runners
  if (workers.length > 0) out.workers = workers
  if (agents.length > 0) out.agents = agents
  if (f.allowExec) out.allow_exec = true
  if (f.globalMemory) out.global_memory = true
  if (f.readOnly) out.read_only = true
  return Object.keys(out).length > 0 ? out : null
}

// ceilingToForm 是反向：把现有上限填回表单（编辑时用）。
export function ceilingToForm(c?: CallerCeilingView | null): CallerCeilingForm {
  const join = (list?: string[]): string => (list ?? []).join(', ')
  return {
    projects: join(c?.projects),
    runners: join(c?.runners),
    workers: join(c?.workers),
    agents: join(c?.agents),
    allowExec: c?.allow_exec === true,
    globalMemory: c?.global_memory === true,
    readOnly: c?.read_only === true,
  }
}

// asAuthCeiling 把视图里的上限折成 whoami 用的形状（三个开关的严格默认与后端一致），
// 好复用 utils/ceiling.ts 的摘要文案，不再写第二套。
export function asAuthCeiling(c?: CallerCeilingView | null): AuthCeiling {
  return {
    projects: c?.projects ?? [],
    runners: c?.runners ?? [],
    workers: c?.workers ?? [],
    agents: c?.agents ?? [],
    allow_exec: c?.allow_exec === true,
    global_memory: c?.global_memory === true,
    read_only: c?.read_only === true,
  }
}

// ceilingSummaryLines：一行一维的上限摘要（没有上限时为空）。
export function ceilingSummaryLines(c?: CallerCeilingView | null): string[] {
  return c ? ceilingSummary(asAuthCeiling(c)) : []
}

// ceilingModeText：上限形态的短标签。
export function ceilingModeText(c?: CallerCeilingView | null): string {
  return c ? t('settings.callers.ceilingLimited') : t('settings.callers.ceilingNone')
}

// quotaText：并发 / 限流 / 突发的紧凑写法（0 = 用 server.governance 的默认）。
export function quotaText(v: CallerView): string {
  const dash = (n: number | undefined): string => (n && n > 0 ? String(n) : '-')
  return `${dash(v.max_concurrent_jobs)} / ${dash(v.rate_limit)} / ${dash(v.rate_burst)}`
}

// stateText / stateTone：行的状态（active / disabled / superseded）。
export function stateText(state: string): string {
  switch (state) {
    case 'disabled':
      return t('settings.callers.state.disabled')
    case 'superseded':
      return t('settings.callers.state.superseded')
    default:
      return t('settings.callers.state.active')
  }
}

export function stateTone(state: string): 'ok' | 'off' | 'warn' {
  if (state === 'disabled') return 'off'
  if (state === 'superseded') return 'warn'
  return 'ok'
}

// sourceText：条目来源（配置文件 / 数据库）。
export function sourceText(source: string): string {
  return source === 'db' ? t('settings.callers.source.db') : t('settings.callers.source.file')
}

// isFileCaller：配置文件里声明的用户，控制台不能轮换 / 停用 / 启用（只能改上限与配额）。
export function isFileCaller(v: CallerView): boolean {
  return v.source !== 'db'
}

export function canRotate(v: CallerView): boolean {
  return !isFileCaller(v)
}

export function canDisable(v: CallerView): boolean {
  return !isFileCaller(v) && v.state === 'active'
}

export function canEnable(v: CallerView): boolean {
  return !isFileCaller(v) && v.state !== 'active'
}

// fileSourceNote：文件来源用户被禁用的原因（界面上的说明，不让人以为按钮坏了）。
export function fileSourceNote(): string {
  return t('settings.callers.fileNote')
}

// addBlocker 是「新增」表单的提交前校验：id 必填、上限二选一、选「受限」时至少填一项。
// 返回空串表示可以提交。
export function addBlocker(req: { id: string; mode: CeilingMode; form: CallerCeilingForm }): string {
  if (req.id.trim() === '') return t('settings.callers.err.id')
  if (req.mode === 'limited' && ceilingFromForm(req.form) === null) return t('settings.callers.err.ceilingEmpty')
  return ''
}

// ceilingBlocker 是「改上限」表单的同一套校验（name / id 不变，只改上限）。
export function ceilingBlocker(mode: CeilingMode, form: CallerCeilingForm): string {
  if (mode === 'limited' && ceilingFromForm(form) === null) return t('settings.callers.err.ceilingEmpty')
  return ''
}

// limitBlocker 校验配额输入：三个数都必须是非负整数（0 = 用默认）。
export function limitBlocker(maxConcurrent: string, rateLimit: string, rateBurst: string): string {
  for (const [raw, integer] of [
    [maxConcurrent, true],
    [rateLimit, false],
    [rateBurst, true],
  ] as const) {
    const text = raw.trim()
    if (text === '') continue
    const n = Number(text)
    if (!Number.isFinite(n) || n < 0 || (integer && !Number.isInteger(n))) {
      return t('settings.callers.err.limits')
    }
  }
  return ''
}
