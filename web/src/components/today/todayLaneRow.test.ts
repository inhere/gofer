import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { TodayLane } from '../../api/todayLanes'
import TodayLaneRow from './TodayLaneRow.vue'

function lane(over: Partial<TodayLane> = {}): TodayLane {
  return {
    id: 'w-1',
    kind: 'work',
    title: '导出接口改造',
    project_key: 'orders-api',
    status: 'active',
    agents: [{ agent: 'codex', state: 'running', kind: 'job', ref: 'j1' }],
    progress: {
      pips: [
        { todo_id: 't1', title: 'CSV', status: 'done' },
        { todo_id: 't2', title: '分页', status: 'running' },
        { todo_id: 't3', title: '文档', status: 'pending' },
      ],
      current: '分页方案',
    },
    health: 'ok',
    started_at: 1,
    elapsed_sec: 6000,
    activity_at: 1,
    links: { work_item_id: 'w-1' },
    ...over,
  }
}

const render = (l: TodayLane): Promise<string> => renderToString(createSSRApp({ render: () => h(TodayLaneRow, { lane: l }) }))

describe('TodayLaneRow (rendered)', () => {
  it('renders title, project, agent, pips, current step and elapsed; no health text when ok', async () => {
    const html = await render(lane())
    expect(html).toContain('导出接口改造')
    expect(html).toContain('orders-api')
    expect(html).toContain('codex·running')
    expect(html.match(/class="pip--/g)).toHaveLength(3)
    expect(html).toContain('pip--running')
    expect(html).toContain('分页方案')
    expect(html).toContain('1h40m')
    expect(html).toMatch(/data-test="lane-health"[^>]*><\/span>/)
    expect(html).toContain('role="button"')
    expect(html).toContain('tabindex="0"')
  })

  it('shows the health label (with the reason as title) only when not ok', async () => {
    const html = await render(lane({ health: 'stalled', health_reason: '5 小时没有活动', agents: [], progress: { current: '最近：已加 testcmd 复用' } }))
    expect(html).toContain('停滞')
    expect(html).toContain('title="5 小时没有活动"')
    expect(html).toContain('—') // no agent
    expect(html).not.toContain('pip--')
    expect(html).toContain('最近：已加 testcmd 复用')
  })

  it('marks a blocked lane red on the dot', async () => {
    const html = await render(lane({ health: 'blocked', health_reason: 'plan 「N2」 阻塞' }))
    expect(html).toContain('dot--blocked')
    expect(html).toContain('阻塞')
  })
})
