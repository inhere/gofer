<script setup lang="ts">
// Projects：左列项目 keys（listProjects），选中拉详情（getProject），右列展示配置与项目写层。
// P3 增强（E20/E32）：右列再加 git 状态卡 + 子仓库列表 + 关键文件查看（FilePreview）。
// 刷新策略（D7）：进入项目详情时取 git/repos，git 卡提供手动刷新按钮，不轮询。
// P5（gofer-d1kn）：「按项目 / 按执行机」视图切换（?view=project|runner）。按执行机视图每台
// 执行机一组：server 声明且 allowed_runners 含它的项目 + worker 自有项目 + 它的默认工作空间；
// 按项目视图里 worker 自有项目另列一组，内置 default 标明「server 本机」。只用现有接口。
import { runnerLabel } from '../utils/runnerDisplay'
import { t } from '../i18n'
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWhoami } from '../store/whoami'
import {
  ApiError,
  createProject,
  deleteProject,
  getConfig,
  getProject,
  getProjectFile,
  getProjectGit,
  getWorkerProjects,
  getWorkerView,
  listJobs,
  listPlans,
  listProjects,
  listRepos,
  listRunners,
  updateProject,
} from '../api/client'
import type {
  ConfigView,
  GitStatus,
  ProjectDetail,
  ProjectWriteReq,
  ProjectWriteResp,
  RepoInfo,
  Runner,
  WorkerView,
} from '../api/types'
import FilePreview from '../components/FilePreview.vue'
import Tabs from '../ui/Tabs.vue'
import StatusPill from '../ui/StatusPill.vue'
import type { TabItem } from '../ui/tabs'
import { buildFleet, projectAllowedFromConfig, workerOwnProjects, type FleetGroup, type FleetNote } from '../utils/projectFleet'

const route = useRoute()
const router = useRouter()

const keys = ref<string[]>([])
// 内置项目的 key（server 注入、配置里没写）：列表与详情标「内置」，删除按钮禁用。
const injectedKeys = ref<string[]>([])
const selected = ref<string>('')
const detail = ref<ProjectDetail | null>(null)
const loadingList = ref(false)
const loadingDetail = ref(false)
const listError = ref('')
const detailError = ref('')
const config = ref<ConfigView | null>(null)
const configError = ref('')
const saving = ref(false)
const deleting = ref(false)
const formError = ref('')
const notice = ref('')
const warnings = ref<string[]>([])
// 项目增删改需要 project.write；没有就不摆新增 / 编辑（只读浏览仍可用）。
const { can } = useWhoami()
const mode = ref<'none' | 'create' | 'edit'>('none')

// 设计稿 §6.4-5：项目概览的计数（活跃任务 / 进行中计划），进详情时取一次；失败就整块不显示，
// 不影响配置与 git 展示。
const counts = ref<{ jobs: number; plans: number } | null>(null)

async function loadCounts(key: string): Promise<void> {
  counts.value = null
  try {
    const [jobsResp, plansResp] = await Promise.all([
      listJobs({ project: key, status: 'running', limit: 100 }),
      listPlans({ project: key, status: 'open', limit: 1 }),
    ])
    counts.value = { jobs: jobsResp.jobs?.length ?? 0, plans: plansResp.total ?? 0 }
  } catch {
    counts.value = null
  }
}

interface ProjectForm {
  key: string
  host_path: string
  container_path: string
  default_agent: string
  allowed_agents: string[]
  allowed_runners: string[]
  allow_exec: boolean
  // 交互 job 总开关（AGT-02 §2）：项目侧唯一的交互闸（0.3 已移除按 agent 收窄的历史名单）。
  allow_interactive: boolean
  max_concurrent_jobs: string
  // 项目级强制规则绑定（JOB-06①）：每行一个规则名（来自规则库 /settings/rules），与
  // server/agent 层的绑定取并集，项目永远不能取消 server 的规则。
  rulesText: string
}

const form = reactive<ProjectForm>({
  key: '',
  host_path: '',
  container_path: '',
  default_agent: '',
  allowed_agents: [],
  allowed_runners: [],
  allow_exec: false,
  allow_interactive: false,
  max_concurrent_jobs: '',
  rulesText: '',
})

const agents = computed(() => config.value?.agents ?? [])
const runners = computed(() => config.value?.runners ?? [])
const runnerOptions = computed(() => [
  'local',
  ...runners.value.map((r) => r.key).filter((key) => key !== 'local'),
])
const isEditing = computed(() => mode.value === 'edit')
const canSubmit = computed(() => !saving.value && form.key.trim() !== '' && form.host_path.trim() !== '')

// 项目是否允许交互 job（AGT-02 §2）：只看 allow_interactive。旧 server（控制台热更、二进制未更新
// → 运行时无该字段）读作 false —— 与后端"未写 = 关"一致；此时若项目确实要跑交互 job，操作者需在
// 表单里显式勾选一次（旧的按 agent 收窄名单已随 0.3 移除，不再有可回退的判据）。
function effectiveAllowInteractive(p: ProjectDetail): boolean {
  return p.allow_interactive ?? false
}

// 详情行展示用的开关。
const detailAllowInteractive = computed(() => (detail.value ? effectiveAllowInteractive(detail.value) : false))

// git 状态卡（E20）
const gitStatus = ref<GitStatus | null>(null)
const gitLoading = ref(false)
const gitError = ref('')

// 子仓库列表（E32）
const repos = ref<RepoInfo[]>([])
const reposLoading = ref(false)
const reposError = ref('')

// 关键文件（E32）：白名单候选 → 点击拉内容 → 包 Blob 交 FilePreview 渲染。
const KEY_FILE_CANDIDATES = [
  'README.md',
  'AGENTS.md',
  'CLAUDE.md',
  'go.mod',
  'package.json',
  '.gitignore',
  'LICENSE',
]
const activeFile = ref('') // 当前选中的候选文件名
const fileName = ref('') // 后端回传的 basename（传给 FilePreview 判类型）
const fileBlob = ref<Blob | null>(null) // content 文本包成的 Blob
const fileTruncated = ref(false)
const fileLoading = ref(false)
const fileErr = ref('')

async function loadList(preferredKey = '', keepMessages = false) {
  loadingList.value = true
  listError.value = ''
  try {
    const resp = await listProjects()
    keys.value = resp.projects ?? []
    injectedKeys.value = resp.injected ?? []
    const nextKey =
      preferredKey && keys.value.includes(preferredKey)
        ? preferredKey
        : keys.value[0] ?? ''
    if (nextKey) {
      await selectKey(nextKey, keepMessages)
    } else {
      selected.value = ''
      detail.value = null
      resetForm()
    }
  } catch (e) {
    listError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingList.value = false
  }
}

async function loadConfigOptions() {
  configError.value = ''
  try {
    config.value = await getConfig()
  } catch (e) {
    configError.value = e instanceof Error ? e.message : String(e)
  }
}

// ── 「按项目 / 按执行机」视图（P5，gofer-d1kn）─────────────────────────────
// 视图切换走 URL 的 ?view=：默认「按项目」不写进 URL，?view=runner 可深链（ui/Tabs 的 param）。
const viewTabs = computed<TabItem[]>(() => [
  { value: 'project', label: t('projects.view.project'), count: keys.value.length },
  { value: 'runner', label: t('projects.view.runner'), count: runnerGroups.value.length },
])

// 执行机数据（只用现有接口）：/v1/runners 列执行机；/v1/workers/{id} 取默认工作空间（dirs.workspace）；
// /v1/workers/{id}/projects 取 worker 自有项目。allowed_runners 逐项目取——有配置读权限时
// 一次从 /v1/config 拿全。
const fleetRunners = ref<Runner[]>([])
const workerViews = reactive<Record<string, WorkerView | undefined>>({})
const workerProjects = reactive<Record<string, string[] | undefined>>({})
const workerErrors = reactive<Record<string, string | undefined>>({})
const projectAllowed = reactive<Record<string, string[] | undefined>>({})
const fleetLoading = ref(false)
const fleetError = ref('')

const workerIDs = computed(() =>
  [...new Set(fleetRunners.value.filter((r) => r.type === 'worker').map((r) => r.worker_id || r.name))].sort(),
)

const runnerGroups = computed<FleetGroup[]>(() =>
  buildFleet({
    projects: keys.value,
    injected: injectedKeys.value,
    allowed: projectAllowed,
    runners: fleetRunners.value,
    workerViews,
    workerProjects,
    workerErrors,
  }),
)

// 「按项目」视图里的 worker 自有项目（server 配置里没有的那些）。
const ownProjects = computed(() => workerOwnProjects({ projects: keys.value, workerProjects }))

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

// 取不到数据时的原因文案（FleetNote）。
function fleetNote(note: FleetNote): string {
  if (note === 'offline') return t('projects.fleet.offlineData')
  if (note === 'notReported') return t('projects.fleet.notReported')
  return ''
}

async function loadProjectAllowed(): Promise<void> {
  Object.assign(projectAllowed, projectAllowedFromConfig(config.value?.projects))
  const todo = keys.value.filter((key) => projectAllowed[key] === undefined)
  await Promise.all(
    todo.map(async (key) => {
      try {
        const detail = await getProject(key)
        projectAllowed[key] = detail.allowed_runners ?? []
      } catch {
        // 取不到就不猜：undefined 按「只在本机可用」处理（见 utils/projectFleet.ts）。
      }
    }),
  )
}

async function loadFleet(): Promise<void> {
  fleetLoading.value = true
  fleetError.value = ''
  try {
    const resp = await listRunners()
    fleetRunners.value = resp.runners ?? []
  } catch (e) {
    fleetError.value = errText(e)
  }
  await Promise.all([
    loadProjectAllowed(),
    ...workerIDs.value.map(async (id) => {
      delete workerErrors[id]
      try {
        workerViews[id] = await getWorkerView(id)
      } catch (e) {
        workerViews[id] = undefined
        workerErrors[id] = errText(e)
      }
      try {
        workerProjects[id] = (await getWorkerProjects(id)).projects ?? []
      } catch (e) {
        workerProjects[id] = undefined
        workerErrors[id] = errText(e)
      }
    }),
  ])
  fleetLoading.value = false
}

// 「按执行机」视图里点项目 chip：回到「按项目」视图并选中它（默认页签不写进 URL）。
function openProject(key: string): void {
  void router.replace({ path: route.path, query: {} })
  if (keys.value.includes(key)) void selectKey(key)
}

async function selectKey(key: string, keepMessages = false) {
  selected.value = key
  detail.value = null
  detailError.value = ''
  loadingDetail.value = true
  mode.value = 'edit'
  if (!keepMessages) {
    formError.value = ''
    notice.value = ''
    warnings.value = []
  }
  // 切项目：重置 git/repos/关键文件/计数状态。
  counts.value = null
  resetFile()
  try {
    detail.value = await getProject(key)
    fillForm(detail.value)
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingDetail.value = false
  }
  // 详情拉到后并行取 git + repos + 计数（失败各自降级，不影响配置展示）。
  void loadGit(key)
  void loadRepos(key)
  void loadCounts(key)
}

function startCreate(): void {
  selected.value = ''
  detail.value = null
  detailError.value = ''
  mode.value = 'create'
  formError.value = ''
  notice.value = ''
  warnings.value = []
  gitStatus.value = null
  repos.value = []
  counts.value = null
  resetFile()
  Object.assign(form, {
    key: '',
    host_path: '',
    container_path: '',
    default_agent: agents.value[0]?.key ?? '',
    allowed_agents: [],
    allowed_runners: ['local'],
    allow_exec: false,
    allow_interactive: false,
    max_concurrent_jobs: '',
    rulesText: '',
  })
}

function fillForm(p: ProjectDetail): void {
  Object.assign(form, {
    key: p.key,
    host_path: p.host_path,
    container_path: p.container_path ?? '',
    default_agent: p.default_agent ?? '',
    allowed_agents: [...(p.allowed_agents ?? [])],
    allowed_runners: [...(p.allowed_runners ?? [])],
    allow_exec: p.allow_exec,
    allow_interactive: effectiveAllowInteractive(p),
    max_concurrent_jobs: p.max_concurrent_jobs != null ? String(p.max_concurrent_jobs) : '',
    rulesText: (p.rules ?? []).join('\n'),
  })
}

function resetForm(): void {
  mode.value = 'none'
  Object.assign(form, {
    key: '',
    host_path: '',
    container_path: '',
    default_agent: '',
    allowed_agents: [],
    allowed_runners: [],
    allow_exec: false,
    allow_interactive: false,
    max_concurrent_jobs: '',
    rulesText: '',
  })
}

function toggleAgent(key: string): void {
  form.allowed_agents = toggleValue(form.allowed_agents, key)
  if (form.default_agent && form.allowed_agents.length > 0 && !form.allowed_agents.includes(form.default_agent)) {
    form.default_agent = ''
  }
}

function toggleRunner(key: string): void {
  form.allowed_runners = toggleValue(form.allowed_runners, key)
}

function toggleValue(list: string[], value: string): string[] {
  return list.includes(value) ? list.filter((v) => v !== value) : [...list, value]
}

// 保存时**整体**下发表单（AGT-02 §2）：PUT 是合并语义 —— 缺字段 = 保持不变，存在即覆盖。
// 所以清空的字段要用 []/0/'' 显式表达，不能靠省略：省略 allowed_agents 这种数组会让"取消最后一个
// 勾选"发不出 []（h-aii-3scy 的另一面）。
function buildReq(): ProjectWriteReq {
  const max = Number.parseInt(form.max_concurrent_jobs, 10)
  return {
    key: form.key.trim(),
    host_path: form.host_path.trim(),
    container_path: form.container_path.trim(),
    default_agent: form.default_agent,
    allowed_agents: [...form.allowed_agents],
    allowed_runners: [...form.allowed_runners],
    allow_exec: form.allow_exec,
    max_concurrent_jobs: Number.isFinite(max) && max > 0 ? max : 0,
    allow_interactive: form.allow_interactive,
    // JOB-06①：规则绑定逐行一个名字。显式发数组（含空数组）才能表达"取消最后一个绑定"——
    // 省略字段在 PUT 的合并语义里等于"保持不变"。
    rules: form.rulesText
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l !== ''),
  }
}

async function saveProject(): Promise<void> {
  formError.value = ''
  notice.value = ''
  warnings.value = []
  if (!canSubmit.value) {
    formError.value = t('projects.err.needFields')
    return
  }
  saving.value = true
  try {
    const req = buildReq()
    const resp =
      mode.value === 'create'
        ? await createProject(req)
        : await updateProject(selected.value, req)
    await applyWriteSuccess(resp, mode.value === 'create' ? t('projects.notice.created') : t('projects.notice.saved'))
  } catch (e) {
    formError.value = classifyWriteError(e)
  } finally {
    saving.value = false
  }
}

async function removeProject(): Promise<void> {
  const key = selected.value
  if (!key || !window.confirm(t('projects.confirmDelete', { key }))) {
    return
  }
  formError.value = ''
  notice.value = ''
  warnings.value = []
  deleting.value = true
  try {
    await deleteProject(key)
    notice.value = t('projects.notice.deleted')
    resetForm()
    await loadList('', true)
    delete projectAllowed[key]
    void loadFleet()
  } catch (e) {
    formError.value = classifyWriteError(e)
  } finally {
    deleting.value = false
  }
}

async function applyWriteSuccess(resp: ProjectWriteResp, message: string): Promise<void> {
  notice.value = message
  warnings.value = resp.warnings ?? []
  selected.value = resp.key
  mode.value = 'edit'
  fillForm(resp)
  await loadList(resp.key, true)
  // 准入名单可能改了：「按执行机」视图立刻按新名单分组。
  projectAllowed[resp.key] = resp.allowed_runners ?? []
  void loadFleet()
}

function classifyWriteError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) {
      return t('projects.err.noAccess')
    }
    if (e.status === 409) {
      return t('projects.err.exists')
    }
    if (e.status === 400) {
      return t('projects.err.invalid', { detail: e.detail || e.message })
    }
    if (e.status === 404) {
      return t('projects.err.notFound', { detail: e.detail || e.message })
    }
  }
  return e instanceof Error ? e.message : String(e)
}

async function loadGit(key: string) {
  gitLoading.value = true
  gitError.value = ''
  gitStatus.value = null
  try {
    gitStatus.value = await getProjectGit(key)
  } catch (e) {
    gitError.value = e instanceof Error ? e.message : String(e)
  } finally {
    gitLoading.value = false
  }
}

async function loadRepos(key: string) {
  reposLoading.value = true
  reposError.value = ''
  repos.value = []
  try {
    const resp = await listRepos(key)
    repos.value = resp.repos ?? []
  } catch (e) {
    reposError.value = e instanceof Error ? e.message : String(e)
  } finally {
    reposLoading.value = false
  }
}

function refreshGit() {
  if (selected.value) {
    void loadGit(selected.value)
  }
}

function resetFile() {
  activeFile.value = ''
  fileName.value = ''
  fileBlob.value = null
  fileTruncated.value = false
  fileErr.value = ''
  fileLoading.value = false
}

async function openFile(name: string) {
  if (!selected.value) {
    return
  }
  activeFile.value = name
  fileLoading.value = true
  fileErr.value = ''
  fileBlob.value = null
  fileName.value = ''
  fileTruncated.value = false
  try {
    const fc = await getProjectFile(selected.value, name)
    // FilePreview 吃 Blob，而 /file 回的是 JSON 文本 → 把 content 包成 Blob 传入。
    fileBlob.value = new Blob([fc.content])
    fileName.value = fc.name
    fileTruncated.value = fc.truncated
  } catch (e) {
    // 文件不存在（404）/ 非白名单（403）/ 二进制（415）等：优雅提示，不破坏页面。
    fileErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    fileLoading.value = false
  }
}

// FilePreview 回退下载（理论上关键文件 ≤256KB 且文本，几乎不触发）：从已载入 Blob 本地另存。
function downloadActiveFile() {
  if (!fileBlob.value) {
    return
  }
  const url = URL.createObjectURL(fileBlob.value)
  const a = document.createElement('a')
  a.href = url
  a.download = fileName.value || activeFile.value
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// Unix 秒 → 本地可读时间。
function fmtTs(ts: number): string {
  if (!ts) {
    return ''
  }
  return new Date(ts * 1000).toLocaleString()
}

function viewOnBoard(key: string) {
  void router.push({ path: '/jobs', query: { project: key } })
}

// 项目概览的「进行中计划」跳该项目的计划列表（设计稿 §6.4-5）。
function viewPlans(key: string) {
  void router.push({ path: '/plans', query: { project: key } })
}

onMounted(() => {
  void init()
})

// 先拉项目列表与配置（表单选项 + allowed_runners 的来源），再拉执行机数据。
async function init(): Promise<void> {
  await Promise.all([loadList(), loadConfigOptions()])
  await loadFleet()
}
</script>

<template>
  <div class="projects">
    <header class="page-head">
      <div>
        <h1 class="title">{{ t('nav.projects') }}</h1>
        <p class="sub">{{ t('projects.sub') }}</p>
      </div>
      <button v-if="can('project.write')" class="submit submit--small" type="button" data-test="project-new" @click="startCreate">
        {{ t('projects.new') }}
      </button>
    </header>
    <p v-if="listError" class="error mono">{{ listError }}</p>
    <p v-if="configError" class="error mono">{{ configError }}</p>

    <Tabs :tabs="viewTabs" param="view" :label="t('projects.view.label')">
      <template #default="{ active }">
        <!-- 按执行机（P5，gofer-d1kn）：server 本机 + 每个 worker 一组 -->
        <div v-if="active === 'runner'" class="fleet" data-test="projects-runner-view">
          <div class="fleet-head">
            <p class="fleet-hint mono">{{ t('projects.fleet.hint') }}</p>
            <button
              class="mini-btn mono"
              type="button"
              :disabled="fleetLoading"
              data-test="fleet-refresh"
              @click="loadFleet"
            >
              {{ fleetLoading ? t('hint.loading') : t('act.refresh') }}
            </button>
          </div>
          <p v-if="fleetError" class="error mono">{{ fleetError }}</p>
          <p v-if="fleetLoading && runnerGroups.length <= 1" class="placeholder mono">{{ t('hint.loading') }}</p>
          <ul class="groups" :aria-label="t('projects.fleet.aria')">
            <li
              v-for="g in runnerGroups"
              :key="g.id"
              class="group"
              :class="{ 'group--off': !g.online }"
              :data-test="`fleet-group-${g.kind}`"
            >
              <header class="group-head">
                <span class="group-name mono">{{ g.label }}</span>
                <span class="tag mono">
                  {{ g.kind === 'server' ? t('projects.fleet.local') : t('projects.fleet.worker') }}
                </span>
                <StatusPill
                  :family="g.online ? 'done' : 'idle'"
                  :label="g.online ? t('projects.fleet.online') : t('projects.fleet.offline')"
                />
              </header>

              <dl class="group-kv">
                <dt class="mono">{{ t('projects.fleet.workspace') }}</dt>
                <dd class="mono" data-test="fleet-workspace">
                  <template v-if="g.workspace">
                    <code class="path">{{ g.workspace.path }}</code>
                    <span v-if="g.workspace.exists === false" class="dim">{{ t('projects.fleet.workspaceMissing') }}</span>
                  </template>
                  <span v-else class="dim">{{ fleetNote(g.workspaceNote) }}</span>
                </dd>

                <dt class="mono">{{ t('projects.fleet.projects') }}</dt>
                <dd data-test="fleet-projects">
                  <template v-if="g.projects.length">
                    <button
                      v-for="p in g.projects"
                      :key="p.key"
                      type="button"
                      class="proj-chip mono"
                      @click="openProject(p.key)"
                    >
                      {{ p.key }}
                      <span v-if="p.injected" class="builtin-badge">{{ t('projects.builtin') }}</span>
                    </button>
                  </template>
                  <span v-else class="dim">{{ t('projects.fleet.noProjects') }}</span>
                </dd>

                <template v-if="g.kind === 'worker'">
                  <dt class="mono">{{ t('projects.fleet.own') }}</dt>
                  <dd data-test="fleet-own">
                    <template v-if="g.own.length">
                      <span
                        v-for="p in g.own"
                        :key="p.key"
                        class="proj-chip proj-chip--own mono"
                        :title="p.path || ''"
                      >
                        {{ p.key }}
                      </span>
                    </template>
                    <span v-else class="dim">
                      {{ fleetNote(g.ownNote) || t('projects.fleet.noOwn') }}
                    </span>
                  </dd>
                </template>
              </dl>
              <p v-if="g.error" class="group-error mono" data-test="fleet-group-error">
                {{ t('projects.fleet.error', { detail: g.error }) }}
              </p>
            </li>
          </ul>
        </div>

        <!-- 按项目：原有的主从布局 -->
        <template v-else>
    <label class="mobile-project-picker mono">
      <span>{{ t('projects.picker') }}</span>
      <select class="control mono" :value="selected" @change="selectKey(($event.target as HTMLSelectElement).value)">
        <option value="">{{ t('projects.pickerPlaceholder') }}</option>
        <option v-for="key in keys" :key="key" :value="key">{{ key }}</option>
      </select>
    </label>

    <div class="layout">
      <!-- 列表 -->
      <ul class="list" :aria-label="t('projects.listAria')">
        <li v-for="key in keys" :key="key">
          <button
            type="button"
            class="list-item mono"
            :class="{ 'list-item--active': key === selected }"
            :title="key"
            @click="selectKey(key)"
          >
            {{ key }}
            <span
              v-if="key === 'default'"
              class="builtin-badge"
              data-test="project-default-local"
              :title="t('projects.defaultLocalTitle')"
            >{{ t('projects.defaultLocal') }}</span>
            <span v-if="injectedKeys.includes(key)" class="builtin-badge" data-test="project-builtin-badge" :title="t('projects.builtinTitle')">{{ t('projects.builtin') }}</span>
          </button>
        </li>
        <li v-if="keys.length === 0 && !loadingList && !listError" class="list-empty mono">
          {{ t('projects.empty') }}
        </li>
        <li v-if="loadingList" class="list-empty mono">{{ t('hint.loading') }}</li>
      </ul>

      <!-- 详情 -->
      <section class="detail" :aria-label="t('projects.detailAria')">
        <p v-if="detailError" class="error mono">{{ detailError }}</p>
        <p v-else-if="loadingDetail" class="placeholder mono">{{ t('hint.loading') }}</p>
        <p v-else-if="!detail && mode !== 'create'" class="placeholder mono">{{ t('projects.placeholder') }}</p>

        <div v-if="detail" class="detail-body">
          <div class="detail-head">
            <span class="detail-key mono">{{ detail.key }}</span>
            <span
              v-if="detail.key === 'default'"
              class="builtin-badge"
              data-test="project-default-local"
              :title="t('projects.defaultLocalTitle')"
            >{{ t('projects.defaultLocal') }}</span>
            <span v-if="detail.injected" class="builtin-badge" data-test="project-builtin-detail" :title="t('projects.builtinTitle')">{{ t('projects.builtin') }}</span>
            <button class="board-link mono" type="button" @click="viewOnBoard(detail.key)">
              {{ t('projects.boardLink') }} &#9656;
            </button>
          </div>

          <!-- 项目概览（设计稿 §6.4-5）：活跃任务 / 进行中计划，各自跳到该项目的过滤列表。 -->
          <nav v-if="counts" class="counts" :aria-label="t('projects.counts.title')" data-test="project-counts">
            <button class="count-link" type="button" :title="t('projects.counts.jobsTitle')" @click="viewOnBoard(detail.key)">
              {{ t('projects.counts.jobs', { n: counts.jobs }) }}
            </button>
            <button class="count-link" type="button" :title="t('projects.counts.plansTitle')" @click="viewPlans(detail.key)">
              {{ t('projects.counts.plans', { n: counts.plans }) }}
            </button>
          </nav>

          <dl class="kv">
            <dt class="mono">host_path</dt>
            <dd class="mono">{{ detail.host_path }}</dd>

            <template v-if="detail.container_path">
              <dt class="mono">container_path</dt>
              <dd class="mono">{{ detail.container_path }}</dd>
            </template>

            <dt class="mono">default_agent</dt>
            <dd class="mono">{{ detail.default_agent || '—' }}</dd>

            <dt class="mono">allowed_agents</dt>
            <dd class="mono">
              <template v-if="detail.allowed_agents && detail.allowed_agents.length">
                <span v-for="a in detail.allowed_agents" :key="a" class="tag">{{ a }}</span>
              </template>
              <span v-else>—</span>
            </dd>

            <dt class="mono">allowed_runners</dt>
            <dd class="mono">
              <template v-if="detail.allowed_runners && detail.allowed_runners.length">
                <span v-for="r in detail.allowed_runners" :key="r" class="tag">{{ runnerLabel(r) }}</span>
              </template>
              <span v-else>—</span>
            </dd>

            <!-- JOB-06①：项目级强制规则绑定（与 server/agent 的绑定取并集，注入 prompt 顶部）。 -->
            <dt class="mono">rules</dt>
            <dd class="mono">
              <template v-if="detail.rules && detail.rules.length">
                <span v-for="r in detail.rules" :key="r" class="tag">{{ runnerLabel(r) }}</span>
              </template>
              <span v-else>—</span>
            </dd>

            <dt class="mono">allow_exec</dt>
            <dd class="mono">
              <span class="flag" :class="detail.allow_exec ? 'flag--yes' : 'flag--no'">
                {{ detail.allow_exec ? t('projects.flag.yes') : t('projects.flag.no') }}
              </span>
            </dd>

            <!-- 交互 job 总开关（AGT-02 §2）：项目侧唯一的交互闸（0.3 起按 agent 收窄的名单已移除）。 -->
            <dt class="mono">allow_interactive</dt>
            <dd class="mono">
              <span class="flag" :class="detailAllowInteractive ? 'flag--yes' : 'flag--no'">
                {{ detailAllowInteractive ? t('projects.flag.yes') : t('projects.flag.no') }}
              </span>
            </dd>

            <dt class="mono">max_concurrent_jobs</dt>
            <dd class="mono">{{ detail.max_concurrent_jobs ?? '—' }}</dd>
          </dl>
        </div>

        <section v-if="mode !== 'none' && can('project.write')" class="block edit-block" :aria-label="t('projects.editAria')">
          <div class="block-head">
            <h2 class="block-title mono">{{ t('projects.edit.title') }}</h2>
          </div>

          <p v-if="mode === 'edit' && detail?.injected" class="builtin-hint mono" data-test="project-builtin-hint">
            {{ t('projects.builtinHint') }}
          </p>
          <form class="form" @submit.prevent="saveProject">
            <div class="row">
              <div class="field">
                <label class="label mono" for="cfg-key">{{ t('projects.field.key') }}</label>
                <input
                  id="cfg-key"
                  v-model="form.key"
                  class="control mono"
                  :disabled="isEditing"
                  autocomplete="off"
                  spellcheck="false"
                />
              </div>
              <div class="field">
                <label class="label mono" for="cfg-default-agent">{{ t('projects.field.defaultAgent') }}</label>
                <select id="cfg-default-agent" v-model="form.default_agent" class="control mono">
                  <option value="">-</option>
                  <option v-for="a in agents" :key="a.key" :value="a.key">{{ a.key }}</option>
                </select>
              </div>
            </div>

            <div class="field">
              <label class="label mono" for="cfg-host">{{ t('projects.field.hostPath') }}</label>
              <input
                id="cfg-host"
                v-model="form.host_path"
                class="control mono"
                autocomplete="off"
                spellcheck="false"
              />
            </div>

            <div class="field">
              <label class="label mono" for="cfg-container">{{ t('projects.field.containerPath') }}</label>
              <input
                id="cfg-container"
                v-model="form.container_path"
                class="control mono"
                autocomplete="off"
                spellcheck="false"
                :placeholder="t('projects.field.optional')"
              />
            </div>

            <div class="row">
              <fieldset class="pick">
                <legend class="label mono">{{ t('projects.field.allowedAgents') }}</legend>
                <label v-for="a in agents" :key="a.key" class="check mono">
                  <input
                    type="checkbox"
                    :checked="form.allowed_agents.includes(a.key)"
                    @change="toggleAgent(a.key)"
                  />
                  <span>{{ a.key }}</span>
                </label>
                <p v-if="agents.length === 0" class="field-hint mono">{{ t('projects.field.noAgents') }}</p>
              </fieldset>

              <fieldset class="pick">
                <legend class="label mono">{{ t('projects.field.allowedRunners') }}</legend>
                <label v-for="r in runnerOptions" :key="r" class="check mono">
                  <input
                    type="checkbox"
                    :checked="form.allowed_runners.includes(r)"
                    @change="toggleRunner(r)"
                  />
                  <span>{{ runnerLabel(r) }}</span>
                </label>
              </fieldset>
            </div>

            <div class="row">
              <div class="field field--check">
                <label class="check mono">
                  <input v-model="form.allow_exec" type="checkbox" />
                  <span>allow_exec</span>
                </label>
              </div>
              <div class="field field--check">
                <label class="check mono">
                  <input v-model="form.allow_interactive" type="checkbox" />
                  <span>{{ t('projects.field.allowInteractive') }}</span>
                </label>
              </div>
              <div class="field">
                <label class="label mono" for="cfg-max">{{ t('projects.field.maxConcurrent') }}</label>
                <input
                  id="cfg-max"
                  v-model="form.max_concurrent_jobs"
                  class="control mono"
                  type="number"
                  min="1"
                  :placeholder="t('projects.field.maxConcurrentPlaceholder')"
                />
              </div>
            </div>

            <div class="field">
              <label class="label mono" for="cfg-rules">{{ t('projects.field.rules') }}</label>
              <textarea
                id="cfg-rules"
                v-model="form.rulesText"
                class="control mono"
                rows="3"
                spellcheck="false"
                placeholder="gofer-repo"
              ></textarea>
              <p class="field-hint mono">
                {{ t('projects.field.rulesHint') }}
                <RouterLink to="/settings/rules">{{ t('nav.settings') }} › Rules</RouterLink>
              </p>
            </div>

            <p v-if="formError" class="error mono">{{ formError }}</p>
            <p v-if="notice" class="notice mono">{{ notice }}</p>
            <div v-if="warnings.length" class="warn mono">
              <strong>{{ t('projects.warn.head') }}</strong>
              <ul>
                <li v-for="w in warnings" :key="w">{{ w }}</li>
              </ul>
            </div>

            <div class="actions">
              <button class="submit" type="submit" :disabled="!canSubmit">
                {{ saving ? t('projects.saving') : mode === 'create' ? t('projects.create') : t('projects.save') }}
              </button>
              <button
                v-if="isEditing"
                class="danger"
                type="button"
                :disabled="deleting || saving || !!detail?.injected"
                :title="detail?.injected ? t('projects.deleteBlocked') : ''"
                data-test="project-delete"
                @click="removeProject"
              >
                {{ deleting ? t('projects.deleting') : t('projects.delete') }}
              </button>
            </div>
          </form>
        </section>

        <div v-if="detail" class="detail-body">
          <!-- git 状态卡（E20） -->
          <section class="block" aria-label="git 状态">
            <div class="block-head">
              <h2 class="block-title mono">{{ t('projects.git.title') }}</h2>
              <button
                class="mini-btn mono"
                type="button"
                :disabled="gitLoading"
                @click="refreshGit"
              >
                {{ gitLoading ? t('hint.loading') : t('act.refresh') }}
              </button>
            </div>

            <p v-if="gitError" class="error mono">{{ gitError }}</p>
            <p v-else-if="gitLoading && !gitStatus" class="placeholder mono">{{ t('hint.loading') }}</p>
            <p
              v-else-if="gitStatus && !gitStatus.is_git_repo"
              class="placeholder mono"
            >
              {{ t('projects.git.notRepo') }}
            </p>
            <div v-if="gitStatus" class="git-body">
              <div class="git-line mono">
                <span class="git-branch">{{ gitStatus.branch || '—' }}</span>
                <span
                  class="flag"
                  :class="gitStatus.dirty ? 'flag--no' : 'flag--yes'"
                >
                  {{ gitStatus.dirty ? t('projects.git.dirty') : t('projects.git.clean') }}
                </span>
              </div>
              <ul class="commits" v-if="gitStatus.recent_commits.length">
                <li
                  v-for="c in gitStatus.recent_commits"
                  :key="c.hash"
                  class="commit mono"
                >
                  <span class="commit-hash">{{ c.hash }}</span>
                  <span class="commit-subject" :title="c.subject">{{ c.subject }}</span>
                  <span class="commit-meta">{{ c.author }} · {{ fmtTs(c.ts) }}</span>
                </li>
              </ul>
              <p v-else class="placeholder mono">{{ t('projects.git.none') }}</p>
            </div>
          </section>

          <!-- 子仓库列表（E32） -->
          <section class="block" aria-label="repos">
            <div class="block-head">
              <h2 class="block-title mono">{{ t('projects.repos.title') }}</h2>
            </div>
            <p v-if="reposError" class="error mono">{{ reposError }}</p>
            <p v-else-if="reposLoading" class="placeholder mono">{{ t('hint.loading') }}</p>
            <p v-else-if="repos.length === 0" class="placeholder mono">{{ t('projects.repos.none') }}</p>
            <ul v-else class="repos">
              <li v-for="r in repos" :key="r.rel_path" class="repo mono">
                <span class="repo-path">{{ r.rel_path }}</span>
                <span class="repo-branch">{{ r.branch || '—' }}</span>
                <span class="flag" :class="r.dirty ? 'flag--no' : 'flag--yes'">
                  {{ r.dirty ? t('projects.git.dirty') : t('projects.git.clean') }}
                </span>
              </li>
            </ul>
          </section>

          <!-- 关键文件（E32） -->
          <section class="block" aria-label="files">
            <div class="block-head">
              <h2 class="block-title mono">{{ t('projects.files.title') }}</h2>
            </div>
            <div class="file-tabs">
              <button
                v-for="name in KEY_FILE_CANDIDATES"
                :key="name"
                type="button"
                class="file-tab mono"
                :class="{ 'file-tab--active': name === activeFile }"
                @click="openFile(name)"
              >
                {{ name }}
              </button>
            </div>

            <div v-if="activeFile" class="file-view">
              <p v-if="fileLoading" class="placeholder mono">{{ t('hint.loading') }}</p>
              <p v-else-if="fileErr" class="error mono">{{ fileErr }}</p>
              <template v-else-if="fileBlob">
                <p v-if="fileTruncated" class="truncated mono">
                  {{ t('projects.files.truncated') }}
                </p>
                <FilePreview
                  :name="fileName"
                  :blob="fileBlob"
                  @download="downloadActiveFile"
                />
              </template>
            </div>
            <p v-else class="placeholder mono">{{ t('projects.files.hint') }}</p>
          </section>
        </div>
      </section>

      <!-- worker 自有项目（P5，gofer-d1kn）：只在 worker.yaml 里声明，server 配置里没有 -->
      <section
        v-if="ownProjects.length"
        class="worker-own"
        :aria-label="t('projects.workerOwn.title')"
        data-test="project-worker-own"
      >
        <div class="block-head">
          <h2 class="block-title mono">{{ t('projects.workerOwn.title') }}</h2>
        </div>
        <p class="field-hint mono">{{ t('projects.workerOwn.hint') }}</p>
        <ul class="own-list">
          <li v-for="row in ownProjects" :key="row.key" class="own-row" data-test="project-worker-own-row">
            <span class="own-key mono">{{ row.key }}</span>
            <span v-for="w in row.workers" :key="w" class="tag mono">{{ w }}</span>
          </li>
        </ul>
      </section>
    </div>
        </template>
      </template>
    </Tabs>
  </div>
</template>

<style scoped>
.projects {
  max-width: 1100px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--s3);
  margin-bottom: var(--s3);
}
.title {
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--fg);
}
.sub {
  margin: 2px 0 0;
  color: var(--fg-3);
  font-size: var(--fs-sm);
}
.mobile-project-picker {
  display: none;
}

.layout {
  display: grid;
  grid-template-columns: 260px 1fr;
  gap: 16px;
  align-items: start;
}

.list {
  list-style: none;
  margin: 0;
  padding: 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface);
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.list-item {
  display: block;
  width: 100%;
  text-align: left;
  background: transparent;
  color: var(--fg);
  border: 1px solid transparent;
  border-radius: var(--r-ctl);
  padding: 7px 9px;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.list-item:hover {
  background: var(--sunken);
}
.list-item--active {
  color: var(--accent);
  border-color: var(--line);
  background: var(--sunken);
}
.list-empty {
  color: var(--fg-3);
  font-size: 12px;
  padding: 6px 9px;
}

.detail {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface);
  padding: 16px 18px;
  min-height: 120px;
}
.placeholder {
  color: var(--fg-3);
  font-size: 13px;
  margin: 0;
}

.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--line);
}
.detail-key {
  font-size: 15px;
  color: var(--fg);
  font-weight: 600;
}
.board-link {
  background: transparent;
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 4px 10px;
  font-size: 12px;
}
.board-link:hover {
  border-color: var(--accent);
}

.kv {
  display: grid;
  grid-template-columns: 160px 1fr;
  gap: 8px 16px;
  margin: 0;
  font-size: 12px;
}
.kv dt {
  color: var(--fg-3);
  letter-spacing: 0.04em;
}
.kv dd {
  margin: 0;
  color: var(--fg);
  word-break: break-all;
}

.tag {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 1px 7px;
  margin: 0 6px 4px 0;
  color: var(--accent);
  font-size: 11px;
}

.flag {
  display: inline-block;
  border-radius: var(--r-ctl);
  padding: 1px 8px;
  font-size: 11px;
  letter-spacing: 0.04em;
}
.flag--yes {
  color: var(--done);
  border: 1px solid var(--done);
}
.flag--no {
  color: var(--fg-3);
  border: 1px solid var(--line);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-ctl);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}

/* P3 三块：git 状态卡 / 子仓库 / 关键文件。 */
.block {
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px solid var(--line);
}
.block-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}
.block-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--fg-3);
  margin: 0;
}
.mini-btn {
  background: transparent;
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 3px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--accent);
}
.mini-btn:disabled {
  color: var(--fg-3);
  cursor: default;
}

.form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.row {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
}
.row > .field,
.row > .pick {
  flex: 1 1 0;
  min-width: 220px;
}
.field {
  display: flex;
  flex-direction: column;
}
.field--check {
  justify-content: flex-end;
}
.label {
  font-size: 11px;
  letter-spacing: 0.08em;
  color: var(--fg-3);
  margin-bottom: 6px;
}
.control {
  width: 100%;
  background: var(--sunken);
  color: var(--fg);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 9px 10px;
  font-size: 13px;
  outline: none;
}
.control:focus {
  border-color: var(--accent);
}
.control:disabled {
  opacity: 0.65;
  cursor: default;
}
.pick {
  margin: 0;
  padding: 10px;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--sunken);
}
.check {
  display: flex;
  align-items: center;
  gap: 7px;
  color: var(--fg);
  font-size: 12px;
  margin: 6px 0;
}
.check input {
  accent-color: var(--accent);
}
.field-hint {
  color: var(--fg-3);
  font-size: 11px;
  margin: 6px 0 0;
}
.actions {
  display: flex;
  gap: 10px;
  align-items: center;
}
.submit,
.danger {
  border: none;
  border-radius: var(--r-ctl);
  padding: 9px 14px;
  font-size: 13px;
  font-weight: 600;
}
.submit {
  background: var(--accent);
  color: var(--accent-ink);
}
.submit--small {
  margin-left: auto;
  padding: 5px 11px;
  font-size: 12px;
}
.danger {
  background: transparent;
  color: var(--fail);
  border: 1px solid var(--fail);
}
.submit:disabled,
.danger:disabled {
  opacity: 0.55;
  cursor: default;
}
.warn {
  color: var(--run);
  font-size: 12px;
  border: 1px solid var(--run);
  border-radius: var(--r-ctl);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
}
.warn ul {
  margin: 6px 0 0;
  padding-left: 18px;
}
.notice {
  color: var(--done);
  font-size: 12px;
  margin: 0;
}

/* git 状态卡 */
.git-line {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  margin-bottom: 10px;
}
.git-branch {
  color: var(--accent);
  font-weight: 600;
}
.commits {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.commit {
  display: grid;
  grid-template-columns: 72px 1fr;
  gap: 2px 10px;
  font-size: 11px;
  padding: 5px 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
}
.commit-hash {
  color: var(--accent);
}
.commit-subject {
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.commit-meta {
  grid-column: 2;
  color: var(--fg-3);
}

/* 子仓库列表 */
.repos {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.repo {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12px;
  padding: 5px 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
}
.repo-path {
  color: var(--fg);
  flex: 1;
  word-break: break-all;
}
.repo-branch {
  color: var(--accent);
}

/* 关键文件 */
.file-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
}
.file-tab {
  background: transparent;
  color: var(--fg);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 3px 10px;
  font-size: 11px;
}
.file-tab:hover {
  border-color: var(--accent);
}
.file-tab--active {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--sunken);
}
.file-view {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 12px;
  background: var(--sunken);
}
.truncated {
  color: var(--fg-3);
  font-size: 11px;
  margin: 0 0 8px;
}

@media (max-width: 768px) {
  .layout {
    grid-template-columns: 1fr;
  }
  .kv {
    grid-template-columns: 120px 1fr;
    gap: 6px 10px;
  }
}

@media (max-width: 640px) {
  .head {
    align-items: center;
    justify-content: space-between;
  }
  .mobile-project-picker {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 10px;
    color: var(--fg-3);
    font-size: 11px;
  }
  .mobile-project-picker .control {
    flex: 1;
    min-width: 0;
    padding: 7px 8px;
  }
  .layout {
    grid-template-columns: 1fr;
    gap: 10px;
  }
  .layout > .list {
    display: none;
  }
  .detail {
    padding: 12px;
  }
  .detail-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 8px;
  }
  .kv {
    display: block;
  }
  .kv dt {
    margin-top: 10px;
    overflow-wrap: anywhere;
  }
  .kv dd {
    margin-top: 3px;
    overflow-wrap: anywhere;
  }
}
.builtin-badge {
  margin-left: 6px;
  padding: 0 5px;
  font-size: 10px;
  color: var(--fg-3);
  border: 1px solid currentColor;
  border-radius: 3px;
}
.builtin-hint {
  margin: 0 0 8px;
  font-size: 12px;
  color: var(--need-ink);
}

/* 项目概览（设计稿 §6.4-5）：活跃任务 / 进行中计划，各自可点。 */
.counts {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s2);
  margin: 0 0 var(--s3);
}
.count-link {
  display: inline-flex;
  align-items: center;
  min-height: 28px;
  padding: 0 var(--s2);
  background: var(--surface-2);
  color: var(--fg-2);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-xs);
}
.count-link:hover {
  color: var(--accent);
  border-color: var(--accent);
}

/* 「按项目 / 按执行机」视图切换（P5，gofer-d1kn）：按执行机视图一台执行机一张卡。 */
.fleet-head {
  display: flex;
  align-items: flex-start;
  gap: var(--s3);
  margin-bottom: var(--s3);
}
.fleet-hint {
  margin: 0;
  flex: 1;
  color: var(--fg-3);
  font-size: var(--fs-sm);
}
.groups {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: var(--s3);
}
.group {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  min-width: 0;
  padding: var(--s3);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  background: var(--surface);
}
/* 离线 worker：整张卡灰显；在线与否另有 StatusPill 文字，不只靠颜色。 */
.group--off {
  opacity: 0.6;
}
.group-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s2);
  padding-bottom: var(--s2);
  border-bottom: 1px solid var(--line);
}
.group-name {
  font-size: var(--fs-md);
  font-weight: 600;
  color: var(--fg);
  overflow-wrap: anywhere;
}
.group-kv {
  display: grid;
  grid-template-columns: 100px 1fr;
  gap: var(--s1) var(--s2);
  margin: 0;
  font-size: var(--fs-xs);
}
.group-kv dt {
  color: var(--fg-3);
}
.group-kv dd {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--s1);
  min-width: 0;
  margin: 0;
  color: var(--fg);
}
.group-kv .path {
  color: var(--fg);
  overflow-wrap: anywhere;
}
.group-kv .dim {
  color: var(--fg-3);
}
.proj-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  min-height: 26px;
  padding: 0 var(--s2);
  background: var(--surface-2);
  color: var(--fg-2);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
button.proj-chip:hover {
  color: var(--accent);
  border-color: var(--accent);
}
/* worker 自有：不是 server 项目，点不了（改要去那台 worker 的 worker.yaml）。 */
.proj-chip--own {
  color: var(--need-ink);
  border-style: dashed;
}
.group-error {
  margin: 0;
  color: var(--fg-3);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}

/* 按项目视图里的 worker 自有项目组：跨两列，手机上排到详情前面。 */
.worker-own {
  grid-column: 1 / -1;
  padding: var(--s3);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface);
}
.own-list {
  list-style: none;
  margin: var(--s2) 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--s1);
}
.own-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s2);
  font-size: var(--fs-xs);
}
.own-key {
  color: var(--fg);
  overflow-wrap: anywhere;
}

@media (max-width: 760px) {
  .groups {
    grid-template-columns: 1fr;
  }
  .group-kv {
    grid-template-columns: 1fr;
    gap: 2px;
  }
  .group-kv dt {
    margin-top: var(--s1);
  }
  .proj-chip {
    min-height: 36px;
  }
}
@media (max-width: 640px) {
  .worker-own {
    order: -1;
  }
}
</style>
