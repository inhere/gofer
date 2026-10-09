import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./EscalationBell.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('EscalationBell and acknowledged relay turns', () => {
  it('drops acknowledged relay turns and closes a toast whose item is gone', () => {
    // 用户反馈（v0.91）：点了「无需回复」后铃铛提示与计数仍在。
    expect(source).toContain('.filter((d) => !d.acked_at)')
    expect(source).toContain('if (toast.value?.key && !next.some((it) => it.key === toast.value?.key)) toast.value = null')
    // needs_human / 会话需要授权 / 会话等回复 / 普通决策，各一种提示条
    expect(source.match(/key: fresh/g)?.length).toBe(4)
  })
})
