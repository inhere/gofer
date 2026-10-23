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

// 按钮的标签里可能先出现 disabled 再出现 data-test（统一按钮组件先渲染自己的 props），
// 所以先把整个 <button> 标签取出来再断言，不靠属性顺序。
function buttonTag(html: string, hook: string): string {
  return html.match(new RegExp(`<button[^>]*data-test="${hook}"[^>]*>`))?.[0] ?? ''
}

describe('WorkCard', () => {
  it('puts the badges on their own line so several status chips cannot squeeze the title', async () => {
    // 截图检查 P6-2：状态 / 未整理 / 会话离线三个 chip 在右侧时，标题被挤成一窄列换四五行。
    const html = await render({ item: item(), nowSec: 1060 })
    expect(html).toContain('icard-head--stacked')
    // 左边框颜色来自状态族（needs_onsite → need），状态文字仍在卡片上
    expect(html).toContain('work-card k-need')
    // 标题与徽标区仍在同一个 header 里（CSS 把 header 改成列方向：标题整行，徽标在下）。
    expect(html).toMatch(/class="icard-head--stacked icard-head"[\s\S]*class="icard-title[^"]*"[\s\S]*class="icard-badges"/)
  })
  it('collapsed: key info only — title, status, goal, blocker, next step, session line, link tags', async () => {
    const html = await render({ item: item(), nowSec: 1060 })
    for (const s of ['订单导出', '需现场', '给订单页加 xlsx 导出', '要去现场换设备', '周三到现场', 'claude · server · 执行中', 'shop/app', '1m前', 'issue']) {
      expect(html).toContain(s)
    }
    // the human-set marker
    expect(html).toContain('data-test="status-manual"')
    expect(html).toContain('· 手动')
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
    expect(buttonTag(none, 'open-session')).toContain('disabled')
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

  it('offers a status menu with the current status marked, instead of a native select', async () => {
    const html = await render({ item: item({ status: 'review' }) })
    expect(html).toContain('data-test="status-menu"')
    expect(html).toContain('aria-haspopup="menu"')
    expect(html).not.toContain('<select')
  })

  it('shows who wrote each field, with a colour class per speaker kind', async () => {
    const html = await render({
      item: item({ field_sources: { goal: { by: 'summarizer(claude)', at: 940 }, blocker: { by: 'session:s1abcdefgh(claude)', at: 950 }, next: { by: 'human:me', at: 990 } } }),
      nowSec: 1000,
    })
    expect(html).toContain('data-test="src-goal"')
    expect(html).toContain('整理器 (claude) · 1m前')
    expect(html).toContain('src--summarizer')
    expect(html).toContain('会话 s1abcdef (claude)')
    expect(html).toContain('src--session')
    expect(html).toContain('data-test="src-next"')
    expect(html).toContain('src--human')
  })

  it('shows tidy-up suggestions with adopt / dismiss, and request progress', async () => {
    const html = await render({
      item: item({
        suggestions: [{ field: 'goal', value: '整理器认为的目标', by: 'summarizer(claude)', at: 1, state: 'pending' }, { field: 'status_hint', value: 'waiting_resource', by: 'summarizer(claude)', at: 1, state: 'pending' }],
        requests: [{ id: 'wr-1', work_item_id: 'w-abc', session_id: 's1', kind: 'report', state: 'sent', by: 'human:me', created_at: 900, deadline: 1900 }],
      }),
      nowSec: 1000,
    })
    expect(html).toContain('data-test="suggestions"')
    expect(html).toContain('整理建议 · 目标')
    expect(html).toContain('整理器认为的目标')
    expect(html).toContain('整理建议 · 状态')
    expect(html).toContain('等资源') // the hint is shown as a status label
    expect((html.match(/data-test="accept-suggestion"/g) ?? []).length).toBe(2)
    expect((html.match(/data-test="dismiss-suggestion"/g) ?? []).length).toBe(2)
    expect(html).toContain('data-test="request-live"')
    expect(html).toContain('汇报请求 · 已送达，等会话回复（15 分钟后超时）')
  })

  it('reports the result of a finished request, and disables 整理 while one runs', async () => {
    const done = await render({
      item: item({ requests: [{ id: 'wr-2', work_item_id: 'w-abc', kind: 'report', state: 'expired', by: 'system', created_at: 900 }] }),
      nowSec: 1000,
    })
    expect(done).toContain('data-test="request-done"')
    expect(done).toContain('会话未回应，已改为整理')
    expect(buttonTag(done, 'summarize')).not.toContain('disabled')

    const live = await render({
      item: item({ requests: [{ id: 'wr-3', work_item_id: 'w-abc', kind: 'summarize', state: 'pending', by: 'human:me', created_at: 990 }] }),
      nowSec: 1000,
    })
    expect(buttonTag(live, 'summarize')).toContain('disabled')
    expect(live).toContain('整理中…')

    const noSession = await render({ item: item({ sessions: [], session_ids: [] }) })
    expect(buttonTag(noSession, 'summarize')).toContain('disabled')
  })
})
