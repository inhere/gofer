// 会话授权列表（GET /v1/auth/grants 里 session:* 的行）：Sessions 页与会话抽屉共用一份。
// 只有根管理员能读；别人不请求（会 403），徽标也就不显示。
import { ref } from 'vue'
import { listAuthGrants } from '../api/permissions'
import type { AuthGrant } from '../utils/permissions'
import { parseSubject } from '../utils/permissions'

const grants = ref<AuthGrant[]>([])
let inflight: Promise<void> | null = null

export function useSessionGrants() {
  return { grants }
}

// 重新读一遍；失败保持旧值（徽标只是附加信息，不打断会话页）。并发调用合并。
export function refreshSessionGrants(): Promise<void> {
  if (inflight) return inflight
  inflight = listAuthGrants()
    .then((r) => {
      grants.value = r.grants.filter((g) => parseSubject(g.subject)?.kind === 'session')
    })
    .catch(() => undefined)
    .finally(() => {
      inflight = null
    })
  return inflight
}

export function clearSessionGrants(): void {
  grants.value = []
}
