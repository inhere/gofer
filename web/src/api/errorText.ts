// 服务端错误体的文案：带稳定 `code` 的错误，句子由前端字典的 `err.<code>` 按当前界面语言
// 渲染（占位符取值来自错误体的 `params`）；字典里没有这个 code（旧版服务端、没迁移的错误），
// 就显示服务端给的 detail。`error` 字段是固定短语（脚本 / MCP 依赖），不翻译。
import { t, te } from '../i18n'

export type ErrorParams = Record<string, unknown>

// 占位符只认字符串 / 数字（插值是 {name} 文本替换）；其余类型丢弃。
function placeholderParams(params?: ErrorParams): Record<string, string | number> | undefined {
  if (!params) return undefined
  const out: Record<string, string | number> = {}
  for (const [k, v] of Object.entries(params)) {
    if (typeof v === 'string' || typeof v === 'number') out[k] = v
  }
  return out
}

// localizeErrorDetail 返回错误要显示的 detail：字典里有 err.<code> 就用它渲染，否则原样返回
// 服务端的 detail（可能是 undefined / 空串）。
export function localizeErrorDetail(code: string | undefined, params: ErrorParams | undefined, detail: string | undefined): string | undefined {
  if (code) {
    const key = `err.${code}`
    if (te(key)) return t(key, placeholderParams(params))
  }
  return detail
}
