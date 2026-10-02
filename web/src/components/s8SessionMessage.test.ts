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
    expect(sessions).toContain('查看全文')
    expect(sessions).toContain('openDrawer(s.session_id, true)')
  })
})
