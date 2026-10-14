// 会话归属被拒（gofer-eh53）：多调用方 server（server.callers）里会话属于注册它的
// caller。切 token 后终端的 hook 以另一个身份上报，owner 校验会（正确地）拒掉，于是
// 状态 / 最新消息 / 进度停更。Sessions 列表与 SessionDrawer 共用这里的文案与动作。
import { ApiError, setSessionOwner } from '../api/client'
import type { AgentSession } from '../api/types'

export type RefusedOwnerSession = Pick<AgentSession, 'caller_id' | 'refused_caller_id' | 'refused_at'>

// refusedOwnerNotice：有被拒记录时的一句话；没有记录时返回 ''（不渲染提示）。
export function refusedOwnerNotice(s: RefusedOwnerSession): string {
  const refused = s.refused_caller_id ?? ''
  if (!refused) return ''
  return `hook 以 ${refused} 身份上报被拒（会话属于 ${s.caller_id || '（无）'}），状态已停更`
}

// refusedOwnerHelp：为什么、怎么办（悬停说明）。
export function refusedOwnerHelp(s: RefusedOwnerSession): string {
  return `${refusedOwnerNotice(s)}。切 token 后终端的 hook 换了身份，注册与心跳都归当前 owner 所有，所以它的上报都被拒了。管理员把会话转给 ${s.refused_caller_id}（需要 can_admin），心跳就会重新落下。`
}

// transferOwnerLabel：「转给 agent-dev」按钮的文字；无被拒记录时为空。
export function transferOwnerLabel(s: RefusedOwnerSession): string {
  return s.refused_caller_id ? `转给 ${s.refused_caller_id}` : ''
}

// transferSessionOwner：把会话转给 caller（POST /v1/sessions/{sid}/owner）。
// call 可注入，便于测试断言调用参数；失败抛错，原因用 transferFailText 显示。
export function transferSessionOwner(
  sid: string,
  callerId: string,
  call: (sid: string, callerId: string) => Promise<AgentSession> = setSessionOwner,
): Promise<AgentSession> {
  return call(sid, callerId)
}

// transferFailText：服务端 detail 已是可读原因（例如「caller lacks can_admin
// capability」），优先原样显示。
export function transferFailText(e: unknown): string {
  if (e instanceof ApiError) return e.detail || e.message
  return e instanceof Error ? e.message : String(e)
}
