// 顶栏「新版本」提示（gofer-29gi）：读 server 最近一次检查更新的结果（GET /v1/update）。
// server 自己按 server.update.interval（默认 2h）检查，这里只需偶尔读一次缓存结果。
import { ref } from 'vue'
import { getUpdateStatus } from '../api/client'
import type { UpdateStatus } from '../api/types'

// 有新版本时为最新版本号，否则为空。
export const updateLatest = ref('')

const pollMs = 10 * 60 * 1000
let timer: ReturnType<typeof setInterval> | null = null

export function noteUpdateStatus(st: UpdateStatus | null): void {
  updateLatest.value = st?.newer && st.latest ? st.latest : ''
}

export async function refreshUpdateAvailable(): Promise<void> {
  try {
    noteUpdateStatus(await getUpdateStatus())
  } catch {
    // 读不到（server 重启中 / 没有检查器）就保持原样，下次再试
  }
}

export function startUpdateAvailable(): void {
  if (timer) return
  void refreshUpdateAvailable()
  timer = setInterval(() => void refreshUpdateAvailable(), pollMs)
}

export function stopUpdateAvailable(): void {
  if (timer) clearInterval(timer)
  timer = null
  updateLatest.value = ''
}
