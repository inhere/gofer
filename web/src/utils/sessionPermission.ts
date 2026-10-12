// 终端工具授权请求（kind=permission 的 decision）的纯逻辑：按钮、作答文案、结局文案。
import type { Decision, SessionQuestion } from '../api/types'

export interface PermissionChoice {
  label: string
  answer: string
  style: 'ok' | 'bad'
}

// 允许 / 每个「总是允许」选项 / 拒绝。「附原因拒绝」由组件单独处理（要输入原因）。
export function permissionChoices(d: Decision): PermissionChoice[] {
  const out: PermissionChoice[] = [{ label: '允许', answer: 'allow', style: 'ok' }]
  ;(d.permission?.suggestions ?? []).forEach((s, i) => {
    out.push({ label: s.label ? `总是允许：${s.label}` : '总是允许', answer: `always:${i}`, style: 'ok' })
  })
  out.push({ label: '拒绝', answer: 'deny', style: 'bad' })
  return out
}

export function denyAnswer(reason: string): string {
  const r = reason.trim()
  return r ? `deny:${r}` : 'deny'
}

// 已作答的人话：allow / always:<i> / deny[:原因]
export function permissionAnswerText(d: Decision): string {
  const a = (d.answer ?? '').trim()
  if (a === 'allow') return '已允许'
  if (a.startsWith('always:')) {
    const i = Number(a.slice(7))
    const label = d.permission?.suggestions?.[i]?.label
    return label ? `已总是允许（${label}）` : '已总是允许'
  }
  if (a === 'deny') return '已拒绝'
  if (a.startsWith('deny:')) return `已拒绝：${a.slice(5)}`
  return a
}

// 没在 web 上作答就结束的原因
export function permissionReleasedText(d: Decision): string {
  switch (d.released_by) {
    case 'terminal':
      return '已在终端处理'
    case 'superseded':
      return '已被新的授权请求取代'
    case 'user_returned':
      return '人回到键盘，交回终端处理'
  }
  return '已过期，交回终端处理'
}

// gofer-02fu：选择题作答。picked 是每题选中的 label（多选可多个），other 是自由填写；
// 有自由填写时以它为准。返回 null 表示还有题没答。
export function questionAnswer(
  questions: SessionQuestion[],
  picked: Record<string, string[]>,
  other: Record<string, string>,
): string | null {
  const answers: Record<string, string> = {}
  for (const q of questions) {
    const free = (other[q.question] ?? '').trim()
    const chosen = (picked[q.question] ?? []).filter((l) => l.trim() !== '')
    const a = free || chosen.join(', ')
    if (!a) return null
    answers[q.question] = a
  }
  return `answers:${JSON.stringify(answers)}`
}

// 已作答的选择题：「问题 → 答案」逐行。
export function questionAnswerText(d: Decision): string {
  const a = (d.answer ?? '').trim()
  if (!a.startsWith('answers:')) return a
  try {
    const m = JSON.parse(a.slice('answers:'.length)) as Record<string, string>
    return Object.entries(m)
      .map(([q, v]) => `${q} → ${v}`)
      .join('\n')
  } catch {
    return a
  }
}
