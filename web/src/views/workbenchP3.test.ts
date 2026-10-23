import { describe, expect, it } from 'vitest'

function raw(value: object): string {
  return Object.values(value)[0] as string
}

const workbench = raw(import.meta.glob('./Workbench.vue', { eager: true, query: '?raw', import: 'default' }))
const sidebar = raw(import.meta.glob('../components/workbench/WorkbenchSidebar.vue', { eager: true, query: '?raw', import: 'default' }))
const threadView = raw(import.meta.glob('../components/workbench/ThreadView.vue', { eager: true, query: '?raw', import: 'default' }))
const context = raw(import.meta.glob('../components/workbench/WorkbenchContext.vue', { eager: true, query: '?raw', import: 'default' }))
const conversation = raw(import.meta.glob('../components/workbench/ConversationView.vue', { eager: true, query: '?raw', import: 'default' }))
const dict = raw(import.meta.glob('../i18n/pages/workbench.ts', { eager: true, query: '?raw', import: 'default' }))

// UI 重设计 P3 工作台（issue gofer-hez1）：列表按状态分组 + ProjectFilter、对话时间线
// （工具调用默认折叠、审批卡片内嵌）、输入框三种发送模式、右侧上下文栏、手机左侧抽屉。
describe('workbench P3 layout', () => {
  it('filters the session list by project through the URL and groups by status', () => {
    expect(workbench).toContain("project: projectFilter")
    expect(workbench).toContain("options: projectOptions,")
    expect(workbench).toContain("useProjectFilter('/workbench')")
    expect(workbench).toContain('watch(projectFilter, () => { void loadThreads() })')
    expect(workbench).toContain(':project-options="projectOptions"')
    expect(sidebar).toContain('<ProjectFilter')
    expect(sidebar).toContain('<FilterChips')
    expect(sidebar).toContain('<StatusGlyph')
    expect(sidebar).toContain("t('workbench.row.turns', { n: thread.turns })")
  })

  it('turns the phone session list into a left drawer', () => {
    expect(workbench).toContain('v-if="!mobileLayout"')
    expect(workbench).toContain('side="left"')
    expect(workbench).toContain(':model-value="mobilePane === \'sidebar\'"')
    expect(workbench).toContain(":label=\"t('workbench.mobile.list')\"")
  })

  it('renders the right context rail at >=1280 with turns, uncommitted, view switch and stop', () => {
    expect(workbench).toContain("window.matchMedia('(min-width: 1280px)')")
    expect(workbench).toContain('workbench-body--wide')
    expect(workbench).toContain('<WorkbenchContext')
    expect(context).toContain("t('workbench.ctx.turns')")
    expect(context).toContain("t('workbench.ctx.uncommitted', { n: uncommitted })")
    expect(context).toContain("t('workbench.view.process')")
    expect(context).toContain("t('workbench.view.changes')")
    expect(context).toContain('data-test="ctx-stop"')
    expect(context).toContain("emit('open-job', thread.latest_job_id)")
  })

  it('offers send / queue / interrupt-and-send in the session composer', () => {
    expect(threadView).toContain("type SendMode = 'send' | 'queue' | 'interrupt'")
    expect(threadView).toContain("(['send', 'queue', 'interrupt'] as SendMode[])")
    expect(threadView).toContain("t(`workbench.composer.mode.${mode}`)")
    // 排队：线程一空闲就按顺序发出；打断：先取消再排队。
    expect(threadView).toContain('watch(canTurn, (ready) => {')
    expect(threadView).toContain('await stopCurrent()')
    expect(threadView).toContain("t('workbench.composer.queued', { n: queued.length })")
  })

  it('shows tool calls collapsed by default and approvals inline in the timeline', () => {
    expect(conversation).toContain("import InteractionCard from '../InteractionCard.vue'")
    expect(conversation).toContain('timelineBlocks(round.events)')
    expect(conversation).toContain('data-test="tools-toggle"')
    expect(conversation).toContain("t('workbench.conv.tools', { n: block.events.length })")
    expect(conversation).toContain('data-test="inline-permission"')
    expect(conversation).toContain('inlineInteractions')
  })

  it('keeps the page copy in the workbench dictionary', () => {
    for (const key of ['workbench.list.title', 'workbench.group.need', 'workbench.ctx.title', 'workbench.conv.tools']) {
      expect(dict).toContain(`'${key}'`)
    }
    // 页面与专属组件里不再写死这些中文标签。
    for (const source of [workbench, sidebar, threadView, context]) {
      expect(source).not.toContain('暂无匹配会话')
      expect(source).not.toContain('工作台会话列表')
    }
  })
})
