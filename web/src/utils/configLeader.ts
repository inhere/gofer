// 配置页的 leader 区块与退役配置段提醒：纯函数，视图只负责接线。
import type { LeaderView } from '../api/types'

// leaderRows：把顶层 leader 段的生效视图整理成"键 / 值"行；段不存在时返回 null（视图显示"未配置"）。
export function leaderRows(leader?: LeaderView | null): Array<[string, string]> | null {
  if (!leader) return null
  return [
    ['enabled', leader.enabled ? 'enabled' : 'disabled'],
    ['agent', leader.agent || '-'],
    ['scopes', (leader.scopes ?? []).join('、') || '-'],
    ['max_rounds_per_scope', String(leader.max_rounds_per_scope)],
    ['wake_delay_sec', `${leader.wake_delay_sec}s`],
    ['on_member_done', leader.on_member_done ? 'on' : 'off'],
  ]
}

// warningsSuffix：保存 / 重读配置后的提示后缀；没有警告时为空串。
export function warningsSuffix(warnings?: string[] | null): string {
  const w = (warnings ?? []).filter((x) => x && x.trim() !== '')
  return w.length === 0 ? '' : `；警告：${w.join('；')}`
}
