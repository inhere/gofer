import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const bell = raw(import.meta.glob('./EscalationBell.vue', { eager: true, query: 'raw', import: 'default' }))
const todayStore = raw(import.meta.glob('../store/today.ts', { eager: true, query: 'raw', import: 'default' }))

// gofer-5wvc：待批准的 hold job 要在顶栏弹出提示。弹哪一条是 utils/pendingAlert 的纯逻辑
// （pendingAlert.test.ts 单测），这里钉住接线：快照 / 兜底轮询都把 approvals 交给它，
// 提示条点击跳到 job 详情（待批面板在页首）。
describe('顶栏待批准提示（gofer-5wvc）', () => {
  it('pending 快照里的 approvals 经 pendingAlert 变成提示条', () => {
    expect(bell).toContain("useLiveTopic('pending'")
    expect(bell).toContain('pendingLists((data ?? {}) as PendingSnapshot)')
    expect(bell).toContain('const r = pendingAlert(seen, toast.value, lists)')
    expect(bell).toContain('toast.value = r.toast')
    expect(bell).toContain(':to="toast.to"')
  })

  it('WS 断开后的兜底轮询也取待批 job（GET /v1/jobs?status=awaiting_approval）', () => {
    expect(bell).toContain("status: 'awaiting_approval'")
    expect(bell).toContain('limit: PENDING_APPROVAL_LIMIT')
    expect(bell).toContain('.map(approvalFromJob)')
  })

  it('批准 / 拒绝后从铃铛计数里消失：计数是 today 队列，pending 主题触发重拉', () => {
    // 「待我决策 N」的 N
    expect(bell).toContain('{{ decisionCount }}')
    expect(bell).toContain('decisionCount, overlayOpen, visibleCards')
    // 同一份队列：待批 job 决定后 /v1/today 不再返回该卡，计数自然减一
    expect(todayStore).toContain("export const TODAY_TOPICS = ['pending', 'jobs', 'work', 'sessions', 'plans']")
  })
})
