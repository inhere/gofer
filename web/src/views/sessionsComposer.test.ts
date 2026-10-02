import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('compact session creation form', () => {
  it('offers session type, project, agent, runner, title and optional first message', () => {
    for (const label of ['会话类型', '项目', 'agent', 'runner', '标题（可选）', '第一句话（可选）', '完整配置']) {
      expect(source).toContain(label)
    }
    expect(source).toContain('class="session-create"')
    expect(source).toContain('session: sessionType.value === \'acp\'')
  })

  it('collapses the create form on a phone so the session lists come first', () => {
    // 用户反馈（v0.90）：手机上表单占满第一屏。
    expect(source).toContain("window.matchMedia?.('(max-width: 640px)').matches")
    expect(source).toContain('v-show="createOpen" class="session-create-fields"')
    expect(source).toContain("{{ createOpen ? '收起' : '＋ 新建会话' }}")
  })

  it('keeps the relay-mode note behind a "?" under the agent sessions title', () => {
    // 用户反馈（v0.90）：说明在手机上占 5 行，移到 AGENT 会话下并默认收起。
    expect(source).toContain('v-if="relayHelpOpen" class="relay-note mono"')
    expect(source.indexOf('AGENT 会话')).toBeLessThan(source.indexOf('class="relay-note mono"'))
  })
})
