import { describe, expect, it } from 'vitest'

const drawer = Object.values(import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const workDrawer = Object.values(import.meta.glob('./WorkDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const sessions = Object.values(import.meta.glob('../views/Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('session last-message placement', () => {
  it('places the last message below the timeline and only exposes copy when expanded', () => {
    expect(drawer).toContain('class="last-message-fixed"')
    expect(drawer).toContain('lastMessageOpen')
    expect(drawer).toContain('class="timeline"')
    expect(drawer.indexOf('class="timeline"')).toBeLessThan(drawer.indexOf('class="last-message-fixed"'))
    expect(drawer.indexOf('class="last-message-fixed"')).toBeLessThan(drawer.indexOf('class="composer"'))
    expect(drawer).toContain('v-if="lastMessageOpen"')
    expect(drawer).toContain('v-html="renderMd(session.last_message)"')
    expect(drawer).not.toContain('<pre v-if="lastMessageOpen" class="last-message-text">')
    expect(drawer).toContain("'下一条消息'")
    expect(drawer).toContain('历史消息 ↗')
    // 用户反馈（v0.90）：折叠时手机上折成 3 行，且不该固定在输入框上方——
    // 它是对话流的最后一项，随消息滚动；标题行不换行。
    const timelineClose = drawer.indexOf('</template>', drawer.indexOf('class="timeline"'))
    expect(timelineClose).toBeLessThan(drawer.indexOf('class="last-message-fixed"'))
    expect(drawer).toMatch(/\.last-message-head \{[^}]*flex-wrap: nowrap/)
    expect(sessions).toContain('查看全文')
    expect(sessions).toContain('openDrawer(s.session_id, true)')
  })

  it('anchors to the latest turn when a hidden timeline becomes visible', () => {
    // 监督者复验（W 批）：手机工作台恢复上次会话时线程面板先隐藏（高度 0），
    // 点开同一会话不重载，停在最早的消息。
    expect(drawer).toContain('new ResizeObserver(')
    expect(drawer).toContain('if (timelineWasHidden && !hidden) scrollToBottom()')
  })

  it('renders the ack toggle as a bordered button', () => {
    expect(drawer).toContain('class="link-btn ack-btn mono"')
    expect(drawer).toMatch(/\.ack-btn \{[^}]*border: 1px solid/)
  })

  it('loads immediately when mounted with a session id', () => {
    // 用户反馈（v0.91）：打开会话要等约 3 秒（首个轮询）才出消息。
    expect(drawer).toMatch(/onMounted\(\(\) => \{[\s\S]*?if \(props\.sid\) void load\(\)\.then\(scrollToBottom\)[\s\S]*?liveSession\.start\(\)/)
  })
})

describe('claude session name display (Y4)', () => {
  it('shows a copyable name on the session card, work drawer row and session detail', () => {
    expect(sessions).toContain('data-test="peer-name"')
    expect(sessions).toContain('data-test="peer-name-detail"')
    expect(sessions).toContain('copySessionID(`name:${s.session_id}`, s.peer_name!)')
    expect(workDrawer).toContain('data-test="session-peer-name"')
    expect(workDrawer).toContain('copyPeerName(s.session_id, s.peer_name!)')
    expect(drawer).toContain('data-test="peer-name-detail"')
    expect(drawer).toContain('@click="copyName"')
  })
})
