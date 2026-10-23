import { describe, expect, it } from 'vitest'

// P4：新建会话统一到 ui/NewSessionSheet（设计稿 §8），/sessions 只留入口。
const page = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
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

  it('opens as a sheet from the sessions page instead of an inline form', () => {
    // 用户反馈（v0.90）：手机上内联表单占满第一屏；P4 起改走统一的 Sheet。
    expect(page).toContain("import NewSessionSheet from '../ui/NewSessionSheet.vue'")
    expect(page).toContain('<NewSessionSheet v-model="sheetOpen" :type="sheetType"')
    expect(page).not.toContain('class="session-create"')
    expect(sheet).toContain('<Sheet')
  })

  it('keeps the relay-mode note behind a "?" under the agent sessions title', () => {
    // 用户反馈（v0.90）：说明在手机上占 5 行，移到 AGENT 会话下并默认收起。
    expect(page).toContain('v-if="relayHelpOpen" class="relay-note"')
    expect(page.indexOf("t('sessions.tab.agent')")).toBeLessThan(page.indexOf('class="relay-note"'))
  })

  it('opens a newly created ACP session in the workbench conversation', () => {
    expect(page).toContain("path: '/workbench'")
    expect(page).toContain('thread: `s:${payload.sessionID}`')
    expect(page).not.toContain('? `/jobs/')
  })
})
