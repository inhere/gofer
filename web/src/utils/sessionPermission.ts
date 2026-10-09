// 终端工具授权请求（kind=permission 的 decision）的纯逻辑：按钮、作答文案、结局文案。
import type { Decision } from '../api/types'

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
