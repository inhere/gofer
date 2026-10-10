export type ACPEventKind =
  | 'prompt'
  | 'message'
  | 'thought'
  | 'tool'
  | 'permission'
  | 'plan'
  | 'usage'
  | 'stop'
  | 'truncated'

export interface ACPEventBase {
  seq: number
  kind: ACPEventKind
  truncated?: boolean
}

export interface ACPTextEvent extends ACPEventBase {
  kind: 'prompt' | 'message' | 'thought'
  text: string
}

export interface ACPToolLocation {
  path: string
  line?: number
}

export interface ACPToolEvent extends ACPEventBase {
  kind: 'tool'
  tool_call_id: string
  title?: string
  tool_kind?: string
  status?: string
  raw_input?: string
  raw_output?: string
  locations?: ACPToolLocation[]
}

export interface ACPPermissionEvent extends ACPEventBase {
  kind: 'permission'
  tool_call_id?: string
  title?: string
  tool_kind?: string
  outcome?: string
  option_id?: string
  option_kind?: string
  auto?: boolean
  reason?: string
}

export interface ACPPlanEntry {
  content: string
  priority?: string
  status?: string
}

export interface ACPPlanEvent extends ACPEventBase {
  kind: 'plan'
  entries?: ACPPlanEntry[]
}

export interface ACPUsageEvent extends ACPEventBase {
  kind: 'usage'
  used?: number
  input_tokens?: number
  output_tokens?: number
  total_tokens?: number
  cost_usd?: number
  raw?: string
}

export interface ACPStopEvent extends ACPEventBase {
  kind: 'stop'
  stop_reason?: string
}

export interface ACPTruncatedEvent extends ACPEventBase {
  kind: 'truncated'
  skipped: number
}

export type ACPEvent =
  | ACPTextEvent
  | ACPToolEvent
  | ACPPermissionEvent
  | ACPPlanEvent
  | ACPUsageEvent
  | ACPStopEvent
  | ACPTruncatedEvent

export interface ACPRound {
  jobId: string
  events: ACPEvent[]
}

const ACP_KINDS = new Set<ACPEventKind>([
  'prompt',
  'message',
  'thought',
  'tool',
  'permission',
  'plan',
  'usage',
  'stop',
  'truncated',
])

export function isACPEvent(value: unknown): value is ACPEvent {
  if (typeof value !== 'object' || value == null) return false
  const candidate = value as { seq?: unknown; kind?: unknown }
  return typeof candidate.seq === 'number'
    && typeof candidate.kind === 'string'
    && ACP_KINDS.has(candidate.kind as ACPEventKind)
}

// ACPNotice is a server-side explanation sent on the ACP stream instead of records
// (gofer-e2x7): the job runs where its structured record cannot reach this server —
// a peer gofer, or a worker whose protocol predates the acp.jsonl mirror.
export interface ACPNotice {
  seq: number
  kind: 'notice'
  code: string
  text: string
  worker_id?: string
  worker_protocol?: number
  min_protocol?: number
}

export function isACPNotice(value: unknown): value is ACPNotice {
  if (typeof value !== 'object' || value == null) return false
  const candidate = value as { kind?: unknown; code?: unknown }
  return candidate.kind === 'notice' && typeof candidate.code === 'string'
}

// acpNoticeText renders a notice for the workbench; unknown codes fall back to the
// server's own text.
export function acpNoticeText(notice: ACPNotice): string {
  switch (notice.code) {
    case 'acp_mirror_unsupported':
      return `worker ${notice.worker_id ?? ''} 的协议 v${notice.worker_protocol ?? '?'} 不支持镜像结构化记录（需 v${notice.min_protocol ?? '?'}+），请升级该 worker；当前可点「查看过程」看日志。`
    case 'acp_mirror_peer':
      return '该 job 在 peer gofer 上执行，结构化记录留在对端、不在本机显示；可点「查看过程」看日志。'
    default:
      return notice.text
  }
}

// reduceACPEvents projects transport events into the ordered display model. A tool
// keeps the position of its first sighting while later status/content fields replace
// only values the update actually carries.
export function reduceACPEvents(events: readonly ACPEvent[]): ACPEvent[] {
  const result: ACPEvent[] = []
  const toolIndex = new Map<string, number>()

  for (const event of events) {
    if (event.kind === 'tool') {
      const index = toolIndex.get(event.tool_call_id)
      if (index == null) {
        toolIndex.set(event.tool_call_id, result.length)
        result.push(cloneEvent(event))
      } else {
        const previous = result[index] as ACPToolEvent
        result[index] = mergeDefined(previous, event)
      }
      continue
    }

    const previous = result[result.length - 1]
    if (event.kind === 'thought' && previous?.kind === 'thought') {
      result[result.length - 1] = {
        ...previous,
        seq: event.seq,
        text: previous.text + event.text,
        truncated: previous.truncated || event.truncated || undefined,
      }
      continue
    }
    if (event.kind === 'truncated' && previous?.kind === 'truncated') {
      result[result.length - 1] = {
        ...previous,
        seq: event.seq,
        skipped: previous.skipped + event.skipped,
      }
      continue
    }
    result.push(cloneEvent(event))
  }

  return result
}

export function groupACPRounds(
  jobIds: readonly string[],
  eventsByJob: Readonly<Record<string, readonly ACPEvent[]>>,
): ACPRound[] {
  return jobIds.map((jobId) => ({
    jobId,
    // The workbench conversation is only the user's words and the agent's
    // reply. Tool calls, thoughts, approvals and usage stay in job detail.
    events: reduceACPEvents(eventsByJob[jobId] ?? []).filter(
      (event) => event.kind === 'prompt' || event.kind === 'message',
    ),
  }))
}

export function visibleRoundIDs(jobIds: readonly string[], count = 3): string[] {
  if (count <= 0) return []
  return jobIds.slice(Math.max(0, jobIds.length - count))
}

function cloneEvent<T extends ACPEvent>(event: T): T {
  if (event.kind === 'tool' && event.locations) {
    return { ...event, locations: event.locations.map((location) => ({ ...location })) }
  }
  if (event.kind === 'plan' && event.entries) {
    return { ...event, entries: event.entries.map((entry) => ({ ...entry })) }
  }
  return { ...event }
}

function mergeDefined(previous: ACPToolEvent, update: ACPToolEvent): ACPToolEvent {
  const merged: ACPToolEvent = { ...previous }
  const fields = merged as unknown as Record<string, unknown>
  for (const [key, value] of Object.entries(update)) {
    if (value !== undefined) {
      fields[key] = value
    }
  }
  if (update.locations) {
    merged.locations = update.locations.map((location) => ({ ...location }))
  }
  return merged
}
