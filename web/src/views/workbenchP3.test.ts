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
    // P4：工作台的输入框换成统一的 Composer（设计与实现只在 ui/Composer.vue 一处）。
    const composer = raw(import.meta.glob('../ui/Composer.vue', { eager: true, query: '?raw', import: 'default' }))
    expect(threadView).toContain("import Composer from '../../ui/Composer.vue'")
    expect(threadView).toContain(":modes=\"['send', 'queue', 'interrupt']\"")
    expect(threadView).toContain('@interrupt="onInterrupt"')
    expect(composer).toContain("export type ComposerMode = 'send' | 'queue' | 'interrupt'")
    expect(composer).toContain("emit('send', next)")
    // 排队：线程一空闲就按顺序发出；打断：先取消再排队。
    expect(composer).toContain('watch(')
    expect(threadView).toContain('await stopCurrent()')
    expect(composer).toContain("t('composer.queued', { n: queued.length })")
  })

  it('persists the pending-send queue per session so a reload keeps it', () => {
    // 评审意见：队列按会话存 localStorage，重开恢复，发出 / 丢弃后清除。
    // P4：队列收进 ui/composer.ts，工作台的键还是 gofer.workbench.queue.<id>。
    const composer = raw(import.meta.glob('../ui/composer.ts', { eager: true, query: '?raw', import: 'default' }))
    expect(composer).toContain('export function readComposerQueue(')
    expect(composer).toContain('export function writeComposerQueue(')
    expect(composer).toContain('export function clearComposerQueue(')
    expect(threadView).toContain('queue-scope="workbench"')
    expect(threadView).toContain(':queue-key="thread.id"')
    const queue = raw(import.meta.glob('../components/workbench/sendQueue.ts', { eager: true, query: '?raw', import: 'default' }))
    expect(queue).toContain("const SCOPE = 'workbench'")
    expect(queue).toContain('composerQueueKey(SCOPE, threadID)')
  })

  it('shows the orphan waiting count as one header line', () => {
    // 评审意见：项目分组行上的「待处理」计数（受阻计划 / 待决策）改在列表头显示。
    expect(sidebar).toContain('data-test="orphan-notice"')
    expect(sidebar).toContain("t('workbench.orphan.notice', { n: orphanWaiting })")
    expect(sidebar).toContain("emit('openOrphans')")
    expect(workbench).toContain('orphanWaitingCount(response.value.projects)')
    expect(workbench).toContain('@open-orphans="openOrphanWaiting"')
    expect(workbench).toContain("query: Record<string, string> = { status: 'blocked' }")
  })

  it('shows tool calls collapsed by default and approvals inline in the timeline', () => {
    // R12：时间线渲染搬到共用的 ConversationTimeline（工作台 / job 消息 / 抽屉同一套）。
    const timeline = raw(import.meta.glob('../components/ConversationTimeline.vue', { eager: true, query: '?raw', import: 'default' }))
    expect(conversation).toContain('<ConversationTimeline')
    expect(conversation).toContain('acpEventEntries(round.events')
    expect(conversation).toContain('pendingInteractions')
    expect(timeline).toContain("import InteractionCard from './InteractionCard.vue'")
    expect(timeline).toContain('data-test="tools-toggle"')
    expect(timeline).toContain("t('conversation.tools', { n: entry.calls.length })")
    expect(timeline).toContain('data-test="inline-permission"')
  })

  it('keeps the page copy in the workbench dictionary', () => {
    for (const key of ['workbench.list.title', 'workbench.group.need', 'workbench.ctx.title', 'workbench.conv.emptyReady']) {
      expect(dict).toContain(`'${key}'`)
    }
    // R12：时间线（工具调用 / 注入规则 / 未发送）的文案在共用的 conversation 字典里。
    const convDict = raw(import.meta.glob('../i18n/pages/conversation.ts', { eager: true, query: '?raw', import: 'default' }))
    for (const key of ['conversation.tools', 'conversation.injectedShow', 'conversation.unsent']) {
      expect(convDict).toContain(`'${key}'`)
    }
    // 页面与专属组件里不再写死这些中文标签。
    for (const source of [workbench, sidebar, threadView, context]) {
      expect(source).not.toContain('暂无匹配会话')
      expect(source).not.toContain('工作台会话列表')
    }
  })
})
