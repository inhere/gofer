import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('ACP session composer placement', () => {
  it('renders a chat composer after the rendered command and keeps status guidance', () => {
    expect(source).toContain('class="session-composer-card"')
    expect(source).toContain('第一句话（可选）')
    expect(source).toContain('运行中 / 结束中 / 已结束')
    expect(source).toContain('placeholder="第一句话（可选）"')
    // ACP 的「继续会话」第一句话选填；只有批处理 cli job 的续跑仍要求指令。
    expect(source).toContain(':disabled="resuming || (resumeNeedsPrompt && !resumePrompt.trim())"')
    expect(source).toContain("agentTypes.value[job.value.agent] === 'acp-agent'")
    expect(source.indexOf('class="session-composer-card"')).toBeGreaterThan(source.indexOf('class="rendered-command"'))
    expect(source).not.toContain('下一条消息')
  })
})
