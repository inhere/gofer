// 会话主题（topic / scope）的显示、编辑与查找：纯函数，不碰 DOM，便于单测。
// 字段与规则对应 server：PATCH /v1/sessions/{sid}（topic ≤200 字符、scope ≤20 项且是项目根相对路径），
// GET /v1/sessions?q=&path=（hits / maybe_offline）。服务端仍是唯一权威，这里只是提前给出可读的错误。
import type { AgentSession, SessionHit } from '../api/types'
import { t, te } from '../i18n'
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

// topicSourceTag：来源的小标签；未知来源原样返回。
export function topicSourceTag(source: string | undefined): string {
  if (!source) return ''
  const key = `util.topic.tag.${source}`
  return te(key) ? t(key) : source
}

// topicDisplay：卡片 / 抽屉里主题的显示规则。推断值灰显并标「推断」；self / human 显示来源小标签。
export function topicDisplay(s: Pick<AgentSession, 'topic' | 'topic_source'>): TopicDisplay {
  const text = (s.topic ?? '').trim()
  if (!text) return { text: '', inferred: false, tag: '', title: t('util.topic.none') }
  const inferred = s.topic_source === 'inferred'
  const tag = topicSourceTag(s.topic_source)
  const title = inferred
    ? t('util.topic.title.inferred')
    : s.topic_source === 'self'
      ? t('util.topic.title.self')
      : s.topic_source === 'human'
        ? t('util.topic.title.human')
        : t('util.topic.title.plain')
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
  if (s.state === 'ended') return { ok: false, reason: t('util.topic.err.ended') }
  const owner = s.caller_id ?? ''
  if (!owner || (who && who.caller_id === owner)) return { ok: true, reason: '' }
  if (holdsPermission(who, MANAGE_OTHERS)) return { ok: true, reason: '' }
  return { ok: false, reason: t('util.topic.err.others', { perm: MANAGE_OTHERS }) }
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
    if (isAbsoluteLike(r)) return { items: [], error: t('util.topic.err.absolute', { v: r }) }
    const segs = r.split('/').filter((x) => x !== '' && x !== '.')
    if (segs.includes('..')) return { items: [], error: t('util.topic.err.dotdot', { v: r }) }
    if (segs.length === 0) return { items: [], error: t('util.topic.err.rootDir', { v: r }) }
    const clean = segs.join('/')
    if (!seen.has(clean)) {
      seen.add(clean)
      out.push(clean)
    }
  }
  if (out.length > SCOPE_MAX) return { items: [], error: t('util.topic.err.scopeMax', { max: SCOPE_MAX, n: out.length }) }
  return { items: out, error: '' }
}

// scopeToText：范围列表 → 输入框文字（每行一项）。
export function scopeToText(scope: string[] | undefined): string {
  return (scope ?? []).join('\n')
}

// validateTopic：≤200 字符、不含控制字符；输入框留空 = 不改主题；要清除已有主题用显式的 buildClearTopicPatch。
export function validateTopic(topic: string): string {
  const v = topic.trim()
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(v)) return t('util.topic.err.controlChars')
  if ([...v].length > TOPIC_MAX) return t('util.topic.err.topicMax', { max: TOPIC_MAX, n: [...v].length })
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

// hitLabel：命中原因；主题类命中若来自推断值，加「（推断）」。
export function hitLabel(h: SessionHit): string {
  const key = `util.topic.hit.${h.kind}`
  const base = te(key) ? t(key) : h.kind
  const inferred = (h.kind === 'topic' || h.kind === 'scope') && h.source === 'inferred'
  return inferred ? t('util.topic.hit.inferred', { base }) : base
}

// hitReasons：每条命中一行文字，如「范围命中：internal/job」。
export function hitReasons(hits: SessionHit[] | undefined): string[] {
  return (hits ?? []).map((h) => (h.match ? t('util.topic.hit.line', { label: hitLabel(h), match: h.match }) : hitLabel(h)))
}

// 「可能不在线」标记：旧 worker 没上报心跳时的提示。
export function maybeOfflineLabel(): string {
  return t('util.topic.maybeOffline')
}

export function findSummary(count: number, input: string): string {
  return count === 0
    ? t('util.topic.findNone', { q: input.trim() })
    : t('util.topic.findCount', { n: count })
}
