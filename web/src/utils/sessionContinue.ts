// 「接着聊」（R14）：已结束的 ACP 会话用现有续接接口继续——POST /v1/jobs/{id}/resume
// （mode=session，空指令）：服务端新开一个持续会话、`session/load` 恢复上下文后停在
// 等待输入（S 批 S2 语义，已有实现，这里只是把入口补到三处）。
import { resumeJob } from '../api/client'
import type { Job, WorkbenchThread } from '../api/types'
import { acpThreadIsEnded } from './acpThreads'

// canContinueThread：ACP（非交互终端）会话、已结束、且服务端说可续接。
export function canContinueThread(
  thread: Pick<WorkbenchThread, 'kind' | 'interactive' | 'resumable' | 'status' | 'raw_status' | 'latest_job_id'>,
): boolean {
  if (thread.kind !== 'agent') return false
  if (thread.interactive) return false
  if (!thread.resumable || !thread.latest_job_id) return false
  return acpThreadIsEnded(thread)
}

// continueThread 调现有续接接口（新开持续会话），返回新 job（其 session_id 链回原会话）。
// 空指令是 ACP 续接的常态：拿到上下文后停在等待输入等人说话。
export async function continueThread(
  thread: Pick<WorkbenchThread, 'latest_job_id'>,
): Promise<Job> {
  const jobID = thread.latest_job_id ?? ''
  if (!jobID) throw new Error('thread has no job to continue')
  return resumeJob(jobID, '', { mode: 'session' })
}
