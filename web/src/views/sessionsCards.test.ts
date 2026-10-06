import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// 三个区都由表格改成了卡片（共用 InfoCard，与「工作」页同一套）；这里钉住：
// 默认只显示关键信息、其余在展开里、既有功能一个不少。
describe('Sessions page cards', () => {
  it('uses the shared InfoCard for the agent, ACP and terminal areas (no private card markup)', () => {
    expect(source).toContain("import InfoCard from '../components/InfoCard.vue'")
    expect(source.match(/<InfoCard\b/g)?.length).toBe(3)
    for (const hook of ['data-test="agent-cards"', 'data-test="acp-cards"', 'data-test="pty-cards"']) {
      expect(source).toContain(hook)
    }
    expect(source.match(/class="icard-grid"/g)?.length).toBe(3)
    // the old tables are gone
    expect(source).not.toContain('class="table"')
    expect(source).not.toContain('thead--agent')
  })

  it('each area keeps its own expand/collapse state through the shared helper', () => {
    expect(source).toContain("import { toggleExpanded } from '../utils/cardExpand'")
    for (const [set, id] of [['expandedAgent', 's.session_id'], ['expandedAcp', 'item.id'], ['expandedPty', 's.pty_session_id']]) {
      expect(source).toContain(`:expanded="${set}.has(${id})"`)
      expect(source).toContain(`@toggle="${set} = toggleExpanded(${set}, ${id})"`)
    }
  })

  it('keeps only the key info in the card head and moves the rest into the expandable details', () => {
    const agent = source.slice(source.indexOf('data-test="agent-cards"'), source.indexOf('data-test="acp-cards"'))
    const metaAt = agent.indexOf('<template #meta>')
    const actionsAt = agent.indexOf('<template #actions>')
    const detailsAt = agent.indexOf('<template #details>')
    expect(metaAt).toBeGreaterThan(0)
    expect(detailsAt).toBeGreaterThan(actionsAt)
    const head = agent.slice(0, detailsAt)
    // key info: title, state badge, agent / project / workspace / last activity, work item, main actions
    for (const keep of ['sessionDisplayName(s)', 'agentStateLabel(s.state)', '{{ s.agent }}', 's.project_key', 'workspaceLabel(s.cwd)', 'fmtAgo(s.last_seen_at, nowSec)', 'data-test="work-link"', 'data-test="row-open"', 'data-test="row-wake"']) {
      expect(head).toContain(keep)
    }
    // the noisy columns of the old table only appear in the expanded part
    const details = agent.slice(detailsAt)
    for (const moved of ['copySessionID(s.session_id)', 'runnerLabel(s.runner)', '{{ s.cwd }}', 's.last_cwd', 's.turn_no', 'peerMessagingLabel(s)', 's.transcript', 'RELAY_MODES', 'relayEvidence(s)', 'openDrawer(s.session_id, true)']) {
      expect(details).toContain(moved)
      expect(head).not.toContain(moved)
    }
  })

  it('shows the owning work item on the card and links to the work page', () => {
    expect(source).toContain('listWorkItems({ limit: 500 })')
    expect(source).toContain('workBySession.get(s.session_id)')
    expect(source).toContain('`/work?id=${encodeURIComponent(workBySession.get(s.session_id)!.id)}`')
    expect(source).toContain("createLiveTopic('work'")
  })

  it('keeps every existing control: relay switch, wake / takeover, show-ended, refresh, drawer', () => {
    expect(source).toContain('@click="onSetRelayMode(s, m)"')
    expect(source).toContain('@click="onWake(s)"')
    expect(source).toContain('v-model="showEnded"')
    expect(source).toContain('@click="loadAgentSessions()"')
    expect(source).toContain('@click="loadAcpSessions()"')
    expect(source).toContain('@click="load()"')
    expect(source).toContain('@open="openDrawer(s.session_id)"')
    expect(source).toContain('<SessionDrawer')
    expect(source).toContain('已接管 →')
  })

  it('shows the ACP and terminal cards with their own key info and actions', () => {
    expect(source).toContain('查看过程')
    expect(source).toContain('打开终端')
    expect(source).toContain('下载录制')
    expect(source).toContain('onDownloadRecording(s)')
    expect(source).toContain('第 {{ item.turn_no ?? 0 }} 轮')
  })

  it('lays out one column on a phone and several on a desktop through the shared grid', () => {
    const card = Object.values(import.meta.glob('../components/InfoCard.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
    expect(card).toContain('repeat(auto-fill, minmax(330px, 1fr))')
    expect(card).toMatch(/@media \(max-width: 640px\) \{\s*\.icard-grid \{\s*grid-template-columns: minmax\(0, 1fr\);/)
  })
})
