// 工作流列表页（/workflows）与模板详情页的纯逻辑：分组、可编辑判定、深链。
// 页面本身只做接线；这里不碰网络与路由实例，便于单测。
import type { WorkflowTemplateInfo } from '../api/types'

export type TemplateLayer = 'project' | 'global' | 'builtin'

// 列表里的分组顺序：项目 → 全局 → 内置（越靠近项目越先看到）。
export const TEMPLATE_LAYERS: readonly TemplateLayer[] = ['project', 'global', 'builtin']

export interface TemplateGroup {
  layer: TemplateLayer
  items: WorkflowTemplateInfo[]
}

// 未知来源（后端以后新增的层）按内置处理，不让模板从列表里消失。
export function layerOf(tpl: Pick<WorkflowTemplateInfo, 'source'>): TemplateLayer {
  return tpl.source === 'project' || tpl.source === 'global' ? tpl.source : 'builtin'
}

// 按来源分组；空组不返回。组内保持接口给的顺序。
export function groupTemplates(list: readonly WorkflowTemplateInfo[]): TemplateGroup[] {
  return TEMPLATE_LAYERS.map((layer) => ({ layer, items: list.filter((tpl) => layerOf(tpl) === layer) })).filter(
    (g) => g.items.length > 0,
  )
}

// 只有全局 / 项目文件能改能删；内置没有文件（同名保存是「覆盖」，在向导里做）。
export function isEditableLayer(source: string | undefined): source is 'project' | 'global' {
  return source === 'project' || source === 'global'
}

// 模板的执行次数（接口缺省按 0）。
export function runCount(tpl: Pick<WorkflowTemplateInfo, 'runs'>): number {
  return tpl.runs && tpl.runs > 0 ? tpl.runs : 0
}

// 变量名列表：必填的带 * 标记，按名字排序（Go map 无序，页面不能跟着抖）。
export function varNames(tpl: Pick<WorkflowTemplateInfo, 'spec'>): { name: string; required: boolean }[] {
  const vars = tpl.spec?.vars ?? {}
  return Object.keys(vars)
    .sort()
    .map((name) => ({ name, required: !!vars[name]?.required }))
}

// 深链。名字走 encodeURIComponent，项目只在项目模板上带。
export function runLink(name: string, project?: string): string {
  const q = new URLSearchParams({ template: name })
  if (project) q.set('project', project)
  return `/workflows/new?${q.toString()}`
}

export function templateLink(name: string, project?: string): string {
  const q = project ? `?project=${encodeURIComponent(project)}` : ''
  return `/workflows/templates/${encodeURIComponent(name)}${q}`
}

export function runsLink(name: string): string {
  return `/workflows/runs?template=${encodeURIComponent(name)}`
}

// 取 ?template= / ?project=（vue-router 的 query 可能是数组或 null）。
export function queryString(v: unknown): string {
  const x = Array.isArray(v) ? v[0] : v
  return typeof x === 'string' ? x : ''
}
