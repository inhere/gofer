// 「从此会话新开」（gofer-ldmp）：用一个已结束 job / 已登记会话的 session id 开一个继承其上下文的
// 【新】会话（POST /v1/jobs 的 from_session，即 CLI 的 `job run --from-session`）。
// 能力来自 /v1/agents 的 from_session（cli-agent 配了 from_session_args）；源 agent 不支持时
// 在同会话族里找一个支持的，找不到就灰显并写原因（后端对不支持的 agent 同样 400）。
import type { AgentInfo } from '../api/types'

export interface FromSessionSource {
  // 源 job 的 agent（续接载体 exec 时传 resume_agent）/ 会话登记的 agent
  agent: string
  sessionId?: string
  project?: string
  runner?: string
  cwd?: string
}

export interface FromSessionChoice {
  // 没有 session id 时不显示入口
  visible: boolean
  disabled: boolean
  // 不可用原因，或改用同族 agent 的说明
  note: string
  // 新 job 用的 agent（源 agent 或同族的另一个）
  agent: string
}

function supports(a: AgentInfo): boolean {
  // 旧 server 无该字段 = 未知，按可用处理（与续接能力一致；真不支持时后端 400 给原因）
  return a.type === 'cli-agent' && a.from_session !== false
}

export function fromSessionChoice(src: FromSessionSource, agents: AgentInfo[]): FromSessionChoice {
  if (!src.sessionId) return { visible: false, disabled: true, note: '', agent: '' }
  const self = agents.find((a) => a.key === src.agent)
  // agents 还没取到 / 源 agent 已不在配置里：不预判，按源 agent 放行
  if (agents.length === 0) return { visible: true, disabled: false, note: '', agent: src.agent }
  if (self && supports(self)) return { visible: true, disabled: false, note: '', agent: self.key }
  const family = self?.session_family ?? ''
  const alt = family === ''
    ? undefined
    : agents
      .filter((a) => a.key !== src.agent && a.session_family === family && a.available && supports(a))
      .sort((a, b) => a.key.localeCompare(b.key))[0]
  if (alt) return { visible: true, disabled: false, note: `改用 ${alt.key} 新开（同会话族）`, agent: alt.key }
  const who = self ? `agent ${self.key}` : `agent ${src.agent}（未在配置中）`
  return {
    visible: true,
    disabled: true,
    note: `${who} 未配置 from_session_args，同会话族也没有可用的 agent，不能从会话新开`,
    agent: '',
  }
}

// cwd 只在是项目相对路径时带上（会话登记的往往是执行机上的绝对路径，提交会被拒）。
function relativeCwd(cwd?: string): string {
  const c = (cwd ?? '').trim()
  if (c === '' || c.startsWith('/') || c.startsWith('\\') || /^[A-Za-z]:/.test(c) || c.startsWith('~')) return ''
  return c
}

// 跳转新建 job 表单的 query：agent / project / runner / cwd 来自源，from_session = 源会话 id。
export function fromSessionQuery(src: FromSessionSource, choice: FromSessionChoice): Record<string, string> {
  const q: Record<string, string> = { from_session: src.sessionId ?? '' }
  if (choice.agent) q.agent = choice.agent
  if (src.project) q.project = src.project
  if (src.runner) q.runner = src.runner
  const cwd = relativeCwd(src.cwd)
  if (cwd) q.cwd = cwd
  return q
}

// 新建表单读回 ?from_session=（裁空白、限长与后端 maxFromSessionLen 一致）。
export function readFromSessionQuery(v: unknown): string {
  if (typeof v !== 'string') return ''
  const s = v.trim()
  return s.length > 200 ? '' : s
}

// 提交时附带 from_session：只有 cli-agent 有意义（exec / acp-agent 后端必拒）。
export function withFromSession<T extends { from_session?: string }>(req: T, fromSession: string, isCliAgent: boolean): T {
  if (fromSession !== '' && isCliAgent) req.from_session = fromSession
  return req
}

// 新建表单带 from_session 时的前置校验（后端同样会拒，这里先给出能照做的原因）。
// agentFromSession 来自 /v1/agents；undefined = 未知（旧 server / 未取到），不拦。
export function fromSessionFormError(
  fromSession: string,
  form: { agentType: string; agentFromSession?: boolean; continuousSession: boolean },
): string {
  if (fromSession === '') return ''
  if (form.continuousSession) return '从会话新开不支持持续 ACP 会话，请取消「持续会话」'
  if (form.agentType !== '' && form.agentType !== 'cli-agent') return '从会话新开只支持 cli-agent（需配置 from_session_args）'
  if (form.agentFromSession === false) return '所选 agent 未配置 from_session_args，不能从会话新开'
  return ''
}
