import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const alerts = raw(import.meta.glob('../utils/pendingAlert.ts', { eager: true, query: 'raw', import: 'default' }))
const bell = raw(import.meta.glob('./EscalationBell.vue', { eager: true, query: 'raw', import: 'default' }))

describe('EscalationBell and acknowledged relay turns', () => {
  it('drops acknowledged relay turns and closes a toast whose item is gone', () => {
    // 用户反馈（v0.91）：点了「无需回复」后铃铛提示与计数仍在。
    expect(alerts).toContain('.filter((d) => !d.acked_at)')
    expect(alerts).toContain('const kept = toast && items.some((it) => it.key === toast.key) ? toast : null')
    // 每种待办各有自己的提示条：待批准 job / needs_human 交互 / 权限 / 中继 / 普通决策
    for (const title of ['⏸ job 等你批准', '⚠ 新的人工介入请求', '会话在等你回答', '会话需要授权', '会话等待回复', '新的决策请求']) {
      expect(alerts).toContain(title)
    }
    // 弹哪一条的逻辑在 pendingAlert（可单测），铃铛只接线
    expect(bell).toContain('const r = pendingAlert(seen, toast.value, lists)')
  })
})
