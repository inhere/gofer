import type { WorkbenchThread } from '../../api/types'

// 工作台的「自动定位」：除了用户自己点选会话，界面只在两种场景下替用户挪焦点——
//  - 从发起栏新建会话后，等新 job 出现在线程列表里就定位到它；
//  - 通知 / 今天 / 会话页带 `?thread=` 的深链接，定位到目标会话（还没出现就等下一次刷新）。
//
// 两种都是一次性意图（gofer-2seo）：
//  - 定位成功就消费掉（深链接同时从地址里去掉），之后的推送刷新不再重复定位；
//  - 还没定位成功时，用户只要把焦点挪到别的会话（侧栏 / 命令面板 / 窗格 / 标签 / 移动端点选），
//    意图就作废——否则用户切走后，下一次推送刷新会把焦点拽回去。
//  - 在已打开的会话里回复不产生定位意图：新一轮本来就落在这个会话所在的窗格里。
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

interface Intent {
  target: string
  // 登记意图时聚焦的会话；焦点变了说明用户已经自己选了别的会话。
  base: string
}

export function createThreadAutoFocus(host: ThreadAutoFocusHost): ThreadAutoFocus {
  let pendingJob: Intent | null = null
  let pendingRequest: Intent | null = null
  // 已定位过的深链接；地址清理是异步的，期间的刷新不能再定位一次。
  let consumedRequest = ''

  function userMoved(intent: Intent): boolean {
    return host.focusedThreadID() !== intent.base
  }

  function launched(jobID: string): void {
    pendingJob = { target: jobID, base: host.focusedThreadID() }
    host.setSelectedID(`j:${jobID}`)
  }

  function continued(): void {
    // 回复 / 续接的新一轮属于当前会话，它已经在自己的窗格里；不登记定位意图。
  }

  function consumeRequest(id: string): void {
    pendingRequest = null
    consumedRequest = id
    host.clearRequestedThread()
  }

  function locateRequested(threadsByID: Map<string, WorkbenchThread>, announce: boolean): boolean {
    const id = host.requestedThreadID()
    if (!id) {
      pendingRequest = null
      consumedRequest = ''
      return false
    }
    if (!announce) {
      if (id === consumedRequest) return false
      if (pendingRequest && (pendingRequest.target !== id || userMoved(pendingRequest))) {
        consumeRequest(id)
        return false
      }
    }
    const thread = threadsByID.get(id)
    if (!thread) {
      if (announce) {
        pendingRequest = { target: id, base: host.focusedThreadID() }
        host.notice('通知指向的会话尚未出现，正在等待刷新')
      }
      return false
    }
    const changed = host.focusedThreadID() !== id
    host.selectThread(thread)
    consumeRequest(id)
    if (announce && changed) host.notice('已定位到通知对应的会话')
    return true
  }

  function refreshed(threadsByID: Map<string, WorkbenchThread>, layoutReady: boolean): void {
    if (pendingJob) {
      if (layoutReady && userMoved(pendingJob)) {
        pendingJob = null
      } else {
        const jobID = pendingJob.target
        const match = [...threadsByID.values()].find((thread) => thread.job_ids?.includes(jobID))
        if (match) {
          pendingJob = null
          host.selectThread(match)
        }
      }
    }
    if (layoutReady) locateRequested(threadsByID, false)
  }

  return { launched, continued, locateRequested, refreshed }
}
