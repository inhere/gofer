import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { AgentSession, WorkItem, WorkSessionBrief } from '../api/types'
import WorkCard from './WorkCard.vue'

function item(over: Partial<WorkItem> = {}): WorkItem {
  return {
    id: 'w-abc', title: '订单导出', goal: '给订单页加 xlsx 导出', status: 'needs_onsite', status_source: 'human', source: 'human',
    unsorted: false, rev: 3, created_at: 1, updated_at: 1, last_activity_at: 1000, due: false, session_offline: false,
    blocker_text: '要去现场换设备', next_step: '周三到现场', project_key: 'self', workspace: '/ws/shop/app',
    sessions: [{ session_id: 's1', role: 'current', agent: 'claude', runner: 'local', state: 'running', offline: false, last_seen_at: 990 }],
    session_ids: ['s1'], links: [{ kind: 'issue', ref: 'ISS-1' }], ...over,
  }
}

async function render(props: Record<string, unknown>): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(WorkCard as any, props) }))
}

describe('WorkCard', () => {
  it('collapsed: key info only — title, status, goal, blocker, next step, session line, link tags', async () => {
    const html = await render({ item: item(), nowSec: 1060 })
    for (const s of ['订单导出', '需现场', '给订单页加 xlsx 导出', '要去现场换设备', '周三到现场', 'claude · server · 执行中', 'shop/app', '1m前', 'issue']) {
      expect(html).toContain(s)
    }
    // the human-set marker
    expect(html).toMatch(/需现场(<!--\[-->)? · 手动/)
    // nothing from the details block
    expect(html).not.toContain('data-test="card-details"')
    expect(html).not.toContain('会话首次提问自动创建')
    expect(html).toMatch(/>详情<span class="icard-caret"/)
  })

  it('expanded: shows the full fields, the sessions list and where the status came from', async () => {
    const html = await render({ item: item({ summary: '做到导出接口', source: 'auto' }), expanded: true })
    expect(html).toContain('data-test="card-details"')
    expect(html).toContain('做到导出接口')
    expect(html).toContain('会话首次提问自动创建')
    expect(html).toContain('你手动设置（优先于会话）')
    expect(html).toContain('w-abc')
    expect(html).toContain('data-test="card-sessions"')
    expect(html).toMatch(/>收起<span class="icard-caret/)
  })

  it('flags due reminders, unsorted drafts and offline sessions without changing the status', async () => {
    const html = await render({ item: item({ due: true, unsorted: true, goal: '', session_offline: true, status: 'active', status_source: 'auto' }) })
    expect(html).toContain('data-test="due-badge"')
    expect(html).toContain('提醒到期')
    expect(html).toContain('未整理')
    expect(html).toContain('目标待补')
    expect(html).toContain('data-test="offline-badge"')
    expect(html).toContain('进行中')
    expect(html).toContain('icard--hot') // a due item is surfaced
  })

  it('greys "open session" out when there is no session, and only offers wake for an offline resumable one', async () => {
    const none = await render({ item: item({ sessions: [], session_ids: [] }) })
    expect(none).toMatch(/data-test="open-session"[^>]*disabled/)
    expect(none).toContain('没有关联会话')

    const offline: WorkSessionBrief = { session_id: 's1', role: 'current', agent: 'claude', state: 'offline', offline: true, last_seen_at: 1 }
    const full: Record<string, AgentSession> = {
      s1: { session_id: 's1', agent: 'claude', state: 'offline', relay_mode: 'auto', auto_armed: false, idle_sec: -1, turn_no: 1, last_seen_at: 1, started_at: 1, peer_messaging: false, can_resume: true, resume_message: '起一个新终端继续' },
    }
    const wake = await render({ item: item({ sessions: [offline], session_offline: true }), sessions: full })
    expect(wake).toContain('data-test="wake"')
    expect(wake).toContain('接管') // not ended -> "接管"

    const noPlan = await render({ item: item({ sessions: [offline], session_offline: true }), sessions: { s1: { ...full.s1, can_resume: false } } })
    expect(noPlan).not.toContain('data-test="wake"')
    const running = await render({ item: item(), sessions: full })
    expect(running).not.toContain('data-test="wake"')
  })

  it('offers a status picker with the current status disabled', async () => {
    const html = await render({ item: item({ status: 'review' }) })
    expect(html).toContain('data-test="status-select"')
    expect(html).toMatch(/<option value="review" disabled[^>]*>待验收<\/option>/)
    expect(html).toContain('>已放弃</option>')
  })
})
