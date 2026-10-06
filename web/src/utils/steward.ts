// 管家（W2b）前端的纯逻辑：状态文案、agent 下拉过滤、笔记冲突处理、快捷问题、
// 面板订阅切换。组件只负责渲染与调用，这里的函数可以脱离 DOM 直接测试。
import type { AgentInfo } from '../api/types'
import type { MergeSuggestion, StewardNotes, StewardSettings, StewardState, StewardStatus } from '../api/steward'

export const QUICK_QUESTIONS = ['我手上还有什么没完成？', '今天去现场要做什么？', '把等资源的整理成清单'] as const

export function stateLabel(s: StewardState | string, enabled = true): string {
  if (!enabled) return '未启用'
  switch (s) {
    case 'running':
      return '运行中'
    case 'idle':
      return '空闲'
    default:
      return '未启动'
  }
}

// 设置页 agent 下拉：只列已安装（available）的 acp-agent；当前值不在其中也要列出，
// 免得保存时被悄悄换掉，并标注原因。
export interface AgentOption {
  key: string
  label: string
  missing?: boolean
}

export function acpAgentOptions(agents: AgentInfo[], current: string): AgentOption[] {
  const out: AgentOption[] = agents
    .filter((a) => a.type === 'acp-agent' && a.available)
    .map((a) => ({ key: a.key, label: a.version ? `${a.key}（${a.version}）` : a.key }))
    .sort((a, b) => a.key.localeCompare(b.key))
  if (current && !out.some((o) => o.key === current)) {
    const known = agents.find((a) => a.key === current)
    const why = !known ? '未找到' : known.type !== 'acp-agent' ? '不是 acp-agent' : '本机不可用'
    out.unshift({ key: current, label: `${current}（${why}）`, missing: true })
  }
  return out
}

// 只发有改动的字段。
export function settingsDiff(
  loaded: StewardSettings,
  form: { enabled: boolean; agent: string; project: string; review_time: string; idle_end_min: string; event_wake: boolean },
): Partial<Omit<StewardSettings, 'review_time_explicit'>> {
  const d: Partial<Omit<StewardSettings, 'review_time_explicit'>> = {}
  if (form.enabled !== loaded.enabled) d.enabled = form.enabled
  if (form.agent.trim() !== loaded.agent) d.agent = form.agent.trim()
  const proj = form.project.trim()
  if (proj !== (loaded.project === 'default' ? '' : loaded.project)) d.project = proj
  const rt = form.review_time.trim()
  const loadedRt = loaded.review_time_explicit ? loaded.review_time : ''
  if (rt !== loadedRt) d.review_time = rt
  const idle = Number.parseInt(form.idle_end_min, 10)
  if (Number.isFinite(idle) && idle !== loaded.idle_end_min) d.idle_end_min = idle
  if (form.event_wake !== loaded.event_wake) d.event_wake = form.event_wake
  return d
}

export function agentSwitchWarning(status: StewardStatus | null, newAgent: string): string {
  if (!status || status.state === 'not_started') return ''
  if (newAgent.trim() === (status.job_agent || status.agent)) return ''
  return '当前管家会话会结束，下次需要时用新 agent 重建。'
}

export function notesSizeLabel(bytes: number): string {
  return bytes >= 1024 ? `${(bytes / 1024).toFixed(1)}KB` : `${bytes}B`
}

// 保存笔记冲突（409）：服务端返回 current。给编辑器两个选择：采用最新版继续合并，
// 或在最新版本号上覆盖保存（保留我的文字）。
export interface NotesConflict {
  current: StewardNotes
  mine: string
}

export function notesConflictOf(err: unknown, mine: string): NotesConflict | null {
  const e = err as { status?: number; body?: { current?: StewardNotes } } | null
  if (!e || e.status !== 409) return null
  const cur = e.body?.current
  return { current: cur ?? { version: 0, body: '' }, mine }
}

// 面板订阅：当前订阅的 job 与 ask / status 报告的 job 不同 = 需要重新订阅；
// 曾经订阅过别的 job（非空）= 管家被重建，提示用户。
export interface Subscription {
  jobId: string
  resubscribe: boolean
  rebuilt: boolean
}

export function nextSubscription(current: string, reported: string | undefined): Subscription {
  const next = reported ?? ''
  if (!next || next === current) return { jobId: current, resubscribe: false, rebuilt: false }
  return { jobId: next, resubscribe: true, rebuilt: current !== '' }
}

export function mergeSuggestionText(sg: MergeSuggestion, titleOf: (id: string) => string): string {
  const base = `管家建议把「${titleOf(sg.source_id)}」并入「${titleOf(sg.target_id)}」`
  return sg.reason ? `${base}：${sg.reason}` : base
}

export const BUSY_TEXT = '管家正忙，稍后再问'

// 管家会话里的 prompt 对用户不友好：首轮是 24KB 的 prime（角色说明 + 笔记 + 工作项清单）+ 问题，
// 巡检是系统指令。面板只显示用户真正说的话；系统发的折成一行说明。
const PRIME_HEAD = '# 你是工作管家'
const PRIME_SEP = '\n\n---\n\n'
const READY_LINE_HEAD = '读完以上内容后'

export function displayPrompt(text: string): string {
  if (text.startsWith(PRIME_HEAD)) {
    const i = text.lastIndexOf(PRIME_SEP)
    const rest = i >= 0 ? text.slice(i + PRIME_SEP.length) : ''
    const started = '（管家已启动：已注入角色说明、笔记和工作项清单）'
    if (!rest || rest.startsWith(READY_LINE_HEAD)) return started
    return `${started}\n${displayPrompt(rest)}`
  }
  const ask = /^## 来自 [^\n]* 的提问\n\n([\s\S]*?)\n\n（先用 gofer_work_list/.exec(text)
  if (ask) return ask[1]
  const sys = /^## (每日巡检|手动巡检|事件整理)[^\n]*/.exec(text)
  if (sys) return `（系统）${sys[0].replace(/^## /, '')}`
  return text
}
