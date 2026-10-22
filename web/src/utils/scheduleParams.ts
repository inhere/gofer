// 定时任务参数的表单辅助（设计 2026-10-22 §D5）：把 schedule.params 声明换算成
// 「立即执行」表单的初始值、控件类型与底部环境变量预览。纯函数，便于单测；
// 控件本身在 Schedules.vue。
import type { ScheduleParam } from '../api/types'

export type ParamControl = 'number' | 'toggle' | 'select' | 'text'

// paramControl 按声明类型选控件：int → 数字框，bool → 开关，choice → 下拉，其余文本。
export function paramControl(p: ScheduleParam): ParamControl {
  switch (p.type) {
    case 'int':
      return 'number'
    case 'bool':
      return 'toggle'
    case 'choice':
      return 'select'
    default:
      return 'text'
  }
}

// paramDefaults 是「立即执行」表单的初始值：每个参数都取声明里的默认值。
// 未声明默认值的参数填空串（后端提交时也按空串处理）。
export function paramDefaults(params: ScheduleParam[] | undefined): Record<string, string> {
  const out: Record<string, string> = {}
  for (const p of params ?? []) {
    out[p.name] = p.default ?? ''
  }
  return out
}

// hasParams 判断这个任务是否需要弹表单：没有参数的任务保持一键执行。
export function hasParams(params: ScheduleParam[] | undefined): boolean {
  return (params ?? []).length > 0
}

// boolValue 把表单里的开关状态换成 env 值（"1" / "0"），非 bool 参数只做回显。
export function boolValue(on: boolean): string {
  return on ? '1' : '0'
}

// envPreview 是表单底部「本次将设置的环境变量」：按声明顺序列出 NAME=value，
// 空值也列出来（脚本据此判断"未设置"与"空串"的差别）。
export function envPreview(
  params: ScheduleParam[] | undefined,
  values: Record<string, string>,
): { name: string; value: string }[] {
  return (params ?? []).map((p) => ({ name: p.name, value: values[p.name] ?? p.default ?? '' }))
}
