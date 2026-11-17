// 历史页的「按主题 / 路径找」（gofer session find 同一接口）：查什么、显示什么、点开去哪。
// 页面只做接线（见 views/sessionsHistory.test.ts 的接线断言），口径都在 utils/sessionFind.ts。
import { describe, expect, it } from 'vitest'
import { findQuery, findRowTarget } from './sessionFind'
import { findSummary, hitReasons, topicDisplay } from './sessionTopic'

describe('find sessions by topic / path', () => {
  it('turns the search box input into the session find request', () => {
    // 关键词（无路径分隔符）当 q；前后空白去掉。
    expect(findQuery('让锁', 50)).toEqual({ q: '让锁', project: undefined, limit: 50 })
    expect(findQuery('  让锁  ', 50, 'p1')).toEqual({ q: '让锁', project: 'p1', limit: 50 })
    // 含 / 或 \ 且无空白当路径（反斜杠归一成 /，与 CLI 一致）。
    expect(findQuery('internal/job', 50)).toEqual({ path: 'internal/job', project: undefined, limit: 50 })
    expect(findQuery('internal\\job\\dirlock.go', 50)).toEqual({
      path: 'internal/job/dirlock.go',
      project: undefined,
      limit: 50,
    })
    // 带空白的不是路径，仍按关键词查。
    expect(findQuery('internal/job 让锁', 50)).toEqual({ q: 'internal/job 让锁', project: undefined, limit: 50 })
    // 空输入不查找（页面回到空态，不发请求）。
    expect(findQuery('', 50)).toBeNull()
    expect(findQuery('   ', 50)).toBeNull()
  })

  it('renders the session name, topic and why each row matched', () => {
    // 命中原因直接来自服务端 hits（与旧页一致）：每条一行文字，推断值标注「推断」。
    expect(hitReasons([{ kind: 'scope', match: 'internal/job' }])).toEqual(['范围命中：internal/job'])
    expect(
      hitReasons([
        { kind: 'topic', match: '让锁', source: 'inferred' },
        { kind: 'project', match: 'alpha' },
      ]),
    ).toEqual(['主题命中（推断）：让锁', '项目：alpha'])
    // 主题：推断值灰显并带标记，人工写的主题正常显示。
    expect(topicDisplay({ topic: '等待输入让锁', topic_source: 'human' })).toMatchObject({
      text: '等待输入让锁',
      inferred: false,
    })
    expect(topicDisplay({ topic: '推断出的主题', topic_source: 'inferred' })).toMatchObject({
      text: '推断出的主题',
      inferred: true,
    })
    // 没有主题时不占位。
    expect(topicDisplay({}).text).toBe('')
    // 摘要：命中条数与查找词。
    expect(findSummary(0, ' zzz ')).toContain('没有找到')
    expect(findSummary(2, '让锁')).toContain('2')
  })

  it('opens the drawer for terminal sessions and the workbench thread for acp / pty', () => {
    const threads = [
      { id: 'r:sid-term', kind: 'relay' as const },
      { id: 's:sid-acp', kind: 'agent' as const },
      { id: 's:sid-pty', kind: 'agent' as const, interactive: true },
    ]
    // ACP / pty 的工作台线程：进那个线程（工作台可以接着聊）。
    expect(findRowTarget('sid-acp', threads)).toEqual({
      kind: 'thread',
      threadID: 's:sid-acp',
      link: '/workbench?thread=s%3Asid-acp',
    })
    expect(findRowTarget('sid-pty', threads)).toEqual({
      kind: 'thread',
      threadID: 's:sid-pty',
      link: '/workbench?thread=s%3Asid-pty',
    })
    // 终端会话（relay 线程）打开详情抽屉。
    expect(findRowTarget('sid-term', threads)).toEqual({ kind: 'drawer', sid: 'sid-term' })
    // 查找结果来自会话登记表，未必在已加载的历史页里：认不出来按终端会话处理，功能不丢。
    expect(findRowTarget('sid-other', threads)).toEqual({ kind: 'drawer', sid: 'sid-other' })
    expect(findRowTarget('sid-other', [])).toEqual({ kind: 'drawer', sid: 'sid-other' })
  })
})
