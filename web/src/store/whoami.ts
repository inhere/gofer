// whoami 缓存：我是谁、持有哪些权限、是不是根管理员。整个控制台共用一份，
// 加载一次；登录变化（换 / 清 token）与配置重载后刷新——权限来自配置与数据库授权，两者都会变。
// 取不到时（旧 server、网络）保持 null：界面不隐藏任何操作，由服务端的 403 说话。
import { computed, ref } from 'vue'
import { getWhoami } from '../api/permissions'
import { ceilingReadOnly, callerCeiling } from '../utils/ceiling'
import { canDo, holdsPermission } from '../utils/permissionGate'
import type { GateAction } from '../utils/permissionGate'
import { manageMode } from '../utils/permissions'
import type { AuthWhoami } from '../utils/permissions'
import { hasToken, onAuthChange } from './auth'

const whoami = ref<AuthWhoami | null>(null)
const loading = ref(false)
const loadError = ref('')
let inflight: Promise<void> | null = null
// 登录变化后作废在途请求，免得旧身份的回答落进新身份。
let generation = 0

export function loadWhoami(force = false): Promise<void> {
  if (!force && (whoami.value || inflight)) return inflight ?? Promise.resolve()
  if (!hasToken()) {
    whoami.value = null
    return Promise.resolve()
  }
  const mine = ++generation
  loading.value = true
  const p: Promise<void> = getWhoami()
    .then((w) => {
      if (mine !== generation) return
      whoami.value = w
      loadError.value = ''
    })
    .catch((e: unknown) => {
      if (mine !== generation) return
      loadError.value = e instanceof Error ? e.message : String(e)
    })
    .finally(() => {
      if (mine === generation) {
        loading.value = false
        inflight = null
      }
    })
  inflight = p
  return p
}

// 配置重载后调用：权限可能变了。
export function refreshWhoami(): Promise<void> {
  return loadWhoami(true)
}

function resetWhoami(): void {
  generation++
  whoami.value = null
  inflight = null
  loading.value = false
  loadError.value = ''
  if (hasToken()) void loadWhoami(true)
}

onAuthChange(resetWhoami)

export function useWhoami() {
  void loadWhoami()
  return {
    whoami,
    loading,
    loadError,
    // 有效权限名集合；未取到时为空集（用 can() 判断，别直接拿集合判）。
    permissionSet: computed(() => new Set((whoami.value?.permissions ?? []).map((p) => p.name))),
    mode: computed(() => manageMode(whoami.value)),
    // 调用方显示名（id → 名称）；配合 utils/callerLabel 的 callerLabel / callerTitle 使用。
    callerNames: computed(() => whoami.value?.caller_names ?? {}),
    isRoot: computed(() => whoami.value?.root === true),
    // 本人调用方上限（没有上限 / 旧服务端 → null）与它派生的只读：
    // 只读时所有写入口都该消失（服务端也会 403）。
    ceiling: computed(() => callerCeiling(whoami.value)),
    readOnly: computed(() => ceilingReadOnly(whoami.value?.ceiling)),
    // 动作要显示吗（见 utils/permissionGate.ts 的动作表）。
    can: (action: GateAction, ownerId?: string): boolean => canDo(whoami.value, action, ownerId),
    holds: (perm: string): boolean => holdsPermission(whoami.value, perm),
  }
}
