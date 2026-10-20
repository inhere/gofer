// 只读身份 / 审阅权限 / 受限调用方的接线断言（gofer-7798 第二期 F web）：
// 纯判定在 utils/reviewGate.test.ts，这里只确认「页面真的用了它」「缺省字段真的不显示」。
import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const jobDetail = raw(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const reviewQueue = raw(import.meta.glob('./ReviewQueue.vue', { eager: true, query: '?raw', import: 'default' }))
const reviewPanel = raw(import.meta.glob('../components/ReviewPanel.vue', { eager: true, query: '?raw', import: 'default' }))
const approvalPanel = raw(
  import.meta.glob('../components/ApprovalPanel.vue', { eager: true, query: '?raw', import: 'default' }),
)
const commentThread = raw(
  import.meta.glob('../components/CommentThread.vue', { eager: true, query: '?raw', import: 'default' }),
)
const interactionCard = raw(
  import.meta.glob('../components/InteractionCard.vue', { eager: true, query: '?raw', import: 'default' }),
)
const dashboardSystem = raw(
  import.meta.glob('../components/DashboardSystem.vue', { eager: true, query: '?raw', import: 'default' }),
)
const statusBar = raw(
  import.meta.glob('../components/today/TodayStatusBar.vue', { eager: true, query: '?raw', import: 'default' }),
)
const statsTypes = raw(import.meta.glob('../api/types.ts', { eager: true, query: '?raw', import: 'default' }))
const todayApi = raw(import.meta.glob('../api/today.ts', { eager: true, query: '?raw', import: 'default' }))

describe('job 详情：只读身份收起写入口', () => {
  it('whoami 同时取 readOnly', () => {
    expect(jobDetail).toContain('const { can, readOnly, whoami } = useWhoami()')
  })
  it('重建 / 取消 / 删除 / 续接 / 结束会话都按只读隐藏', () => {
    expect(jobDetail).toContain('v-if="job && isTerminalView && job.can_delete && !readOnly"')
    expect(jobDetail).toContain('canCancelJob(job.value?.status) && !readOnly.value')
    expect(jobDetail).toContain("can('job.delete', job.caller_id) && !readOnly")
    expect(jobDetail).toContain('v-if="isTerminalView && canResume"')
    expect(jobDetail).toContain('showResumeForm && canResume')
    expect(jobDetail).toContain('v-if="sessionAlive && !readOnly"')
    expect(jobDetail).toContain('v-if="!readOnly"')
  })
  it('合并 / 交付按钮收起，只留一行说明', () => {
    expect(jobDetail).toContain('v-if="canWriteGit"')
    expect(jobDetail).toContain('data-test="wt-merge-blocked"')
    expect(jobDetail).toContain("v-if=\"job.status === 'done' && canWriteGit\"")
  })
  it('评论输入与交互作答按只读收起（列表照常）', () => {
    expect(jobDetail).toContain(':readonly="readOnly"')
    expect(commentThread).toContain('v-if="!readonly" class="cm-form"')
    expect(commentThread).toContain('data-test="cm-readonly"')
    expect(interactionCard).toContain('readonly?: boolean')
    expect(interactionCard).toContain("!!props.submitting || !pending.value || !!props.readonly")
    expect(interactionCard).toContain('data-test="icard-readonly"')
  })
})

describe('验收 / 批准按审阅权限显示', () => {
  it('详情页用 canReviewJob 算原因并传给两个面板', () => {
    expect(jobDetail).toContain("import { canReviewJob } from '../utils/reviewGate'")
    expect(jobDetail).toContain('canReviewJob(whoami.value, job.value)')
    expect(jobDetail).toContain(':blocked-reason="reviewBlockedReason"')
  })
  it('面板在原位置给一行灰字原因', () => {
    expect(reviewPanel).toContain('blockedReason?: string')
    expect(reviewPanel).toContain("props.job.status === 'needs_review' && !props.blockedReason")
    expect(reviewPanel).toContain('data-test="rp-blocked"')
    expect(approvalPanel).toContain('blockedReason?: string')
    expect(approvalPanel).toContain('data-test="ap-blocked"')
  })
  it('ORM 队列行内裁决同样按规则显示', () => {
    expect(reviewQueue).toContain("import { canReviewJob } from '../utils/reviewGate'")
    expect(reviewQueue).toContain('v-if="reviewGate(job).allowed"')
    expect(reviewQueue).toContain('data-test="rq-blocked"')
  })
})

describe('受限调用方的缺省字段：缺省即隐藏，不显示 0', () => {
  it('/v1/stats 的 db / sessions / usage 可选', () => {
    expect(statsTypes).toContain('db?: {')
    expect(statsTypes).toContain('sessions?: {')
    expect(statsTypes).toContain('usage?: {')
  })
  it('系统卡片按字段缺省整卡不渲染', () => {
    expect(dashboardSystem).toContain('v-if="stats?.db"')
    expect(dashboardSystem).toContain('v-if="stats?.sessions"')
  })
  it('/v1/today 的 usage_today / steward_today 可选，状态条缺省不摆那一项', () => {
    expect(todayApi).toContain('usage_today?: {')
    expect(todayApi).toContain('steward_today?: {')
    expect(statusBar).toContain('const u = st.usage_today')
    expect(statusBar).toContain('if (u) {')
    expect(statusBar).toContain('if (sw) {')
  })
})
