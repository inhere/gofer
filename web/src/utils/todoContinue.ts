// todo 的「接着上一项的会话做」（`continue_from`）在前端的纯逻辑：前序候选列表、默认选中、
// 卡片三段文案、请求取值与 400 文案。与 utils/planBoard 一样刻意不 import 任何模块，只描述
// 它需要的最小结构，便于直接用 node 跑断言。
//
// 服务端事实（internal/jobstore/todos.go、internal/httpapi/plan_handler.go）：
//  - todo.continue_from = 本项续接的前序 todo id；派发时接续条件全满足就在前序的 agent
//    会话里继续（条件不满足则退回新会话，把原因写进 continue_note）。
//  - todo.continued_by = 已经接走本项会话的后继 todo id（续接位的占用标记）。
//  - todo.continue_note = 最近一次派发【没有】续接的原因；真正续接成功或从未派发时为空。
//  - 创建 / 修改 todo 都接受 continue_from（键缺省 = 保持原值，显式空串 = 清除）。校验失败
//    返回 400，error 为 CONTINUE_ERROR、detail 为原因。
import { t as tr } from '../i18n'

export interface ContinueTodo {
  todo_id: string
  title: string
}

// 一条 todo 上与本功能有关的最小结构（Todo 多出来的字段结构上兼容）。
export interface ContinueTodoState extends ContinueTodo {
  continue_from?: string
  continued_by?: string
  continue_note?: string
}

// continueCandidates：可以选作「前序项」的其它待办 —— 同一个 plan 的清单顺序，去掉自己
// （服务端也拒绝指向自己）。空清单 = 这个 plan 里没有可接续的项，开关应禁用。
export function continueCandidates<T extends ContinueTodo>(
  todos: readonly T[],
  selfId: string,
): T[] {
  return todos.filter((t) => t.todo_id !== selfId)
}

// defaultContinueFrom：开关刚打开时的默认前序项。
//  - 新增（selfId 为空）：新条目追加在清单末尾，故「上一项」是列表的最后一项（plan 为空
//    时没有候选，返回 ''）；
//  - 编辑：取显示顺序里紧邻自己的前一项；自己就是第一项时没有「上一项」，退而取第一个
//    可选项（总比空选中强）。
export function defaultContinueFrom(todos: readonly ContinueTodo[], selfId: string): string {
  const candidates = continueCandidates(todos, selfId)
  if (candidates.length === 0) return ''
  if (selfId === '') return candidates[candidates.length - 1].todo_id
  const idx = todos.findIndex((t) => t.todo_id === selfId)
  return idx > 0 ? todos[idx - 1].todo_id : candidates[0].todo_id
}

// continueFromValue：表单开关 + 选中的前序项 → 写进请求的 continue_from 取值。
// '' = 清除（关掉开关），服务端据此释放旧的续接位。
export function continueFromValue(on: boolean, id: string): string {
  return on ? id.trim() : ''
}

export interface ContinuationLabels {
  // 本项设置的前序项：接续自 <前序标题>
  from: string
  // 最近一次派发退回了新会话：未接续：<原因>
  note: string
  // 本项的会话已被谁接走：已被 <后继标题> 接续
  by: string
}

export type ContinuationKind = 'from' | 'note' | 'by'

export interface ContinuationLine {
  kind: ContinuationKind
  text: string
}

// 前序 / 后继不在本页数据里时（被删掉、或只加载了一部分）退回显示 id —— 说清接续了谁，
// 比整行空着强。
function titleOf(id: string, todos: readonly ContinueTodo[]): string {
  return todos.find((t) => t.todo_id === id)?.title || id
}

// continuationLabels：一条 todo 上的三段接续文案，空串 = 不渲染那一行。三段可以同时出现
// （设置前序项但最近一次派发退回了新会话）。
export function continuationLabels(
  t: ContinueTodoState,
  todos: readonly ContinueTodo[],
): ContinuationLabels {
  const from = (t.continue_from ?? '').trim()
  const by = (t.continued_by ?? '').trim()
  const note = (t.continue_note ?? '').trim()
  return {
    from: from ? tr('plans.todo.cont.from', { title: titleOf(from, todos) }) : '',
    note: note ? tr('plans.todo.cont.note', { reason: note }) : '',
    by: by ? tr('plans.todo.cont.by', { title: titleOf(by, todos) }) : '',
  }
}

// continuationLines：卡片 / 列表行要渲染的行（顺序固定：接续自 → 未接续 → 已被接续）。
export function continuationLines(
  t: ContinueTodoState,
  todos: readonly ContinueTodo[],
): ContinuationLine[] {
  const l = continuationLabels(t, todos)
  const all: ContinuationLine[] = [
    { kind: 'from', text: l.from },
    { kind: 'note', text: l.note },
    { kind: 'by', text: l.by },
  ]
  return all.filter((x) => x.text !== '')
}

// 写入 continue_from 被服务端拒绝时 400 响应里的 error 文本（plan_handler 的 writeError）。
export const CONTINUE_ERROR = 'invalid continue_from'

// continueApiErrorText：这类 400 的原因（detail）要显示在表单里。不是这一类错误返回 ''，
// 调用方照旧走通用错误提示。
export function continueApiErrorText(e: unknown): string {
  const err = e as { code?: string; detail?: string } | null | undefined
  if (!err || err.code !== CONTINUE_ERROR) return ''
  const reason = (err.detail ?? '').trim()
  return reason ? tr('plans.todo.cont.invalidWith', { reason }) : tr('plans.todo.cont.invalid')
}
