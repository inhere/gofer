import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// R13 / R10：历史页的数据全部来自工作台线程接口（含 ACP），不再有单独的 ACP 页签组件。
describe('ACP sessions in the history list', () => {
  it('lists one card per thread from the workbench interface and opens it there', () => {
    expect(source).toContain("from '../api/workbench'")
    expect(source).toContain('listWorkbenchThreads(')
    expect(source).toContain('historyQuery(')
    expect(source).toContain('acpThreadLink(thread.id)')
    expect(source).toContain('next_cursor')
    expect(source).toContain('data-test="history-cards"')
    expect(source).toContain("t('sessions.card.lastReply')")
    expect(source).toContain("t('sessions.history.open')")
  })

  it('includes finished sessions by default and keeps paging', () => {
    // since: 1 = 整个历史（含已结束，由 utils/sessionHistory 统一给）；分页用 cursor 追加更早的一页。
    const helper = Object.values(import.meta.glob('../utils/sessionHistory.ts', { eager: true, query: '?raw', import: 'default' }))[0] as string
    expect(helper).toContain('since: 1')
    expect(source).toContain('historyQuery(')
    expect(source).toContain("cursor: reset ? undefined : cursor.value")
    expect(source).toContain('data-test="history-load-more"')
    // 旧的「显示已结束」勾选随三份列表一起删掉了：默认就包含已结束。
    expect(source).not.toContain('showEnded')
  })
})
