import { describe, expect, it } from 'vitest'
import { acpEventEntries } from '../../utils/conversationTimeline'
import { isACPEvent, type ACPEvent } from './acpEvents'

function raw(value: object): string {
  return Object.values(value)[0] as string
}

const conversation = raw(import.meta.glob('./ConversationView.vue', { eager: true, query: '?raw', import: 'default' }))

// 实时 SSE 收到的会话重启提示（issue gofer-glqw）：服务端在跟写转录时就把 session_note 下发
// （internal/streaming 的 follow 循环与重放共用同一个 normalizer），前端实时处理与重放同口径
// ——按 acp 帧收下、进时间线一行小字。
describe('live session note', () => {
  it('drops nothing between the live frame and the timeline note', () => {
    const frame = { seq: 7, kind: 'session_note', code: 'turn_interrupted_worker_restart' }
    expect(isACPEvent(frame)).toBe(true)
    const entries = acpEventEntries([frame as ACPEvent], { user: '你', agent: '助手' })
    expect(entries).toEqual([{ kind: 'note', id: 'note-7', code: 'turn_interrupted_worker_restart' }])
  })

  it('the workbench appends every acp event the stream sends, notes included', () => {
    // 实时路径没有按 kind 的白名单过滤：认成 ACP 事件就进那一轮的 events。
    expect(conversation).toContain("if (frame.type !== 'acp' || !isACPEvent(frame.data)) return")
    expect(conversation).toContain('[jobId]: [...(eventsByJob.value[jobId] ?? []), event]')
    expect(conversation).toContain('acpEventEntries(round.events')
  })
})
