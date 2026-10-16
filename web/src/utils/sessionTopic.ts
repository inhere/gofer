// 会话主题（topic / scope）的显示、编辑与查找：纯函数，不碰 DOM，便于单测。
// 字段与规则对应 server：PATCH /v1/sessions/{sid}（topic ≤200 字符、scope ≤20 项且是项目根相对路径），
// GET /v1/sessions?q=&path=（hits / maybe_offline）。服务端仍是唯一权威，这里只是提前给出可读的错误。
import type { AgentSession, SessionHit } from '../api/types'
import { holdsPermission } from './permissionGate'
import type { AuthWhoami } from './permissions'

export const TOPIC_MAX = 200
export const SCOPE_MAX = 20
export const MANAGE_OTHERS = 'session.manage.others'

export interface TopicDisplay {
  // 要显示的文字；空 = 没有主题（含没有推断值）。
  text: string
  inferred: boolean
  // 小标签：推断 / 自述 / 人工；没有主题时为空。
  tag: string
  // 悬停说明。
  title: string
}

const SOURCE_TAG: Record<string, string> = { inferred: '推断', self: '自述', human: '人工' }

// topicSourceTag：来源的中文小标签；未知来源原样返回。
export function topicSourceTag(source: string | undefined): string {
  if (!source) return ''
  return SOURCE_TAG[source] ?? source
}

// topicDisplay：卡片 / 抽屉里主题的显示规则。推断值灰显并标「推断」；self / human 显示来源小标签。
export function topicDisplay(s: Pick<AgentSession, 'topic' | 'topic_source'>): TopicDisplay {
  const text = (s.topic ?? '').trim()
  if (!text) return { text: '', inferred: false, tag: '', title: '还没有主题' }
  const inferred = s.topic_source === 'inferred'
  const tag = topicSourceTag(s.topic_source)
  const title = inferred
    ? '推断值：由当前目录与会话名拼出，没有人为设置；点编辑写入真正的主题'
    : s.topic_source === 'self'
      ? '会话自己设置的主题'
      : s.topic_source === 'human'
        ? '由人（或其它调用方）设置的主题'
        : '会话主题'
  return { text, inferred, tag, title }
}

export interface EditGate {
  ok: boolean
  // 不能编辑时的原因（给人看）。
  reason: string
}

// canEditTopic：已结束的会话不能改；自己的会话（或没有 owner 的会话）不需要权限；别人的需要 session.manage.others。
// whoami 未取到时不拦，由服务端的 403 说话。
export function canEditTopic(who: AuthWhoami | null | undefined, s: Pick<AgentSession, 'state' | 'caller_id'>): EditGate {
  if (s.state === 'ended') return { ok: false, reason: '会话已结束，不能再修改主题与范围' }
  const owner = s.caller_id ?? ''
  if (!owner || (who && who.caller_id === owner)) return { ok: true, reason: '' }
  if (holdsPermission(who, MANAGE_OTHERS)) return { ok: true, reason: '' }
  return { ok: false, reason: `这是别人的会话，修改需要 ${MANAGE_OTHERS} 权限` }
}

export interface ScopeCheck {
  items: string[]
  error: string
}

function isAbsoluteLike(p: string): boolean {
  return p.startsWith('/') || p.startsWith('\\') || /^[A-Za-z]:/.test(p)
}

// parseScopeInput：把输入框文字（换行 / 逗号 / 空白分隔）整理成范围列表并校验。
// 清洗方式与服务端一致：./a/b/ → a/b，去重；绝对路径、含 .. 段、项目根本身、超过 20 项都报错。
export function parseScopeInput(text: string): ScopeCheck {
  const raw = text.split(/[\s,，;；]+/).map((x) => x.trim()).filter(Boolean)
  const out: string[] = []
  const seen = new Set<string>()
  for (const r of raw) {
    if (isAbsoluteLike(r)) return { items: [], error: `「${r}」是绝对路径，范围要写项目根相对路径（如 internal/job）` }
    const segs = r.split('/').filter((x) => x !== '' && x !== '.')
    if (segs.includes('..')) return { items: [], error: `「${r}」含 ..，范围不能跳出项目根` }
    if (segs.length === 0) return { items: [], error: `「${r}」是项目根本身，请写具体子目录` }
    const clean = segs.join('/')
    if (!seen.has(clean)) {
      seen.add(clean)
      out.push(clean)
    }
  }
  if (out.length > SCOPE_MAX) return { items: [], error: `范围最多 ${SCOPE_MAX} 项（现在 ${out.length} 项）` }
  return { items: out, error: '' }
}

// scopeToText：范围列表 → 输入框文字（每行一项）。
export function scopeToText(scope: string[] | undefined): string {
  return (scope ?? []).join('\n')
}

// validateTopic：≤200 字符、不含控制字符；输入框留空 = 不改主题；要清除已有主题用显式的 buildClearTopicPatch。
export function validateTopic(topic: string): string {
  const t = topic.trim()
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(t)) return '主题不能含换行或控制字符'
  if ([...t].length > TOPIC_MAX) return `主题最多 ${TOPIC_MAX} 个字符（现在 ${[...t].length}）`
  return ''
}

export interface TopicPatch {
  topic?: string
  scope?: string[]
}

export interface PatchBuild {
  patch: TopicPatch
  error: string
  // 没有任何变化。
  unchanged: boolean
}

// buildTopicPatch：对比当前会话与输入，只带改过的字段（省略 = 保持；scope: [] = 清空）。
export function buildTopicPatch(
  s: Pick<AgentSession, 'topic' | 'topic_source' | 'scope'>,
  topicInput: string,
  scopeInput: string,
): PatchBuild {
  const patch: TopicPatch = {}
  const topic = topicInput.trim()
  const terr = validateTopic(topic)
  if (terr) return { patch, error: terr, unchanged: false }
  const sc = parseScopeInput(scopeInput)
  if (sc.error) return { patch, error: sc.error, unchanged: false }
  // 推断值不是真实主题：输入与推断值相同视为没改。
  const current = s.topic_source === 'inferred' ? '' : (s.topic ?? '')
  if (topic && topic !== current) patch.topic = topic
  const cur = s.scope ?? []
  if (sc.items.length !== cur.length || sc.items.some((x, i) => x !== cur[i])) patch.scope = sc.items
  return { patch, error: '', unchanged: Object.keys(patch).length === 0 }
}

// hasStoredTopic：有真实（非推断）主题可清除。
export function hasStoredTopic(s: Pick<AgentSession, 'topic' | 'topic_source'>): boolean {
  return !!(s.topic ?? '').trim() && s.topic_source !== 'inferred'
}

// buildClearTopicPatch：显式清除主题。server 对出现的 "topic": "" 视为清空（省略才是不改），范围不动。
export function buildClearTopicPatch(): TopicPatch {
  return { topic: '' }
}

// ---- 查找 ----

export interface FindParams {
  q?: string
  path?: string
}

// buildFindParams：含 / 或 \ 且不含空白的输入当路径，其余当关键词（与 CLI 一致）。空输入 = 不查找（null）。
export function buildFindParams(input: string): FindParams | null {
  const t = input.trim()
  if (!t) return null
  if (/[/\\]/.test(t) && !/\s/.test(t)) return { path: t.replace(/\\/g, '/') }
  return { q: t }
}

const HIT_LABELS: Record<string, string> = {
  scope: '范围命中',
  scope_weak: '范围相关',
  topic: '主题命中',
  peer_name: '会话名',
  project: '项目',
}

// hitLabel：命中原因的中文；主题类命中若来自推断值，加「（推断）」。
export function hitLabel(h: SessionHit): string {
  const base = HIT_LABELS[h.kind] ?? h.kind
  const inferred = (h.kind === 'topic' || h.kind === 'scope') && h.source === 'inferred'
  return inferred ? `${base}（推断）` : base
}

// hitReasons：每条命中一行文字，如「范围命中：internal/job」。
export function hitReasons(hits: SessionHit[] | undefined): string[] {
  return (hits ?? []).map((h) => (h.match ? `${hitLabel(h)}：${h.match}` : hitLabel(h)))
}

export const MAYBE_OFFLINE_LABEL = '可能不在线'

export function findSummary(count: number, input: string): string {
  return count === 0
    ? `没有找到匹配「${input.trim()}」的在线会话（已结束的会话不参与查找）`
    : `按相关度排序，共 ${count} 个会话`
}
