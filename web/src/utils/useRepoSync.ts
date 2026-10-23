// 「同步仓库」（TRK-05）：server 把 `gofer repo sync` 派成一个隐藏 exec job，页面轮询它到结束。
// Issues 页与记忆页共用同一套接线（两页都能看到仓库同步的状态与按钮），文案由页面负责。
import { ref, type Ref } from 'vue'
import { getJob, logsTail, syncTrackerRepo } from '../api/client'
import { runTrackerSync } from './trackerSync'

export interface RepoSyncResult {
  ok: boolean
  jobId: string
  // 失败时的原因（成功为空串）
  message: string
}

export interface RepoSyncState {
  syncing: Ref<boolean>
  syncRepo: (trackerId: string) => Promise<RepoSyncResult | null>
}

export function useRepoSync(): RepoSyncState {
  const syncing = ref(false)

  async function syncRepo(trackerId: string): Promise<RepoSyncResult | null> {
    if (syncing.value || !trackerId) return null
    syncing.value = true
    try {
      const out = await runTrackerSync({
        start: () => syncTrackerRepo(trackerId),
        getJob: (id) => getJob(id),
        stderrTail: (id) => logsTail(id, 'stderr', 4096),
        sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      })
      return { ok: out.state === 'ok', jobId: out.jobId, message: out.message }
    } catch (e) {
      return { ok: false, jobId: '', message: e instanceof Error ? e.message : String(e) }
    } finally {
      syncing.value = false
    }
  }

  return { syncing, syncRepo }
}
