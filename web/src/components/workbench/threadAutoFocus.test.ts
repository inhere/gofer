import { describe, expect, it } from 'vitest'
import type { WorkbenchThread } from '../../api/types'
import { createThreadAutoFocus, type ThreadAutoFocus } from './threadAutoFocus'

// gofer-2seo：工作台回复完会话 A 再切到会话 B，推送刷新不能把焦点拽回 A。
// 这里用一个最小的「工作台」替身：单窗格焦点 + 移动端单栏（sidebar / main），
// 与 Workbench.vue 一样把 selectThread / 深链接 / 刷新交给 threadAutoFocus。

function thread(id: string, jobIDs: string[] = []): WorkbenchThread {
  return {
    id,
    kind: id.startsWith('r:') ? 'relay' : 'agent',
    status: 'idle',
    title: id,
    project_key: 'p',
    resumable: true,
    started_at: 1,
    updated_at: 1,
    turns: jobIDs.length,
    job_ids: jobIDs,
  } as WorkbenchThread
}

class FakeWorkbench {
  focused = ''
  selected = ''
  mobilePane: 'sidebar' | 'main' = 'sidebar'
  query = ''
  // router.replace 是异步的：asyncRoute 时清理先挂起，flushRoute() 才生效并触发地址 watcher。
  asyncRoute = false
  routeCleared = false
  notices: string[] = []
  threads = new Map<string, WorkbenchThread>()
  autoFocus: ThreadAutoFocus

  constructor(list: WorkbenchThread[], query = '') {
    this.query = query
    this.setThreads(list)
    this.autoFocus = createThreadAutoFocus({
      requestedThreadID: () => this.query,
      clearRequestedThread: () => {
        if (this.asyncRoute) this.routeCleared = true
        else this.query = ''
      },
      focusedThreadID: () => this.focused,
      selectThread: (t) => this.select(t),
      setSelectedID: (id) => { this.selected = id },
      notice: (message) => { this.notices.push(message) },
    })
  }

  setThreads(list: WorkbenchThread[]): void {
    this.threads = new Map(list.map((t) => [t.id, t]))
  }

  // Workbench.selectThread：放进聚焦窗格、选中、移动端切到对话栏。
  select(t: WorkbenchThread): void {
    this.focused = t.id
    this.selected = t.id
    this.mobilePane = 'main'
  }

  // 用户在侧栏 / 命令面板点选。
  pick(id: string): void {
    this.select(this.threads.get(id)!)
  }

  // 移动端「返回」列表。
  back(): void {
    this.mobilePane = 'sidebar'
  }

  // 首次进入页面：布局就绪后定位深链接。
  mount(focused: string): void {
    this.focused = focused
    this.selected = focused
    this.autoFocus.locateRequested(this.threads, true)
  }

  flushRoute(): void {
    if (!this.routeCleared) return
    this.routeCleared = false
    this.query = ''
    this.autoFocus.locateRequested(this.threads, true)
  }

  // 地址变化（再次点通知链接）→ Workbench 的 route.query.thread watcher。
  navigate(query: string): void {
    this.query = query
    this.autoFocus.locateRequested(this.threads, true)
  }

  // loadThreads 拉回新列表（推送失效 / 兜底轮询 / context.refresh）。
  refresh(list: WorkbenchThread[]): void {
    this.setThreads(list)
    this.autoFocus.refreshed(this.threads, true)
  }

  // ThreadView.sendTurn → context.continued(job_id) → loadThreads。
  reply(jobID: string, list: WorkbenchThread[]): void {
    this.autoFocus.continued(jobID)
    this.refresh(list)
  }
}

describe('workbench thread auto-focus (gofer-2seo)', () => {
  it('replying to A then switching to B stays on B when A’s resumed turn shows up later', () => {
    const a = thread('s:a', ['a1'])
    const b = thread('s:b', ['b1'])
    const wb = new FakeWorkbench([a, b])
    wb.mount('s:a')

    // 续接已结束的持续会话会新建一个 job（a2）；它在 worker 写回会话状态前还不在 A 的 job_ids 里。
    wb.reply('a2', [a, b])
    wb.pick('s:b')
    expect(wb.focused).toBe('s:b')

    // A 的新一轮开始运行 / 收到回复 → jobs 主题推送 → 重拉列表。
    wb.refresh([thread('s:a', ['a1', 'a2']), b])
    expect(wb.focused).toBe('s:b')
    expect(wb.selected).toBe('s:b')
    wb.refresh([thread('s:a', ['a1', 'a2']), b])
    expect(wb.focused).toBe('s:b')
  })

  it('a reply never re-targets the focused pane or rewrites the selection to a job id', () => {
    const a = thread('s:a', ['a1'])
    const wb = new FakeWorkbench([a, thread('s:b')])
    wb.mount('s:a')
    wb.reply('a1', [a, thread('s:b')])
    expect(wb.selected).toBe('s:a')
    expect(wb.focused).toBe('s:a')
  })

  it('a notification deep link is located once; later refreshes do not drag focus back to it', () => {
    const a = thread('r:a')
    const b = thread('r:b')
    const wb = new FakeWorkbench([a, b], 'r:a')
    wb.mount('s:x')
    expect(wb.focused).toBe('r:a')
    expect(wb.notices).toContain('已定位到通知对应的会话')
    expect(wb.query).toBe('')

    // 在 A 里回复（中继：sessions 主题推送），然后切到 B。
    wb.pick('r:b')
    wb.refresh([a, b])
    expect(wb.focused).toBe('r:b')
    wb.refresh([a, b])
    expect(wb.focused).toBe('r:b')
  })

  it('a deep link is not re-applied while the URL cleanup is still in flight, and can be followed again later', () => {
    const a = thread('r:a')
    const b = thread('r:b')
    const wb = new FakeWorkbench([a, b], 'r:a')
    wb.asyncRoute = true
    wb.mount('r:b')
    expect(wb.focused).toBe('r:a')
    wb.pick('r:b')
    wb.refresh([a, b])
    expect(wb.focused).toBe('r:b')
    wb.flushRoute()
    wb.refresh([a, b])
    expect(wb.focused).toBe('r:b')

    // 用户再点一次同一条通知：照常定位。
    wb.navigate('r:a')
    expect(wb.focused).toBe('r:a')
  })

  it('mobile: after going back to the list, a push refresh does not pull the conversation pane open', () => {
    const a = thread('r:a')
    const b = thread('r:b')
    const wb = new FakeWorkbench([a, b], 'r:a')
    wb.mount('')
    expect(wb.mobilePane).toBe('main')
    wb.back()
    wb.pick('r:b')
    wb.back()
    wb.refresh([a, b])
    expect(wb.mobilePane).toBe('sidebar')
    expect(wb.focused).toBe('r:b')
  })

  it('still waits for a deep-linked session that has not appeared yet, unless the user moved on', () => {
    const b = thread('r:b')
    const waiting = new FakeWorkbench([b], 'r:a')
    waiting.mount('r:b')
    expect(waiting.notices).toContain('通知指向的会话尚未出现，正在等待刷新')
    waiting.refresh([thread('r:a'), b])
    expect(waiting.focused).toBe('r:a')

    const moved = new FakeWorkbench([b, thread('s:c')], 'r:a')
    moved.mount('r:b')
    moved.pick('s:c')
    moved.refresh([thread('r:a'), b, thread('s:c')])
    expect(moved.focused).toBe('s:c')
  })

  it('a session launched from the composer is focused once it appears, unless the user picked another', () => {
    const a = thread('s:a', ['a1'])
    const wb = new FakeWorkbench([a])
    wb.mount('s:a')
    wb.autoFocus.launched('n1')
    wb.refresh([a])
    wb.refresh([a, thread('s:n', ['n1'])])
    expect(wb.focused).toBe('s:n')
    // 只定位一次。
    wb.pick('s:a')
    wb.refresh([a, thread('s:n', ['n1'])])
    expect(wb.focused).toBe('s:a')

    const moved = new FakeWorkbench([a, thread('s:b')])
    moved.mount('s:a')
    moved.autoFocus.launched('n1')
    moved.refresh([a, thread('s:b')])
    moved.pick('s:b')
    moved.refresh([a, thread('s:b'), thread('s:n', ['n1'])])
    expect(moved.focused).toBe('s:b')
  })
})
