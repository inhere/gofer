// 调用方上限（ceiling）接线的源码断言：纯函数判定在 utils/ceiling.test.ts，这里只确认
// 「表单真的问了上限」「只读真的把写入口收起来了」两件事没有被后来的改动悄悄拆掉。
import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const newJob = raw(import.meta.glob('./NewJob.vue', { eager: true, query: '?raw', import: 'default' }))
const sessions = raw(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))
const newSession = raw(import.meta.glob('../ui/NewSessionSheet.vue', { eager: true, query: '?raw', import: 'default' }))
const newWorkflow = raw(import.meta.glob('./NewWorkflow.vue', { eager: true, query: '?raw', import: 'default' }))
const plans = raw(import.meta.glob('./Plans.vue', { eager: true, query: '?raw', import: 'default' }))
const workflows = raw(import.meta.glob('./Workflows.vue', { eager: true, query: '?raw', import: 'default' }))
const schedules = raw(import.meta.glob('./Schedules.vue', { eager: true, query: '?raw', import: 'default' }))
const app = raw(import.meta.glob('../ui/AppShell.vue', { eager: true, query: '?raw', import: 'default' }))
const permissions = raw(
  import.meta.glob('./settings/Permissions.vue', { eager: true, query: '?raw', import: 'default' }),
)
const whoamiStore = raw(import.meta.glob('../store/whoami.ts', { eager: true, query: '?raw', import: 'default' }))
const gate = raw(import.meta.glob('../utils/permissionGate.ts', { eager: true, query: '?raw', import: 'default' }))
const composer = raw(
  import.meta.glob('../components/workbench/WorkbenchComposer.vue', { eager: true, query: '?raw', import: 'default' }),
)

describe('whoami 的上限进入共享 store', () => {
  it('store 暴露 ceiling 与 readOnly', () => {
    expect(whoamiStore).toContain('ceiling: computed(() => callerCeiling(whoami.value))')
    expect(whoamiStore).toContain('readOnly: computed(() => ceilingReadOnly(whoami.value?.ceiling))')
  })
  it('只读上限同时压住按权限隐藏的那张动作表', () => {
    expect(gate).toContain('export function writeBlocked')
    expect(gate).toContain('if (writeBlocked(who)) return false')
  })
})

describe('新建 job 表单按上限收窄', () => {
  it('project 不列出范围外的项并说明', () => {
    expect(newJob).toContain('projects.value.filter((p) => ceilingAllowsProject(ceil.value, p.key))')
    expect(newJob).toContain('data-test="nj-project-ceiling"')
  })
  it('runner 灰显 + 写原因（沿用既有 runnerBlocks 机制）', () => {
    expect(newJob).toContain('if (!ceilingAllowsRunner(ceil.value, r.name))')
    expect(newJob).toContain("out[r.name] = { short: CEILING_LABEL, full: ceilingDeniedText('runner', r.name) }")
  })
  it('agent 收窄并挡住 exec（allow_exec）', () => {
    expect(newJob).toContain('list = list.filter((a) => ceilingAllowsAgent(ceil.value, a.key))')
    expect(newJob).toContain('if (!ceilingAllowsExec(ceil.value))')
    expect(newJob).toContain('agentCeilingBlocked')
  })
  it('预填 / rebuild 绕过下拉时提交前再拦一道，且只读时不可提交', () => {
    expect(newJob).toContain("return ceilingDeniedText('project', projectKey.value)")
    expect(newJob).toContain("return ceilingDeniedText('agent', agentKey.value)")
    expect(newJob).toContain('!ceilingReadOnly.value')
    expect(newJob).toContain('data-test="nj-readonly"')
  })
})

describe('新建会话 / workflow 向导按上限收窄', () => {
  it('会话：project 过滤 + 说明，agent 过滤，runner 灰显', () => {
    // P4：新建会话表单在 ui/NewSessionSheet，收窄规则随之搬过去。
    expect(newSession).toContain('meta.value.projects.filter((p) => ceilingAllowsProject(ceil.value, p.key))')
    expect(newSession).toContain('.filter((a) => ceilingAllowsAgent(ceil.value, a.key))')
    expect(newSession).toContain('if (!ceilingAllowsRunner(ceil.value, runner.name))')
    expect(newSession).toContain('class="ns-hint"')
  })
  it('workflow 向导：向导的候选表（project / agent / runner）都收窄', () => {
    expect(newWorkflow).toContain('projects.value.filter((p) => ceilingAllowsProject(ceil.value, p.key))')
    expect(newWorkflow).toContain('ceilingAllowsAgent(ceil.value, a.key)')
    expect(newWorkflow).toContain('runners.value.filter((r) => ceilingAllowsRunner(ceil.value, r.name))')
  })
  it('工作台「新会话」表单同样收窄，只读时不给提交', () => {
    expect(composer).toContain('meta.value.projects.filter((p) => ceilingAllowsProject(ceil.value, p.key))')
    expect(composer).toContain('.filter((agent) => ceilingAllowsAgent(ceil.value, agent.key))')
    expect(composer).toContain('if (!ceilingAllowsRunner(ceil.value, r.name))')
    expect(composer).toContain('!ceilingReadOnly.value')
    expect(composer).toContain('data-test="composer-readonly"')
  })
})

describe('只读上限：写入口消失 + 顶栏标记 + 权限页摘要', () => {
  it('顶栏挂「只读」标记，新建 job 入口收起', () => {
    expect(app).toContain('data-testid="readonly-badge"')
    expect(app).toContain('v-if="!readOnly" to="/new"')
  })
  it('plan / workflow / schedule / 会话的创建入口与表单都收起', () => {
    expect(plans).toContain('v-if="!readOnly" class="create-toggle mono"')
    expect(plans).toContain('v-if="!readOnly" class="create-row mono"')
    expect(workflows).toContain('v-if="!readOnly" to="/workflows/new" class="create-link"')
    expect(schedules).toContain('v-if="!readOnly" to="/schedules/new"')
    expect(sessions).toContain('v-if="!readOnly"')
    expect(sessions).toContain('@click="openSheet(\'pty\')"')
  })
  it('设置 → 权限显示上限摘要', () => {
    expect(permissions).toContain('data-test="perm-ceiling"')
    expect(permissions).toContain('ceilingSummary(ceiling.value)')
  })
})
