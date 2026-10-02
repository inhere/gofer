import { describe, expect, it } from 'vitest'

const drawer = Object.values(import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const sessions = Object.values(import.meta.glob('../views/Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('session last-message placement', () => {
  it('places the last message below the timeline and only exposes copy when expanded', () => {
    expect(drawer).toContain('class="last-message-fixed"')
    expect(drawer).toContain('lastMessageOpen')
    expect(drawer).toContain('class="timeline"')
    expect(drawer.indexOf('class="timeline"')).toBeLessThan(drawer.indexOf('class="last-message-fixed"'))
    expect(drawer.indexOf('class="last-message-fixed"')).toBeLessThan(drawer.indexOf('class="composer"'))
    expect(drawer).toContain('v-if="lastMessageOpen"')
    expect(drawer).toContain('历史消息 ↗')
    // 用户反馈（v0.90）：折叠时手机上折成 3 行，且不该固定在输入框上方——
    // 它是对话流的最后一项，随消息滚动；标题行不换行。
    const timelineClose = drawer.indexOf('</template>', drawer.indexOf('class="timeline"'))
    expect(timelineClose).toBeLessThan(drawer.indexOf('class="last-message-fixed"'))
    expect(drawer).toMatch(/\.last-message-head \{[^}]*flex-wrap: nowrap/)
    expect(sessions).toContain('查看全文')
    expect(sessions).toContain('openDrawer(s.session_id, true)')
  })
})
