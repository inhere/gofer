import { describe, expect, it } from 'vitest'
import { isSessionReply, mergeSessionTimeline, sessionReplyLabel } from './sessionMessaging'
import { opLabel } from './messenger'
import type { SessionMessage } from '../api/types'

const drawer = Object.values(import.meta.glob('../components/SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// gofer-6er0：经传话人发给终端会话的消息，会话的回复原文直接出现在 web 对话里。
describe('session reply to a web message', () => {
  const sent: SessionMessage = { id: 'msg-1', session_id: 's1', text: '进度如何', status: 'delivered', channel: 'messenger', created_at: 10, updated_at: 10 }
  const reply: SessionMessage = { id: 'reply-1', session_id: 's1', text: '进度：**完成**', status: 'delivered', channel: 'send_message',
    direction: 'reply', source: 'session', peer: 'uds:/tmp/cc-socks/1.sock', reply_to: 'msg-1', created_at: 12, updated_at: 12 }

  it('tells a reply from a web message and labels it as the session', () => {
    expect(isSessionReply(sent)).toBe(false)
    expect(isSessionReply(reply)).toBe(true)
    const session = { peer_name: 'proj-a', title: '', agent: 'claude', session_id: 'abcdef1234567890' }
    expect(sessionReplyLabel(reply, session)).toBe('proj-a · 回复')
    expect(sessionReplyLabel({ ...reply, source: 'messenger' }, session)).toBe('proj-a · 回复（经传话人收到）')
    expect(sessionReplyLabel(reply, null)).toBe('agent · 回复')
  })

  it('places the reply after the message it answers', () => {
    const timeline = mergeSessionTimeline([], [reply, sent])
    expect(timeline.map((e) => (e.kind === 'message' ? e.message.id : ''))).toEqual(['msg-1', 'reply-1'])
  })

  it('renders a reply as an agent-side markdown bubble, before the web-message branch', () => {
    expect(drawer).toContain(`v-if="entry.kind === 'message' && isSessionReply(entry.message)"`)
    expect(drawer).toContain('class="bubble bubble--agent session-reply" data-test="session-reply"')
    expect(drawer).toContain('v-html="renderMd(entry.message.text)"')
    expect(drawer.indexOf('data-test="session-reply"')).toBeLessThan(drawer.indexOf('你（经转达）'))
  })

  it('names reply entries in the messenger delivery history', () => {
    expect(opLabel('reply')).toBe('收到回复')
    expect(opLabel('send')).toBe('传话')
    expect(opLabel('list_agents')).toBe('列出会话')
  })
})
