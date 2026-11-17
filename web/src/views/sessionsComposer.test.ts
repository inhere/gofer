import { describe, expect, it } from 'vitest'

// P4：新建会话统一到 ui/NewSessionSheet（设计稿 §8），/sessions 只留入口。
const page = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const workbenchPage = Object.values(import.meta.glob('./Workbench.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const sheet = Object.values(import.meta.glob('../ui/NewSessionSheet.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('new session sheet', () => {
  it('offers session type, project, agent, runner, title and optional first message', () => {
    for (const key of [
      "t('sessions.new.type')",
      "t('field.project')",
      "t('field.agent')",
      "t('sessions.new.titleField')",
      "t('sessions.new.prompt')",
      "t('sessions.new.fullConfig')",
    ]) {
      expect(sheet).toContain(key)
    }
    expect(sheet).toContain('runnerLabel(runner.name)')
    expect(sheet).toContain("session: type.value === 'acp'")
  })

  it('opens as a sheet from the workbench sidebar instead of an inline form', () => {
    // 用户反馈（v0.90）：手机上内联表单占满第一屏；P4 起改走统一的 Sheet。R13 起
    // 新建会话只在工作台侧栏顶部（/sessions 只留历史列表）。
    expect(workbenchPage).toContain("import NewSessionSheet from '../ui/NewSessionSheet.vue'")
    expect(workbenchPage).toContain('<NewSessionSheet v-model="newSessionOpen" type="acp"')
    expect(workbenchPage).toContain('@click="openNewSession"')
    expect(page).not.toContain('class="session-create"')
    expect(page).not.toContain('NewSessionSheet')
    expect(sheet).toContain('<Sheet')
  })

  it('keeps the two new options and the collapsed advanced fields in the sheet', () => {
    expect(sheet).toContain('data-test="session-permission-tier"')
    expect(sheet).toContain('data-test="session-keep-dir-lock"')
    expect(sheet).toContain('sessionSubmitOptions(')
    // 其余高级项收进「更多」（默认收起）。
    expect(sheet).toContain('v-if="moreOpen"')
  })

  it('opens a newly created ACP session in the workbench conversation', () => {
    expect(workbenchPage).toContain("path: '/workbench'")
    expect(workbenchPage).toContain('thread: `s:${payload.sessionID}`')
    expect(workbenchPage).not.toContain('? `/jobs/')
  })

  it('feeds the container-worker default into the runner pick for a fresh ACP session', () => {
    // An empty current runner must not fall straight to the project's first runner.
    expect(sheet).toContain("const preferred = runnerName.value ||")
    expect(sheet).toContain('pickRunner(preferred, selectedProject.value, runners.value, runnerBlocks.value)')
  })
})
