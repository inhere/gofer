import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// R13：Sessions 页改成三类会话合一的历史页——一条共享卡片列表 + 每行的详情抽屉入口；
// 原来三份各自渲染的卡片（Agent / ACP / 终端）与页签内的加载器都已删除。
describe('Sessions history page', () => {
  it('renders one shared InfoCard list for every source (no per-source card markup)', () => {
    expect(source).toContain("import InfoCard from '../components/InfoCard.vue'")
    expect(source.match(/<InfoCard\b/g)?.length).toBe(1)
    expect(source.match(/class="icard-grid"/g)?.length).toBe(1)
    expect(source).toContain('data-test="history-cards"')
    // the old per-source lists and their loaders are gone
    expect(source).not.toContain('SessionsAcpTab')
    expect(source).not.toContain('data-test="agent-cards"')
    expect(source).not.toContain('data-test="pty-cards"')
    expect(source).not.toContain('class="table"')
    expect(source).not.toContain('thead--agent')
  })

  it('keeps expand / collapse state through the shared helper', () => {
    expect(source).toContain("import { toggleExpanded } from '../utils/cardExpand'")
    expect(source).toContain(':expanded="expanded.has(thread.id)"')
    expect(source).toContain('@toggle="expanded = toggleExpanded(expanded, thread.id)"')
  })

  it('shows the owning work item on the card and links to the work page', () => {
    expect(source).toContain('listWorkItems({ limit: 500 })')
    expect(source).toContain('workFor(thread)')
    expect(source).toContain('`/work?id=${encodeURIComponent(workFor(thread)!.id)}`')
    expect(source).toContain("createLiveTopic('work'")
  })

  it('keeps every terminal-only control reachable: relay, wake, grant, owner transfer, recording', () => {
    // 中继开关、授权、唤醒 / 接管、转归属、主题、删除都在详情抽屉里（三板斧搬到那里，
    // 不丢功能）；行上给入口，终端会话另有接回终端与录制下载。
    expect(source).toContain("import SessionDrawer from '../components/SessionDrawer.vue'")
    expect(source).toContain('<SessionDrawer')
    expect(source).toContain('data-test="history-detail"')
    expect(source).toContain('@click="openDrawer(threadSessionID(thread))"')
    expect(source).toContain('data-test="history-attach"')
    expect(source).toContain('data-test="history-recording"')
    expect(source).toContain('downloadPtyRecording(')
    expect(source).toContain("t('sessions.card.openTerminal')")
    expect(source).toContain("t('sessions.card.downloadRecording')")
  })

  it('offers continue, job and workbench links on a row', () => {
    expect(source).toContain('data-test="history-open"')
    expect(source).toContain('data-test="history-continue"')
    expect(source).toContain('data-test="history-job"')
    expect(source).toContain('acpThreadLink(thread.id)')
    expect(source).toContain("t('sessions.history.open')")
    expect(source).toContain("t('sessions.card.turns')")
  })

  it('lays out one column on a phone and several on a desktop through the shared grid', () => {
    const card = Object.values(import.meta.glob('../components/InfoCard.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
    expect(card).toContain('repeat(auto-fill, minmax(330px, 1fr))')
    expect(card).toMatch(/@media \(max-width: 760px\) \{\s*\.icard-grid \{\s*grid-template-columns: minmax\(0, 1fr\);/) })
})
