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
})
