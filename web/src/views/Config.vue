<script setup lang="ts">
// Config：脱敏配置总览 + projects 写层。只展示后端 bool 化后的 secret 状态，
// 不接收、不缓存、不渲染任何 secret 值。
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import {
  ApiError,
  createProject,
  deleteProject,
  getConfig,
  updateProject,
} from '../api/client'
import type {
  ConfigView,
  ProjectDetail,
  ProjectWriteReq,
  ProjectWriteResp,
} from '../api/types'

const POLL_MS = 5000

const config = ref<ConfigView | null>(null)
const loading = ref(false)
const loaded = ref(false)
const loadError = ref('')
const saving = ref(false)
const deleting = ref(false)
const formError = ref('')
const notice = ref('')
const warnings = ref<string[]>([])
const selectedKey = ref('')
const mode = ref<'none' | 'create' | 'edit'>('none')

let pollTimer: number | null = null

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

const projects = computed(() => config.value?.projects ?? [])
const agents = computed(() => config.value?.agents ?? [])
const runners = computed(() => config.value?.runners ?? [])
const roles = computed(() => config.value?.roles ?? [])
const callers = computed(() => config.value?.server.callers ?? [])
const workers = computed(() => config.value?.server.workers ?? [])
const runnerOptions = computed(() => [
  'local',
  ...runners.value.map((r) => r.key).filter((key) => key !== 'local'),
])
const selectedProject = computed(() =>
  projects.value.find((p) => p.key === selectedKey.value),
)
const isEditing = computed(() => mode.value === 'edit')
const canSubmit = computed(() => !saving.value && form.key.trim() !== '' && form.host_path.trim() !== '')

async function loadConfig(silent = false): Promise<void> {
  if (!silent) {
    loading.value = true
  }
  loadError.value = ''
  try {
    config.value = await getConfig()
    loaded.value = true
    if (mode.value === 'none' && projects.value.length > 0) {
      selectProject(projects.value[0])
    }
    if (mode.value === 'edit' && selectedKey.value && !selectedProject.value) {
      resetForm()
    }
  } catch (e) {
    loadError.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}

function startPolling(): void {
  stopPolling()
  pollTimer = window.setInterval(() => {
    if (!saving.value && !deleting.value && !document.hidden) {
      void loadConfig(true)
    }
  }, POLL_MS)
}

function stopPolling(): void {
  if (pollTimer != null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

function onVisibility(): void {
  if (!document.hidden) {
    void loadConfig(true)
  }
}

function selectProject(p: ProjectDetail): void {
  selectedKey.value = p.key
  mode.value = 'edit'
  formError.value = ''
  notice.value = ''
  warnings.value = []
  fillForm(p)
}

function startCreate(): void {
  selectedKey.value = ''
  mode.value = 'create'
  formError.value = ''
  notice.value = ''
  warnings.value = []
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
  selectedKey.value = ''
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
        : await updateProject(selectedKey.value, req)
    applyWriteSuccess(resp, mode.value === 'create' ? '项目已创建' : '项目已保存')
  } catch (e) {
    formError.value = classifyWriteError(e)
  } finally {
    saving.value = false
  }
}

async function removeProject(): Promise<void> {
  if (!selectedKey.value || !window.confirm(`确认删除项目 ${selectedKey.value}？`)) {
    return
  }
  formError.value = ''
  notice.value = ''
  warnings.value = []
  deleting.value = true
  try {
    await deleteProject(selectedKey.value)
    notice.value = '项目已删除'
    resetForm()
    await loadConfig(true)
  } catch (e) {
    formError.value = classifyWriteError(e)
  } finally {
    deleting.value = false
  }
}

async function applyWriteSuccess(resp: ProjectWriteResp, message: string): Promise<void> {
  notice.value = message
  warnings.value = resp.warnings ?? []
  selectedKey.value = resp.key
  mode.value = 'edit'
  fillForm(resp)
  await loadConfig(true)
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
  return errorMessage(e)
}

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function yesNo(v: boolean): string {
  return v ? '是' : '否'
}

function setText(v: boolean): string {
  return v ? '已配置' : '未配置'
}

function joinOrDash(items?: string[]): string {
  return items && items.length > 0 ? items.join(', ') : '-'
}

onMounted(() => {
  void loadConfig()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="config-page">
    <div class="head">
      <span class="eyebrow mono">CONFIG</span>
      <h1 class="title mono">配置</h1>
      <span class="poll mono" :class="{ 'poll--on': loading }">●</span>
    </div>

    <p v-if="loadError" class="error mono">{{ loadError }}</p>
    <p v-else-if="loading && !loaded" class="placeholder mono">加载配置中...</p>

    <template v-if="config">
      <section class="section" aria-label="脱敏配置总览">
        <div class="section-head">
          <h2 class="section-title mono">脱敏配置总览</h2>
          <button class="mini-btn mono" type="button" :disabled="loading" @click="loadConfig()">
            {{ loading ? '刷新中...' : '刷新' }}
          </button>
        </div>

        <div class="overview-grid">
          <article class="panel">
            <h3 class="panel-title mono">SERVER</h3>
            <dl class="kv mono">
              <dt>addr</dt><dd>{{ config.server.addr || '-' }}</dd>
              <dt>path_view</dt><dd>{{ config.server.path_view || '-' }}</dd>
              <dt>web_enabled</dt><dd><span class="flag" :class="config.server.web_enabled ? 'flag--yes' : 'flag--no'">{{ yesNo(config.server.web_enabled) }}</span></dd>
              <dt>server token</dt><dd><span class="flag" :class="config.server.token_set ? 'flag--yes' : 'flag--no'">{{ setText(config.server.token_set) }}</span></dd>
              <dt>allow_empty_token</dt><dd><span class="flag" :class="config.server.allow_empty_token ? 'flag--warn' : 'flag--no'">{{ yesNo(config.server.allow_empty_token) }}</span></dd>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">GOVERNANCE / METRICS</h3>
            <dl class="kv mono">
              <dt>require_answer</dt><dd><span class="flag" :class="config.server.governance.require_answer_capability ? 'flag--yes' : 'flag--no'">{{ yesNo(config.server.governance.require_answer_capability) }}</span></dd>
              <dt>require_admin</dt><dd><span class="flag" :class="config.server.governance.require_admin_capability ? 'flag--yes' : 'flag--no'">{{ yesNo(config.server.governance.require_admin_capability) }}</span></dd>
              <dt>caller max</dt><dd>{{ config.server.governance.default_caller_max_concurrent || '-' }}</dd>
              <dt>rate</dt><dd>{{ config.server.governance.default_rate_limit || '-' }} / {{ config.server.governance.default_rate_burst || '-' }}</dd>
              <dt>metrics</dt><dd><span class="flag" :class="config.server.metrics.enabled ? 'flag--yes' : 'flag--no'">{{ config.server.metrics.enabled ? 'enabled' : 'disabled' }}</span></dd>
              <dt>metrics token</dt><dd><span class="flag" :class="config.server.metrics.token_set ? 'flag--yes' : 'flag--no'">{{ setText(config.server.metrics.token_set) }}</span></dd>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">STORAGE</h3>
            <dl class="kv mono">
              <dt>root</dt><dd>{{ config.storage.root || '-' }}</dd>
              <dt>db_path</dt><dd>{{ config.storage.db_path || '-' }}</dd>
              <dt>exchange</dt><dd>{{ config.storage.default_exchange_subdir || '-' }}</dd>
              <dt>result</dt><dd>{{ config.storage.default_result_subdir || '-' }}</dd>
              <dt>retention</dt><dd>{{ config.storage.retention.max_age_days || '-' }}d / {{ config.storage.retention.max_count || '-' }}</dd>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">SUPERVISOR / PRESENCE</h3>
            <dl class="kv mono">
              <dt>supervisor</dt><dd><span class="flag" :class="config.supervisor?.enabled ? 'flag--yes' : 'flag--no'">{{ config.supervisor?.enabled ? 'enabled' : 'disabled' }}</span></dd>
              <dt>desired</dt><dd>{{ config.supervisor?.desired_supervisors ?? '-' }}</dd>
              <dt>auto_answer</dt><dd>{{ config.supervisor ? yesNo(config.supervisor.auto_answer) : '-' }}</dd>
              <dt>presence ttl</dt><dd>{{ config.presence.ttl_sec }}s</dd>
              <dt>schedule sweep</dt><dd>{{ config.schedule.sweep_interval_sec }}s</dd>
              <dt>runner probe</dt><dd>{{ config.server.runner_probe.interval_seconds }}s / {{ config.server.runner_probe.timeout_seconds }}s</dd>
            </dl>
          </article>
        </div>

        <div class="block">
          <h3 class="block-title mono">CALLERS</h3>
          <div class="table-wrap">
            <table class="table mono">
              <thead>
                <tr>
                  <th>id</th>
                  <th>token</th>
                  <th>can_answer</th>
                  <th>can_admin</th>
                  <th>quota</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in callers" :key="c.id">
                  <td>{{ c.id }}</td>
                  <td><span class="flag" :class="c.token_set ? 'flag--yes' : 'flag--no'">{{ setText(c.token_set) }}</span></td>
                  <td><span class="flag" :class="c.can_answer ? 'flag--yes' : 'flag--no'">{{ yesNo(c.can_answer) }}</span></td>
                  <td><span class="flag" :class="c.can_admin ? 'flag--yes' : 'flag--no'">{{ yesNo(c.can_admin) }}</span></td>
                  <td>{{ c.max_concurrent_jobs || '-' }} / {{ c.rate_limit || '-' }} / {{ c.rate_burst || '-' }}</td>
                </tr>
                <tr v-if="callers.length === 0"><td colspan="5">无 caller</td></tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="cards-3">
          <article class="panel">
            <h3 class="panel-title mono">AGENTS</h3>
            <ul class="mini-list">
              <li v-for="a in agents" :key="a.key" class="mini-row mono">
                <span class="item-main">{{ a.key }} · {{ a.type }}</span>
                <span v-if="a.command" class="muted">{{ a.command }}</span>
                <span v-for="k in a.env_keys" :key="`${a.key}:${k}`" class="tag">{{ k }}</span>
              </li>
              <li v-if="agents.length === 0" class="empty mono">无 agent</li>
            </ul>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">RUNNERS</h3>
            <ul class="mini-list">
              <li v-for="r in runners" :key="r.key" class="mini-row mono">
                <span class="item-main">{{ r.key }} · {{ r.type }}</span>
                <span v-if="r.base_url" class="muted">{{ r.base_url }}</span>
                <span class="flag" :class="r.token_set ? 'flag--yes' : 'flag--no'">{{ setText(r.token_set) }}</span>
              </li>
              <li v-if="runners.length === 0" class="empty mono">无 runner</li>
            </ul>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">ROLES / WORKERS</h3>
            <ul class="mini-list">
              <li v-for="r in roles" :key="r.key" class="mini-row mono">
                <span class="item-main">{{ r.key }} · {{ r.agent }}</span>
                <span v-for="k in r.env_keys" :key="`${r.key}:${k}`" class="tag">{{ k }}</span>
              </li>
              <li v-for="w in workers" :key="`worker:${w.id}`" class="mini-row mono">
                <span class="item-main">worker {{ w.id }}</span>
                <span class="flag" :class="w.token_set ? 'flag--yes' : 'flag--no'">{{ setText(w.token_set) }}</span>
                <span class="muted">{{ joinOrDash(w.labels) }}</span>
              </li>
              <li v-if="roles.length === 0 && workers.length === 0" class="empty mono">无 role / worker</li>
            </ul>
          </article>
        </div>
      </section>

      <section class="section" aria-label="项目编辑">
        <div class="section-head">
          <h2 class="section-title mono">项目编辑</h2>
          <button class="submit submit--small" type="button" @click="startCreate">新增项目</button>
        </div>

        <div class="editor">
          <ul class="project-list" aria-label="项目列表">
            <li v-for="p in projects" :key="p.key">
              <button
                type="button"
                class="list-item mono"
                :class="{ 'list-item--active': p.key === selectedKey }"
                :title="p.host_path"
                @click="selectProject(p)"
              >
                {{ p.key }}
              </button>
            </li>
            <li v-if="projects.length === 0" class="empty mono">暂无项目</li>
          </ul>

          <form v-if="mode !== 'none'" class="form" @submit.prevent="saveProject">
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

          <p v-else class="placeholder mono">选择项目或点击新增项目</p>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.config-page {
  max-width: 1180px;
  margin: 0 auto;
}
.head,
.section-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
}
.eyebrow {
  font-size: 10px;
  letter-spacing: 0.18em;
  color: var(--queue);
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.poll {
  margin-left: auto;
  color: var(--line);
  font-size: 10px;
}
.poll--on {
  color: var(--phosphor);
}
.section {
  margin-bottom: 20px;
}
.section-title,
.panel-title,
.block-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin: 0;
}
.section-title {
  color: var(--paper);
  font-size: 14px;
}
.mini-btn {
  margin-left: auto;
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.overview-grid,
.cards-3 {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.cards-3 {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.panel,
.form,
.project-list {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 14px;
}
.kv {
  display: grid;
  grid-template-columns: 148px 1fr;
  gap: 7px 12px;
  margin: 10px 0 0;
  font-size: 12px;
}
.kv dt {
  color: var(--queue);
}
.kv dd {
  color: var(--paper);
  margin: 0;
  word-break: break-all;
}
.flag,
.tag {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  font-size: 11px;
}
.flag--yes {
  color: var(--done);
  border-color: var(--done);
}
.flag--no {
  color: var(--queue);
}
.flag--warn {
  color: var(--run);
  border-color: var(--run);
}
.tag {
  color: var(--phosphor);
  margin: 4px 4px 0 0;
}
.block {
  margin-top: 12px;
  padding: 14px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
}
.table-wrap {
  overflow-x: auto;
}
.table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 10px;
  font-size: 12px;
}
.table th,
.table td {
  border-bottom: 1px solid var(--line);
  padding: 7px 8px;
  text-align: left;
}
.table th {
  color: var(--queue);
  font-weight: 400;
}
.table td {
  color: var(--paper);
}
.mini-list,
.project-list {
  list-style: none;
  margin: 0;
}
.mini-list {
  padding: 0;
}
.project-list {
  padding: 8px;
}
.mini-row {
  border-top: 1px solid var(--line);
  padding: 8px 0;
  font-size: 12px;
}
.mini-row:first-child {
  border-top: none;
}
.item-main {
  color: var(--paper);
  display: block;
  margin-bottom: 3px;
}
.muted,
.empty,
.placeholder {
  color: var(--queue);
}
.empty,
.placeholder {
  font-size: 12px;
}
.editor {
  display: grid;
  grid-template-columns: 260px 1fr;
  gap: 14px;
  align-items: start;
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
.list-item:hover,
.list-item--active {
  background: var(--ink);
}
.list-item--active {
  color: var(--phosphor);
  border-color: var(--line);
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
.error,
.warn {
  font-size: 12px;
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
}
.error {
  color: var(--fail);
  border: 1px solid var(--fail);
}
.warn {
  color: var(--run);
  border: 1px solid var(--run);
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
@media (max-width: 900px) {
  .overview-grid,
  .cards-3,
  .editor {
    grid-template-columns: 1fr;
  }
}
</style>
