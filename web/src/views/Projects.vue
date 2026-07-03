<script setup lang="ts">
// Projects：左列项目 keys（listProjects），选中拉详情（getProject），右列展示配置与项目写层。
// P3 增强（E20/E32）：右列再加 git 状态卡 + 子仓库列表 + 关键文件查看（FilePreview）。
// 刷新策略（D7）：进入项目详情时取 git/repos，git 卡提供手动刷新按钮，不轮询。
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  ApiError,
  createProject,
  deleteProject,
  getConfig,
  getProject,
  getProjectFile,
  getProjectGit,
  listProjects,
  listRepos,
  updateProject,
} from '../api/client'
import type {
  ConfigView,
  GitStatus,
  ProjectDetail,
  ProjectWriteReq,
  ProjectWriteResp,
  RepoInfo,
} from '../api/types'
import FilePreview from '../components/FilePreview.vue'

const router = useRouter()

const keys = ref<string[]>([])
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
const mode = ref<'none' | 'create' | 'edit'>('none')

interface ProjectForm {
  key: string
  host_path: string
  container_path: string
  default_agent: string
  allowed_agents: string[]
  allowed_runners: string[]
  allow_exec: boolean
  max_concurrent_jobs: string
}

const form = reactive<ProjectForm>({
  key: '',
  host_path: '',
  container_path: '',
  default_agent: '',
  allowed_agents: [],
  allowed_runners: [],
  allow_exec: false,
  max_concurrent_jobs: '',
})

const agents = computed(() => config.value?.agents ?? [])
const runners = computed(() => config.value?.runners ?? [])
const runnerOptions = computed(() => [
  'local',
  ...runners.value.map((r) => r.key).filter((key) => key !== 'local'),
])
const isEditing = computed(() => mode.value === 'edit')
const canSubmit = computed(() => !saving.value && form.key.trim() !== '' && form.host_path.trim() !== '')

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
  // 切项目：重置 git/repos/关键文件预览状态。
  resetFile()
  try {
    detail.value = await getProject(key)
    fillForm(detail.value)
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingDetail.value = false
  }
  // 详情拉到后并行取 git + repos（失败各自降级，不影响配置展示）。
  void loadGit(key)
  void loadRepos(key)
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
  resetFile()
  Object.assign(form, {
    key: '',
    host_path: '',
    container_path: '',
    default_agent: agents.value[0]?.key ?? '',
    allowed_agents: [],
    allowed_runners: ['local'],
    allow_exec: false,
    max_concurrent_jobs: '',
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
    max_concurrent_jobs: p.max_concurrent_jobs != null ? String(p.max_concurrent_jobs) : '',
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
    max_concurrent_jobs: '',
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

function buildReq(): ProjectWriteReq {
  const req: ProjectWriteReq = {
    key: form.key.trim(),
    host_path: form.host_path.trim(),
    allow_exec: form.allow_exec,
  }
  const containerPath = form.container_path.trim()
  if (containerPath) {
    req.container_path = containerPath
  }
  if (form.default_agent) {
    req.default_agent = form.default_agent
  }
  if (form.allowed_agents.length > 0) {
    req.allowed_agents = [...form.allowed_agents]
  }
  if (form.allowed_runners.length > 0) {
    req.allowed_runners = [...form.allowed_runners]
  }
  const max = Number.parseInt(form.max_concurrent_jobs, 10)
  if (Number.isFinite(max) && max > 0) {
    req.max_concurrent_jobs = max
  }
  return req
}

async function saveProject(): Promise<void> {
  formError.value = ''
  notice.value = ''
  warnings.value = []
  if (!canSubmit.value) {
    formError.value = '请填写 key 与 host_path'
    return
  }
  saving.value = true
  try {
    const req = buildReq()
    const resp =
      mode.value === 'create'
        ? await createProject(req)
        : await updateProject(selected.value, req)
    await applyWriteSuccess(resp, mode.value === 'create' ? '项目已创建' : '项目已保存')
  } catch (e) {
    formError.value = classifyWriteError(e)
  } finally {
    saving.value = false
  }
}

async function removeProject(): Promise<void> {
  if (!selected.value || !window.confirm(`确认删除项目 ${selected.value}？`)) {
    return
  }
  formError.value = ''
  notice.value = ''
  warnings.value = []
  deleting.value = true
  try {
    await deleteProject(selected.value)
    notice.value = '项目已删除'
    resetForm()
    await loadList('', true)
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
}

function classifyWriteError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) {
      return '无 can_admin 权限，当前 caller 不允许编辑配置。'
    }
    if (e.status === 409) {
      return '项目已存在，请换一个 key 或选择现有项目编辑。'
    }
    if (e.status === 400) {
      return `项目配置校验失败：${e.detail || e.message}`
    }
    if (e.status === 404) {
      return `项目不存在：${e.detail || e.message}`
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
  void router.push({ path: '/board', query: { project: key } })
}

onMounted(() => {
  void loadList()
  void loadConfigOptions()
})
</script>

<template>
  <div class="projects">
    <div class="head">
      <h1 class="title mono">PROJECTS</h1>
      <button class="submit submit--small" type="button" @click="startCreate">新增项目</button>
    </div>
    <p v-if="listError" class="error mono">{{ listError }}</p>
    <p v-if="configError" class="error mono">{{ configError }}</p>

    <div class="layout">
      <!-- 列表 -->
      <ul class="list" aria-label="项目列表">
        <li v-for="key in keys" :key="key">
          <button
            type="button"
            class="list-item mono"
            :class="{ 'list-item--active': key === selected }"
            :title="key"
            @click="selectKey(key)"
          >
            {{ key }}
          </button>
        </li>
        <li v-if="keys.length === 0 && !loadingList && !listError" class="list-empty mono">
          暂无项目
        </li>
        <li v-if="loadingList" class="list-empty mono">加载中…</li>
      </ul>

      <!-- 详情 -->
      <section class="detail" aria-label="项目详情">
        <p v-if="detailError" class="error mono">{{ detailError }}</p>
        <p v-else-if="loadingDetail" class="placeholder mono">加载中…</p>
        <p v-else-if="!detail && mode !== 'create'" class="placeholder mono">选择左侧项目查看配置，或点击新增项目</p>

        <div v-if="detail" class="detail-body">
          <div class="detail-head">
            <span class="detail-key mono">{{ detail.key }}</span>
            <button class="board-link mono" type="button" @click="viewOnBoard(detail.key)">
              在看板查看 &#9656;
            </button>
          </div>

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
                <span v-for="r in detail.allowed_runners" :key="r" class="tag">{{ r }}</span>
              </template>
              <span v-else>—</span>
            </dd>

            <dt class="mono">allow_exec</dt>
            <dd class="mono">
              <span class="flag" :class="detail.allow_exec ? 'flag--yes' : 'flag--no'">
                {{ detail.allow_exec ? '是' : '否' }}
              </span>
            </dd>

            <dt class="mono">max_concurrent_jobs</dt>
            <dd class="mono">{{ detail.max_concurrent_jobs ?? '—' }}</dd>
          </dl>
        </div>

        <section v-if="mode !== 'none'" class="block edit-block" aria-label="项目编辑">
          <div class="block-head">
            <h2 class="block-title mono">项目编辑</h2>
          </div>

          <form class="form" @submit.prevent="saveProject">
            <div class="row">
              <div class="field">
                <label class="label mono" for="cfg-key">KEY</label>
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
                <label class="label mono" for="cfg-default-agent">DEFAULT_AGENT</label>
                <select id="cfg-default-agent" v-model="form.default_agent" class="control mono">
                  <option value="">-</option>
                  <option v-for="a in agents" :key="a.key" :value="a.key">{{ a.key }}</option>
                </select>
              </div>
            </div>

            <div class="field">
              <label class="label mono" for="cfg-host">HOST_PATH</label>
              <input
                id="cfg-host"
                v-model="form.host_path"
                class="control mono"
                autocomplete="off"
                spellcheck="false"
              />
            </div>

            <div class="field">
              <label class="label mono" for="cfg-container">CONTAINER_PATH</label>
              <input
                id="cfg-container"
                v-model="form.container_path"
                class="control mono"
                autocomplete="off"
                spellcheck="false"
                placeholder="可选"
              />
            </div>

            <div class="row">
              <fieldset class="pick">
                <legend class="label mono">ALLOWED_AGENTS</legend>
                <label v-for="a in agents" :key="a.key" class="check mono">
                  <input
                    type="checkbox"
                    :checked="form.allowed_agents.includes(a.key)"
                    @change="toggleAgent(a.key)"
                  />
                  <span>{{ a.key }}</span>
                </label>
                <p v-if="agents.length === 0" class="field-hint mono">无 agent 选项</p>
              </fieldset>

              <fieldset class="pick">
                <legend class="label mono">ALLOWED_RUNNERS</legend>
                <label v-for="r in runnerOptions" :key="r" class="check mono">
                  <input
                    type="checkbox"
                    :checked="form.allowed_runners.includes(r)"
                    @change="toggleRunner(r)"
                  />
                  <span>{{ r }}</span>
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
              <div class="field">
                <label class="label mono" for="cfg-max">MAX_CONCURRENT_JOBS</label>
                <input
                  id="cfg-max"
                  v-model="form.max_concurrent_jobs"
                  class="control mono"
                  type="number"
                  min="1"
                  placeholder="不限制"
                />
              </div>
            </div>

            <p v-if="formError" class="error mono">{{ formError }}</p>
            <p v-if="notice" class="notice mono">{{ notice }}</p>
            <div v-if="warnings.length" class="warn mono">
              <strong>保存成功，但有告警：</strong>
              <ul>
                <li v-for="w in warnings" :key="w">{{ w }}</li>
              </ul>
            </div>

            <div class="actions">
              <button class="submit" type="submit" :disabled="!canSubmit">
                {{ saving ? '保存中...' : mode === 'create' ? '创建项目' : '保存项目' }}
              </button>
              <button
                v-if="isEditing"
                class="danger"
                type="button"
                :disabled="deleting || saving"
                @click="removeProject"
              >
                {{ deleting ? '删除中...' : '删除项目' }}
              </button>
            </div>
          </form>
        </section>

        <div v-if="detail" class="detail-body">
          <!-- git 状态卡（E20） -->
          <section class="block" aria-label="git 状态">
            <div class="block-head">
              <h2 class="block-title mono">GIT 状态</h2>
              <button
                class="mini-btn mono"
                type="button"
                :disabled="gitLoading"
                @click="refreshGit"
              >
                {{ gitLoading ? '刷新中…' : '刷新' }}
              </button>
            </div>

            <p v-if="gitError" class="error mono">{{ gitError }}</p>
            <p v-else-if="gitLoading && !gitStatus" class="placeholder mono">加载中…</p>
            <p
              v-else-if="gitStatus && !gitStatus.is_git_repo"
              class="placeholder mono"
            >
              非 git 仓 / 非本地可达
            </p>
            <div v-else-if="gitStatus" class="git-body">
              <div class="git-line mono">
                <span class="git-branch">{{ gitStatus.branch || '—' }}</span>
                <span
                  class="flag"
                  :class="gitStatus.dirty ? 'flag--no' : 'flag--yes'"
                >
                  {{ gitStatus.dirty ? 'dirty' : 'clean' }}
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
              <p v-else class="placeholder mono">无提交记录</p>
            </div>
          </section>

          <!-- 子仓库列表（E32） -->
          <section class="block" aria-label="子仓库">
            <div class="block-head">
              <h2 class="block-title mono">子仓库</h2>
            </div>
            <p v-if="reposError" class="error mono">{{ reposError }}</p>
            <p v-else-if="reposLoading" class="placeholder mono">加载中…</p>
            <p v-else-if="repos.length === 0" class="placeholder mono">未发现 git 仓</p>
            <ul v-else class="repos">
              <li v-for="r in repos" :key="r.rel_path" class="repo mono">
                <span class="repo-path">{{ r.rel_path }}</span>
                <span class="repo-branch">{{ r.branch || '—' }}</span>
                <span class="flag" :class="r.dirty ? 'flag--no' : 'flag--yes'">
                  {{ r.dirty ? 'dirty' : 'clean' }}
                </span>
              </li>
            </ul>
          </section>

          <!-- 关键文件（E32） -->
          <section class="block" aria-label="关键文件">
            <div class="block-head">
              <h2 class="block-title mono">关键文件</h2>
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
              <p v-if="fileLoading" class="placeholder mono">加载中…</p>
              <p v-else-if="fileErr" class="error mono">{{ fileErr }}</p>
              <template v-else-if="fileBlob">
                <p v-if="fileTruncated" class="truncated mono">
                  文件超过 256KB，已截断显示
                </p>
                <FilePreview
                  :name="fileName"
                  :blob="fileBlob"
                  @download="downloadActiveFile"
                />
              </template>
            </div>
            <p v-else class="placeholder mono">点击上方文件查看内容</p>
          </section>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.projects {
  max-width: 1100px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
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
  border-radius: var(--radius);
  background: var(--panel);
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.list-item {
  display: block;
  width: 100%;
  text-align: left;
  background: transparent;
  color: var(--paper);
  border: 1px solid transparent;
  border-radius: var(--radius);
  padding: 7px 9px;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.list-item:hover {
  background: var(--ink);
}
.list-item--active {
  color: var(--phosphor);
  border-color: var(--line);
  background: var(--ink);
}
.list-empty {
  color: var(--queue);
  font-size: 12px;
  padding: 6px 9px;
}

.detail {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 16px 18px;
  min-height: 120px;
}
.placeholder {
  color: var(--queue);
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
  color: var(--paper);
  font-weight: 600;
}
.board-link {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.board-link:hover {
  border-color: var(--phosphor);
}

.kv {
  display: grid;
  grid-template-columns: 160px 1fr;
  gap: 8px 16px;
  margin: 0;
  font-size: 12px;
}
.kv dt {
  color: var(--queue);
  letter-spacing: 0.04em;
}
.kv dd {
  margin: 0;
  color: var(--paper);
  word-break: break-all;
}

.tag {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  margin: 0 6px 4px 0;
  color: var(--phosphor);
  font-size: 11px;
}

.flag {
  display: inline-block;
  border-radius: var(--radius);
  padding: 1px 8px;
  font-size: 11px;
  letter-spacing: 0.04em;
}
.flag--yes {
  color: var(--done);
  border: 1px solid var(--done);
}
.flag--no {
  color: var(--queue);
  border: 1px solid var(--line);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
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
  color: var(--queue);
  margin: 0;
}
.mini-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.mini-btn:disabled {
  color: var(--queue);
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
  color: var(--queue);
  margin-bottom: 6px;
}
.control {
  width: 100%;
  background: var(--ink);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 9px 10px;
  font-size: 13px;
  outline: none;
}
.control:focus {
  border-color: var(--phosphor);
}
.control:disabled {
  opacity: 0.65;
  cursor: default;
}
.pick {
  margin: 0;
  padding: 10px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--ink);
}
.check {
  display: flex;
  align-items: center;
  gap: 7px;
  color: var(--paper);
  font-size: 12px;
  margin: 6px 0;
}
.check input {
  accent-color: var(--phosphor);
}
.field-hint {
  color: var(--queue);
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
  border-radius: var(--radius);
  padding: 9px 14px;
  font-size: 13px;
  font-weight: 600;
}
.submit {
  background: var(--phosphor);
  color: var(--ink);
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
  border-radius: var(--radius);
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
  color: var(--phosphor);
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
  border-radius: var(--radius);
}
.commit-hash {
  color: var(--phosphor);
}
.commit-subject {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.commit-meta {
  grid-column: 2;
  color: var(--queue);
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
  border-radius: var(--radius);
}
.repo-path {
  color: var(--paper);
  flex: 1;
  word-break: break-all;
}
.repo-branch {
  color: var(--phosphor);
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
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
}
.file-tab:hover {
  border-color: var(--phosphor);
}
.file-tab--active {
  color: var(--phosphor);
  border-color: var(--phosphor);
  background: var(--ink);
}
.file-view {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px;
  background: var(--ink);
}
.truncated {
  color: var(--queue);
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
</style>
