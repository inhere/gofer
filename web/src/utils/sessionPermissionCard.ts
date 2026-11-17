// 会话抽屉里的两张卡（终端工具授权请求 / AskUserQuestion 选择题）→ 统一卡片（InteractionCard）
// 的 Interaction 形状。纯投影：作答仍走会话权限接口（answerSessionPermission），服务端契约不变。
import type { Decision, Interaction, SessionQuestion } from '../api/types'
import { t } from '../i18n'
import { denyAnswer, permissionAnswerText, permissionChoices, questionAnswerRecord } from './sessionPermission'

// 卡片上的作答值 → 会话权限接口要的值：选项按钮给的是 permissionChoices 的 answer
// （allow / always:<i> / deny），「其他」里写的自由文本按「附原因拒绝」编码（旧卡片的附原因拒绝）。
export function permissionCardAnswer(value: string, known: readonly string[]): string {
  const v = value.trim()
  if (v === '') {
    return ''
  }
  return known.includes(v) ? v : denyAnswer(v)
}

// 终端工具授权请求 → 卡片：工具名 / 摘要 / 参数进 tool_call（卡片原有渲染），
// allow_other 打开「其他」输入（写拒绝原因）。倒计时用 decision 自己的 timeout_sec。
export function permissionCardInteraction(d: Decision): Interaction {
  const it: Interaction = {
    id: d.id,
    job_id: d.session_id ?? '',
    type: 'permission',
    prompt: d.question || d.permission?.summary || d.title,
    options: permissionChoices(d).map((c) => ({
      value: c.answer,
      label: c.label,
      kind: c.style === 'ok' ? 'allow_once' : 'reject_once',
    })),
    status: decisionCardStatus(d),
    created_at: d.asked_at,
    allow_other: true,
    policy_hint: t('sessions.perm.hint'),
    tool_call: {
      id: d.id,
      kind: d.permission?.tool_name,
      title: d.permission?.summary,
      raw_input_summary: d.permission?.input,
    },
  }
  if (d.timeout_sec > 0) {
    it.expires_at = d.asked_at + d.timeout_sec
  }
  if (it.status === 'answered') {
    it.answer = permissionAnswerText(d)
    it.answered_at = d.answered_at
    it.answered_by = d.answered_by
  }
  return it
}

// AskUserQuestion 的每题一张卡：多选 / 「其他」自由文本 / 选项说明都映射过去。
// 答复仍是 answers:<问题 → 文本>（多选按逗号拼，见 questionPickText）。
export function questionCardInteractions(d: Decision): Interaction[] {
  const answered = questionAnswerRecord(d) ?? {}
  const status = decisionCardStatus(d)
  return (d.permission?.questions ?? []).map((q, i) => {
    const card: Interaction = {
      id: `${d.id}:${i}`,
      job_id: d.session_id ?? '',
      type: 'choice',
      prompt: questionPrompt(q),
      options: (q.options ?? []).map((o) => ({
        value: o.label,
        label: o.label,
        description: o.description,
      })),
      status,
      created_at: d.asked_at,
      multi: !!q.multiSelect,
      allow_other: true,
    }
    if (status === 'answered') {
      card.answer = answered[q.question] ?? ''
    }
    return card
  })
}

// 一张选择题卡的作答值 → 终端 agent 看到的文本：多选的 JSON 数组按逗号拼（answers 是问题→文本）。
export function questionPickText(value: string, multi: boolean): string {
  if (!multi) {
    return value
  }
  try {
    const picked = JSON.parse(value) as unknown
    if (Array.isArray(picked)) {
      return picked.filter((v): v is string => typeof v === 'string').join(', ')
    }
  } catch {
    // 不是 JSON（老卡片 / 单选复用）就按原文。
  }
  return value
}

// decision 的三个状态 → 卡片的只读态（卡片是 decision 投影，没有 pending 之外的中间态）。
function decisionCardStatus(d: Decision): Interaction['status'] {
  if (d.state === 'ANSWERED') return 'answered'
  if (d.state === 'EXPIRED') return 'expired'
  return 'pending'
}

// 选择题的题面：header（如「颜色」）作为前缀，卡片标题行只显示 type/id，题面自带它。
export function questionPrompt(q: SessionQuestion): string {
  return q.header ? `${q.header} · ${q.question}` : q.question
}
