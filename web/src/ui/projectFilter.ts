// 项目筛选的取值（设计稿 §6.4）：列表头的选择写进 URL 的 ?project=，它是唯一真源，
// 因此可以分享、可以前进/后退复原。localStorage 的 gofer.lastProject 只做一件事：
// 新建表单的默认值（来源列表没带筛选时用它）。
import { computed, ref, type ComputedRef, type Ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listProjects } from '../api/client'

export const PROJECT_QUERY = 'project'
export const LAST_PROJECT_KEY = 'gofer.lastProject'

function safeStorage(): Storage | null {
  try {
    return typeof window !== 'undefined' && window.localStorage ? window.localStorage : null
  } catch {
    // 隐私模式 / 存储被禁用：当作没有记忆
    return null
  }
}

export function readProjectQuery(query: Record<string, unknown> | undefined): string {
  const raw = query?.[PROJECT_QUERY]
  return typeof raw === 'string' ? raw : ''
}

export function readLastProject(storage: Storage | null = safeStorage()): string {
  try {
    return storage?.getItem(LAST_PROJECT_KEY) ?? ''
  } catch {
    return ''
  }
}

export function rememberProject(project: string, storage: Storage | null = safeStorage()): void {
  if (!project) {
    return
  }
  try {
    storage?.setItem(LAST_PROJECT_KEY, project)
  } catch {
    // 存不下就算了，只是默认值
  }
}

// 新建表单的默认项目：来源列表当前的筛选值优先，其次上次用过的项目。
export function defaultProject(query: Record<string, unknown> | undefined, storage?: Storage | null): string {
  return readProjectQuery(query) || readLastProject(storage === undefined ? safeStorage() : storage)
}

export interface ProjectFilterState {
  /** URL 里的 ?project=（空串 = 全部项目） */
  project: ComputedRef<string>
  setProject: (value: string) => void
  options: Ref<string[]>
  loadOptions: () => Promise<void>
  loadError: Ref<string>
}

// 列表页接线：读 URL、写 URL 的唯一入口。写 URL 后由页面自己的 watch 重新取数。
export function useProjectFilter(path: string, initialOptions: string[] = []): ProjectFilterState {
  const route = useRoute()
  const router = useRouter()
  const options = ref<string[]>([...initialOptions])
  const loadError = ref('')
  const project = computed(() => readProjectQuery(route.query))

  function setProject(value: string): void {
    const next: Record<string, string> = {}
    for (const [k, v] of Object.entries(route.query)) {
      if (typeof v === 'string' && v) next[k] = v
    }
    if (value) {
      next[PROJECT_QUERY] = value
    } else {
      delete next[PROJECT_QUERY]
    }
    // 换项目回到第一页（否则换完还停在 offset=40 会看到空页）
    delete next.offset
    rememberProject(value)
    void router.push({ path, query: next })
  }

  async function loadOptions(): Promise<void> {
    try {
      const resp = await listProjects()
      options.value = resp.projects ?? []
      loadError.value = ''
    } catch (e) {
      loadError.value = e instanceof Error ? e.message : String(e)
    }
  }

  return { project, setProject, options, loadOptions, loadError }
}
