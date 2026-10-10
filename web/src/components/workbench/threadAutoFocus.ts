import type { WorkbenchThread } from '../../api/types'

// 工作台的「自动定位」：除了用户自己点选会话，界面只在两种场景下替用户挪焦点——
//  - 从发起栏新建会话后，等新 job 出现在线程列表里就定位到它；
//  - 通知 / 今天 / 会话页带 `?thread=` 的深链接，定位到目标会话（还没出现就等下一次刷新）。
export interface ThreadAutoFocusHost {
  // 路由里 `?thread=` 指向的会话 id（没有则为空串）。
  requestedThreadID(): string
  // 深链接已处理：把 `?thread=` 从地址里去掉。
  clearRequestedThread(): void
  // 当前聚焦窗格里的会话 id（空窗格为空串）。
  focusedThreadID(): string
  // 把会话放进聚焦窗格并选中（移动端同时切到对话栏）。
  selectThread(thread: WorkbenchThread): void
  setSelectedID(id: string): void
  notice(message: string): void
}

export interface ThreadAutoFocus {
  // 发起栏新建了一个会话（job）。
  launched(jobID: string): void
  // 在已打开的会话里续了一轮（回复 / 审阅续接）。
  continued(jobID?: string): void
  // 定位深链接目标；announce=true 表示这是一次新请求（进入页面 / 地址变化）。
  locateRequested(threadsByID: Map<string, WorkbenchThread>, announce: boolean): boolean
  // 线程列表刷新（推送失效 / 兜底轮询 / 手动刷新）之后调用。
  refreshed(threadsByID: Map<string, WorkbenchThread>, layoutReady: boolean): void
}

export function createThreadAutoFocus(host: ThreadAutoFocusHost): ThreadAutoFocus {
  let pendingJobID = ''

  function launched(jobID: string): void {
    pendingJobID = jobID
    host.setSelectedID(`j:${jobID}`)
  }

  function continued(jobID?: string): void {
    if (jobID) launched(jobID)
  }

  function locateRequested(threadsByID: Map<string, WorkbenchThread>, announce: boolean): boolean {
    const id = host.requestedThreadID()
    if (!id) return false
    const thread = threadsByID.get(id)
    if (!thread) {
      if (announce) host.notice('通知指向的会话尚未出现，正在等待刷新')
      return false
    }
    const changed = host.focusedThreadID() !== id
    host.selectThread(thread)
    if (announce && changed) host.notice('已定位到通知对应的会话')
    return true
  }

  function refreshed(threadsByID: Map<string, WorkbenchThread>, layoutReady: boolean): void {
    if (pendingJobID) {
      const match = [...threadsByID.values()].find((thread) => thread.job_ids?.includes(pendingJobID))
      if (match) {
        host.selectThread(match)
        pendingJobID = ''
      }
    }
    if (layoutReady) locateRequested(threadsByID, false)
  }

  return { launched, continued, locateRequested, refreshed }
}
