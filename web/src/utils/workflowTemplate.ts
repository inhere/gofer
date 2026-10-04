// 「从模板新建 workflow」向导的纯逻辑（Z4）：变量类型推断、表单默认值与校验、步骤预览行。
//  - 变量类型：看变量被用在 spec 哪个字段（project_key / agent(s) / runner），
//    agent / runner / project 类用下拉（数据来自 meta），其余是文本；名字只作兜底。
//  - 提交值：空串不发（让后端用模板默认 / 项目默认）。
import type { MetaAgent, MetaProject, MetaRunner, WorkflowSpec, WorkflowStepSpec, WorkflowTemplateVar } from '../api/types'
import { projectRunnerOptions } from './runnerChoice'

export type VarKind = 'project' | 'agent' | 'runner' | 'text' | 'textarea'

const VAR_RE = /\$\{vars\.([A-Za-z_][A-Za-z0-9_]*)\}/g

function varsIn(text: string | undefined): string[] {
  if (!text) return []
  return [...text.matchAll(VAR_RE)].map((m) => m[1])
}

// 遍历 spec（含嵌套子 workflow）的步骤。
function eachStep(spec: WorkflowSpec | undefined, fn: (step: WorkflowStepSpec) => void): void {
  for (const step of spec?.steps ?? []) {
    fn(step)
    if (step.sub_workflow) eachStep(step.sub_workflow, fn)
  }
}

interface FanLike {
  agent?: string
  runner?: string
}

export function inferVarKinds(spec: WorkflowSpec): Record<string, VarKind> {
  const used: Record<string, Set<VarKind>> = {}
  const mark = (text: string | undefined, kind: VarKind) => {
    for (const name of varsIn(text)) (used[name] ??= new Set()).add(kind)
  }
  eachStep(spec, (step) => {
    mark(step.project_key, 'project')
    mark(step.agent, 'agent')
    for (const a of step.agents ?? []) mark(a, 'agent')
    mark(step.runner, 'runner')
    for (const f of ((step as unknown as { fan?: FanLike[] }).fan ?? [])) {
      mark(f.agent, 'agent')
      mark(f.runner, 'runner')
    }
  })
  const out: Record<string, VarKind> = {}
  for (const name of Object.keys(spec.vars ?? {})) {
    const kinds = used[name]
    if (kinds?.has('project')) out[name] = 'project'
    else if (kinds?.has('agent')) out[name] = 'agent'
    else if (kinds?.has('runner')) out[name] = 'runner'
    else if (name === 'project') out[name] = 'project'
    else if (name === 'runner' || name.endsWith('_runner')) out[name] = 'runner'
    else if (/(^|_)agent($|_)|^(planner|implementer|verifier|reviewer)$/.test(name)) out[name] = 'agent'
    else if (/^(task|prompt|desc|description|goal|target)$/.test(name)) out[name] = 'textarea'
    else out[name] = 'text'
  }
  return out
}

export interface VarField {
  name: string
  kind: VarKind
  required: boolean
  default: string
  desc: string
}

// 表单字段顺序：project → 必填 → 其余；同组内按名字稳定排序。
export function varFields(spec: WorkflowSpec): VarField[] {
  const kinds = inferVarKinds(spec)
  const rank = (f: VarField) => (f.kind === 'project' ? 0 : f.required ? 1 : 2)
  return Object.entries(spec.vars ?? {})
    .map(([name, v]: [string, WorkflowTemplateVar]) => ({
      name,
      kind: kinds[name] ?? 'text',
      required: !!v.required,
      default: v.default ?? '',
      desc: v.desc ?? '',
    }))
    .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
}

// 默认值：模板 default；project 类用当前选中的项目。
export function initialValues(fields: VarField[], project = ''): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of fields) out[f.name] = f.kind === 'project' ? project : f.default
  return out
}

// 必填校验：返回 变量名 → 错误说明。
export function validateVars(fields: VarField[], values: Record<string, string>): Record<string, string> {
  const errs: Record<string, string> = {}
  for (const f of fields) {
    if (f.required && !(values[f.name] ?? '').trim()) errs[f.name] = '必填'
  }
  return errs
}

// 提交 / 预览用的 vars：空串不发，只发声明过的变量。
export function cleanVars(fields: VarField[], values: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of fields) {
    const v = (values[f.name] ?? '').trim()
    if (v !== '') out[f.name] = f.kind === 'textarea' || f.kind === 'text' ? values[f.name] : v
  }
  return out
}

// agent 下拉：按项目 allowed_agents 过滤（空 = 不限）。当前值不在可选里时保留为一项，避免下拉空白。
export interface PickOption {
  value: string
  label: string
  disabled?: boolean
}

export function agentOptions(project: MetaProject | undefined, agents: MetaAgent[], current: string): PickOption[] {
  const allowed = project?.allowed_agents ?? []
  const list = allowed.length === 0 ? agents : agents.filter((a) => allowed.includes(a.key))
  const out: PickOption[] = list.map((a) => ({ value: a.key, label: `${a.key} · ${a.type}` }))
  if (current && !out.some((o) => o.value === current)) out.unshift({ value: current, label: `${current}（此项目未开放）` })
  return out
}

// runner 下拉：第一项是"项目默认"（空值），其余按 allowed_runners 过滤；不可用的灰显并写原因。
export function runnerPickOptions(
  project: MetaProject | undefined,
  runners: MetaRunner[],
  blocks: Record<string, { short: string }>,
  current: string,
  label: (r: MetaRunner) => string,
): PickOption[] {
  const out: PickOption[] = [{ value: '', label: '项目默认' }]
  for (const r of projectRunnerOptions(project, runners)) {
    const block = blocks[r.name]
    out.push({ value: r.name, label: block ? `${label(r)} · ${block.short}` : label(r), disabled: !!block })
  }
  if (current && !out.some((o) => o.value === current)) out.push({ value: current, label: `${current}（此项目未开放）` })
  return out
}

export function sourceLabel(source: string | undefined): string {
  switch (source) {
    case 'project':
      return '项目'
    case 'global':
      return '全局'
    case 'builtin':
      return '内置'
    default:
      return source || '—'
  }
}

export interface PreviewStep {
  index: number
  name: string
  who: string
  runner: string
  badges: string[]
  summary: string
}

// 渲染后的 spec → 预览行（每步一行：谁来做、什么模式、做什么）。
export function previewSteps(spec: WorkflowSpec): PreviewStep[] {
  return (spec.steps ?? []).map((st, i) => {
    const s = st as WorkflowStepSpec & { fan?: FanLike[]; review?: boolean; join?: string; fan_out?: number }
    const agents = s.agents?.length ? s.agents : s.fan?.length ? s.fan.map((f) => f.agent ?? '') : s.agent ? [s.agent] : []
    const badges: string[] = []
    if (agents.length > 1 || (s.fan_out ?? 0) > 1) badges.push(`并行 ×${agents.length > 1 ? agents.length : s.fan_out}`)
    if (s.join) badges.push(`join:${s.join}`)
    if (s.worktree) badges.push('worktree')
    if (s.read_only) badges.push('只读')
    if (s.review) badges.push('人工验收闸')
    if (s.type === 'workflow') badges.push('子工作流')
    const text = (s.prompt ?? (s.cmd ? s.cmd.join(' ') : '')).replace(/\s+/g, ' ').trim()
    return {
      index: i + 1,
      name: s.name || `step ${i + 1}`,
      who: agents.join(' / ') || '—',
      runner: s.runner ?? '',
      badges,
      summary: text.length > 160 ? `${text.slice(0, 160)}…` : text,
    }
  })
}
