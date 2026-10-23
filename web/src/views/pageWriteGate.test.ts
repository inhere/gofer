// 计划详情 / 工作项 / issue / 信箱的写按钮接线（gofer-bp9c）：只读身份下写按钮 disabled 且
// title 说明原因，有权限时可用。纯判定在 utils/pageGate.test.ts，这里只确认「页面真的用了它、
// 真的绑到了按钮上」（与本仓库其它接线断言同风格：读 .vue 源码）。
import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const planDetail = raw(import.meta.glob('./PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const work = raw(import.meta.glob('./Work.vue', { eager: true, query: '?raw', import: 'default' }))
const issues = raw(import.meta.glob('./Issues.vue', { eager: true, query: '?raw', import: 'default' }))
const mail = raw(import.meta.glob('./Mail.vue', { eager: true, query: '?raw', import: 'default' }))
const issueDrawer = raw(
  import.meta.glob('../components/IssueDrawer.vue', { eager: true, query: '?raw', import: 'default' }),
)
const workCard = raw(import.meta.glob('../components/WorkCard.vue', { eager: true, query: '?raw', import: 'default' }))

describe('计划详情：写控件按 plan.manage.others / 只读灰显', () => {
  it('whoami 与判定都在页面里', () => {
    expect(planDetail).toContain("import { pageWriteBlock } from '../utils/pageGate'")
    expect(planDetail).toContain('const { whoami } = useWhoami()')
    expect(planDetail).toContain("pageWriteBlock(whoami.value, { perms: ['plan.manage.others'], ownerId: plan.value?.owner })")
  })
  it('改状态 / leader / notify 开关灰显并给出原因', () => {
    expect(planDetail).toContain(':disabled="updating || !!planWriteBlock()"')
    expect(planDetail).toContain(':title="planWriteBlock() || undefined"')
    expect(planDetail).toContain(':disabled="leaderSaving || !!planWriteBlock()"')
    expect(planDetail).toContain(':disabled="notifySaving || !!planWriteBlock()"')
  })
  it('启动 / 挂起 / 继续 · 解除阻塞 三个链控件灰显（原 title 说明保留为兜底）', () => {
    expect(planDetail).toContain(':disabled="!!acting || !!planWriteBlock()"')
    expect(planDetail).toContain(':title="planWriteBlock() || PLAN_CONTROL_HELP.run.help"')
    expect(planDetail).toContain(':title="planWriteBlock() || PLAN_CONTROL_HELP.pause.help"')
    expect(planDetail).toContain(':title="planWriteBlock() || PLAN_CONTROL_HELP.resume.help"')
    expect(planDetail).toContain(':title="planWriteBlock() || PLAN_CONTROL_HELP.unblock.help"')
  })
  it('交接说明的新增 / 修改 / 保存灰显', () => {
    expect(planDetail).toContain(':disabled="handoffSaving || !!planWriteBlock()"')
    expect(planDetail).toContain('@click="startHandoffNew"')
    expect(planDetail).toContain('@click="startHandoffEdit"')
  })
})

describe('工作项：整理建议 / 合并建议按只读灰显（路由是 Public，不要权限）', () => {
  it('页面算出 writeBlock 并传给卡片', () => {
    expect(work).toContain("import { pageWriteBlock } from '../utils/pageGate'")
    expect(work).toContain('const writeBlock = computed(() => pageWriteBlock(whoami.value))')
    expect(work.match(/:write-block="writeBlock"/g)?.length).toBe(2)
  })
  it('合并建议的采纳 / 忽略灰显并给出原因', () => {
    expect(work).toContain('data-test="merge-accept" :disabled="!!writeBlock" :title="writeBlock || undefined"')
    expect(work).toContain('data-test="merge-dismiss" :disabled="!!writeBlock" :title="writeBlock || undefined"')
  })
  it('卡片上的整理建议采纳 / 忽略用同一个原因', () => {
    expect(workCard).toContain('writeBlock?: string')
    expect(workCard).toContain('data-test="accept-suggestion"')
    expect(workCard).toContain(':disabled="busy || !!writeBlock" :title="writeBlock || undefined"')
    expect(workCard.match(/:disabled="busy \|\| !!writeBlock"/g)?.length).toBe(2)
  })
})

describe('issue：新建 / 保存 / 评论按只读灰显（路由是 Public，不要权限）', () => {
  it('新建 issue 的提交与抽屉都拿到 writeBlock', () => {
    expect(issues).toContain("import { pageWriteBlock } from '../utils/pageGate'")
    expect(issues).toContain('const writeBlock = computed(() => pageWriteBlock(whoami.value))')
    expect(issues).toContain(':disabled="newBusy || !!writeBlock"')
    expect(issues).toContain(':write-block="writeBlock"')
  })
  it('抽屉的保存与评论按钮灰显并给出原因', () => {
    expect(issueDrawer).toContain('writeBlock?: string')
    expect(issueDrawer).toContain(':disabled="saving || !!props.writeBlock"')
    expect(issueDrawer).toContain('@click="sendComment"')
    expect(issueDrawer.match(/:disabled="!!props.writeBlock"/g)?.length).toBe(1)
  })
})

describe('信箱：回信按只读 / 他人的信灰显', () => {
  it('自己的信不要权限，别人的信要 session.reply.others 或 job.manage.others', () => {
    expect(mail).toContain("import { pageWriteBlock } from '../utils/pageGate'")
    expect(mail).toContain('const replyWriteBlock = computed(')
    expect(mail).toContain("anyPerms: ['session.reply.others', 'job.manage.others']")
    expect(mail).toContain(':can-send="!replyWriteBlock"')
    expect(mail).toContain(":hint=\"replyWriteBlock || ''\"")
  })
})
