// 统一对话时间线的条目模型（设计稿 §2「显示统一」，计划 R12）。
//
// 三处对话显示（工作台 ConversationView、job 详情「消息」、会话抽屉 SessionDrawer）各写
// 过一套模板：工作台 assistant 走 markdown、用户消息是纯文本；job 详情整段 `<pre>`；抽屉
// 里 agent 消息 markdown、人发的消息 `<pre>`。这里把各处的原始数据先规整成同一种条目，
// 由 components/ConversationTimeline.vue 一处渲染（markdown、工具调用折叠、注入折叠、
// 交互卡内嵌、未发送提示），调用方只负责「气泡抬头的措辞」这种页面自己的话术。
//
// 条目只描述「显示什么」，不带 DOM：折叠状态、滚动都由组件自己管。
import type { AttachmentRef, Interaction, JobMessage } from '../api/types'
import {
  permissionOutcome,
  reduceACPEvents,
  timelineBlocks,
  type ACPEvent,
  type ACPTextEvent,
  type ACPToolEvent,
} from '../components/workbench/acpEvents'

// 谁说的话。user 靠右（人的消息），agent 靠左。
export type ConversationSpeaker = 'user' | 'agent'

export interface ConversationMessageEntry {
  // user / agent 两种消息走同一套渲染，只有抬头、对齐与气泡底色不同。
  kind: ConversationSpeaker
  id: string
  text: string
  // label 是气泡抬头（「你」/「助手」/ agent 名 / 会话署名）：各页措辞不同，由调用方给。
  label?: string
  // time 是已经格式化好的时间（「12:03」），timeTitle 是完整时间（悬停提示）。
  time?: string
  timeTitle?: string
  // jobId：这条消息属于另一轮 job 时给一个可点的短 id（job 详情跨轮消息用）。
  jobId?: string
  attachments?: AttachmentRef[]
  truncated?: boolean
  // 服务端因为太长跳过了更早的消息（job 详情），在抬头行标一笔。
  omitted?: boolean
}

export interface ConversationToolCall {
  id: string
  title: string
  summary?: string
  status?: string
}

export interface ConversationToolsEntry {
  // 连续的工具调用合成一组，默认折叠成「N 个工具调用」。
  kind: 'tools'
  id: string
  calls: ConversationToolCall[]
  failed: number
}

export interface ConversationPermissionEntry {
  // ACP 权限决策（已决）：内嵌成一张只读小卡，停在它发生的位置。
  kind: 'permission'
  id: string
  outcome: 'allowed' | 'denied' | 'auto'
  title?: string
  reason?: string
}

export interface ConversationInteractionEntry {
  // 待作答 / 已作答的交互卡（job 交互、会话权限卡都适配成 Interaction 后走这里）。
  kind: 'interaction'
  id: string
  interaction: Interaction
  submitting?: boolean
  readonly?: boolean
}

export interface ConversationInjectedEntry {
  // gofer 注入的规则：默认折成一行，点开看全文。
  kind: 'injected'
  id: string
  text: string
}

export interface ConversationUnsentEntry {
  // 这一轮没发出去（等目录锁超时等）：醒目提示 + 原消息 + 重发。
  kind: 'unsent'
  id: string
  // text 是服务端给的说明（「未发送：目录被 xxx 占用…」）。
  text: string
  // prompt 是被拦下的原消息，重发时填回输入框。
  prompt: string
}

export interface ConversationCustomEntry {
  // 调用方自己渲染的一条（会话抽屉的终端轮次等）：组件只留位置，内容走 #custom 插槽。
  kind: 'custom'
  id: string
}

export type ConversationEntry =
  | ConversationMessageEntry
  | ConversationToolsEntry
  | ConversationPermissionEntry
  | ConversationInteractionEntry
  | ConversationInjectedEntry
  | ConversationUnsentEntry
  | ConversationCustomEntry

export type ConversationMessageEntryKind = ConversationSpeaker

// 条目 id 在同一份时间线里要唯一（v-for 的 key）；同一轮里同一类事件用 seq 区分。
export function conversationEntryKey(entry: ConversationEntry): string {
  return `${entry.kind}:${entry.id}`
}

export interface ConversationLabels {
  // 人的消息抬头（工作台「你」、job 详情「发给 agent」、抽屉「经转达」）。
  user: string
  // agent 消息抬头（工作台「助手」、job 详情「agent 回复」、抽屉「<会话名> · 回复」）。
  agent: string
}

function toolCall(tool: ACPToolEvent): ConversationToolCall {
  const first = tool.locations?.[0]?.path
  const summary = first || (tool.raw_input ? tool.raw_input.slice(0, 80) : '')
  return {
    id: tool.tool_call_id,
    title: tool.title || tool.tool_kind || 'tool',
    summary,
    status: tool.status,
  }
}

// acpEventEntries：一条 ACP 结构化流 → 条目列表。
//
// 先 reduceACPEvents（工具调用的多次更新合并成一条，思考 / 截断相邻合并），再按
// timelineBlocks 的次序出：用户消息、注入规则、agent 消息、工具调用组、权限决策。
export function acpEventEntries(
  events: readonly ACPEvent[],
  labels: ConversationLabels,
  time?: (event: ACPTextEvent) => string,
): ConversationEntry[] {
  const entries: ConversationEntry[] = []
  for (const block of timelineBlocks(reduceACPEvents(events))) {
    if (block.kind === 'text') {
      const speaker: ConversationSpeaker = block.event.kind === 'prompt' ? 'user' : 'agent'
      entries.push({
        kind: speaker,
        id: `${speaker}-${block.event.seq}`,
        text: block.event.text,
        label: speaker === 'user' ? labels.user : labels.agent,
        time: time?.(block.event),
        attachments: block.event.attachments ?? [],
        truncated: block.event.truncated,
      })
      // 服务端把注入的规则从 prompt 正文里拆了出来（acp 流的 injected 字段），排在
      // 用户消息后面折成一行——不再占满一屏（设计稿 §2）。
      if (speaker === 'user' && block.event.injected) {
        entries.push({ kind: 'injected', id: `injected-${block.event.seq}`, text: block.event.injected })
      }
      continue
    }
    if (block.kind === 'permission') {
      entries.push({
        kind: 'permission',
        id: `permission-${block.event.seq}`,
        outcome: permissionOutcome(block.event),
        title: block.event.title,
        reason: block.event.reason,
      })
      continue
    }
    if (block.events.length > 0) {
      entries.push({
        kind: 'tools',
        id: `tools-${block.events[0].seq}`,
        calls: block.events.map(toolCall),
        failed: block.failed,
      })
    }
  }
  return entries
}

// jobMessageEntries：job 详情「消息」接口的列表 → 条目列表。
//
// 服务端已经按时间升序排好、也已经把注入规则拆到 injected 字段（httpapi/job_messages.go），
// 前端不重排、不再解析规则格式。
export function jobMessageEntries(
  messages: readonly JobMessage[],
  labels: ConversationLabels,
  time: (at?: number) => string,
): ConversationEntry[] {
  const entries: ConversationEntry[] = []
  messages.forEach((message, index) => {
    const id = `${message.role}-${message.job_id ?? '-'}-${index}`
    entries.push({
      kind: message.role,
      id,
      text: message.text,
      label: message.role === 'user' ? labels.user : labels.agent,
      time: time(message.at),
      jobId: message.job_id,
      truncated: message.truncated,
      omitted: message.omitted,
    })
    if (message.injected) {
      entries.push({ kind: 'injected', id: `injected-${id}`, text: message.injected })
    }
  })
  return entries
}

// interactionEntries：待作答 / 已作答的交互卡 → 条目列表（内嵌在对话对应位置）。
export function interactionEntries(
  items: readonly Interaction[],
  submitting: ReadonlySet<string> = new Set(),
  readonly = false,
): ConversationEntry[] {
  return items.map((item) => ({
    kind: 'interaction',
    id: item.id,
    interaction: item,
    submitting: submitting.has(item.id),
    readonly,
  }))
}

// turnUnsentEntry：ACP 流里「这一轮没发出去」的记录 → 未发送提示条目。
//
// 会话等目录锁超时时服务端不再执行这一轮，而是回一条 {t: turn_unsent, text, prompt}
// （internal/runner/acp/runner.go）：text 是给人看的说明，prompt 是原消息。原消息留着，
// 点「重发」由父组件填回输入框。
export function turnUnsentEntry(payload: { text?: string; prompt?: string }, id = 'turn-unsent'): ConversationUnsentEntry {
  return {
    kind: 'unsent',
    id,
    text: payload.text ?? '',
    prompt: payload.prompt ?? '',
  }
}

// isTurnUnsent：acp 流里认得这条记录。服务端按 acp.jsonl 的 t 列原样转发成事件
// （kind = t），也容忍直接以 frame 类型下发的形态。
export function isTurnUnsent(value: unknown): value is { kind?: string; t?: string; text?: string; prompt?: string } {
  if (typeof value !== 'object' || value == null) return false
  const candidate = value as { kind?: unknown; t?: unknown }
  return candidate.kind === 'turn_unsent' || candidate.t === 'turn_unsent'
}
