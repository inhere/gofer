<script setup lang="ts">
// Config：脱敏配置总览 + agents/server 编辑（WEB-04③ V1.1）。
// 只展示后端 bool 化后的 secret 状态，不接收、不缓存、不渲染任何 secret 值；
// 写请求体里也永远没有 secret 字段（服务端会以 `secret value not accepted` 拒绝字面值）。
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import {
  ApiError,
  deleteConfigAgent,
  getConfig,
  putConfigAgent,
  putConfigServer,
  reloadConfig,
  validateConfig,
} from '../api/client'
import type { ConfigAgentView, ConfigView, ConfigValidateResult, FieldPolicy } from '../api/types'

const POLL_MS = 5000

const config = ref<ConfigView | null>(null)
const loading = ref(false)
const loaded = ref(false)
const loadError = ref('')
const notice = ref('')
const writeError = ref('')

let pollTimer: number | null = null

const agents = computed(() => config.value?.agents ?? [])
const runners = computed(() => config.value?.runners ?? [])
const roles = computed(() => config.value?.roles ?? [])
const callers = computed(() => config.value?.server.callers ?? [])
const workers = computed(() => config.value?.server.workers ?? [])

async function loadConfig(silent = false): Promise<void> {
  if (!silent) {
    loading.value = true
  }
  loadError.value = ''
  try {
    config.value = await getConfig()
    loaded.value = true
  } catch (e) {
    loadError.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}

function startPolling(): void {
  stopPolling()
  pollTimer = window.setInterval(() => {
    // 编辑弹窗打开时暂停自动刷新：刷新会把表单底下的"当前值"换掉（还没保存就漂移）。
    if (!document.hidden && editor.value === null) {
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

// ---------------------------------------------------------------- 编辑弹窗

type EditorKind = 'agent-create' | 'agent-edit' | 'server'

interface AgentForm {
  key: string
  type: string
  command: string
  argsText: string
  interactiveMode: boolean
  interactiveArgsText: string
  sessionCapture: string
  sessionInjectText: string
  sessionResumeText: string
  maxConcurrent: string
  stallTimeoutSec: string
  fallbackAgentsText: string
  outputFormat: string
  retryMaxAttempts: string
  retryBackoffText: string
  acpPermissionPolicy: string
}

interface ServerForm {
  maxJobTimeoutSec: string
  stallTimeoutSec: string
  autoResumeMax: string
  probeIntervalSec: string
  probeTimeoutSec: string
  retryMaxAttempts: string
  retryBackoffText: string
}

const editor = ref<EditorKind | null>(null)
const editing = ref<ConfigAgentView | null>(null)
const saving = ref(false)
const formError = ref('')
const preview = ref('')
const previewRestart = ref<string[]>([])
const badFields = ref<Set<string>>(new Set())
const validating = ref(false)

const agentForm = reactive<AgentForm>({
  key: '',
  type: 'cli-agent',
  command: '',
  argsText: '',
  interactiveMode: false,
  interactiveArgsText: '',
  sessionCapture: '',
  sessionInjectText: '',
  sessionResumeText: '',
  maxConcurrent: '',
  stallTimeoutSec: '',
  fallbackAgentsText: '',
  outputFormat: '',
  retryMaxAttempts: '',
  retryBackoffText: '',
  acpPermissionPolicy: '',
})

const serverForm = reactive<ServerForm>({
  maxJobTimeoutSec: '',
  stallTimeoutSec: '',
  autoResumeMax: '',
  probeIntervalSec: '',
  probeTimeoutSec: '',
  retryMaxAttempts: '',
  retryBackoffText: '',
})

const agentPolicy = computed<Record<string, FieldPolicy>>(() => config.value?.agent_policy ?? {})
const serverPolicy = computed<Record<string, FieldPolicy>>(() => config.value?.server_policy ?? {})
// restartOnly：server 块里"改了要重启"的字段（策略表给的答案，不是控制台自己列的）。
const restartOnly = computed(() =>
  Object.entries(serverPolicy.value)
    .filter(([, fp]) => fp.restart_required)
    .map(([name]) => name),
)
const isEdit = computed(() => editor.value !== null)
const isServer = computed(() => editor.value === 'server')
const agentTitle = computed(() => (editor.value === 'agent-create' ? '新增 agent' : `编辑 agent · ${agentForm.key}`))

// lines 把多行文本折成 argv/列表；空文本 = 空列表（不是 null）。
function lines(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '')
}

function linesText(items?: string[] | null): string {
  return items && items.length > 0 ? items.join('\n') : ''
}

function optionalInt(text: string): number | null {
  const v = text.trim()
  if (v === '') {
    return null
  }
  const n = Number(v)
  return Number.isFinite(n) ? Math.trunc(n) : null
}

// fieldBad：服务端返回的字段路径（agents.<key>.<field>）→ 高亮对应输入框。
// 只比较最后一段（acp.permission_policy 这类两级路径取末段即可）。
function fieldBad(name: string): boolean {
  return badFields.value.has(name)
}

function markBadFields(paths?: string[]): void {
  const out = new Set<string>()
  for (const p of paths ?? []) {
    const parts = p.split('.')
    out.add(parts[parts.length - 1])
  }
  badFields.value = out
}

function openAgentEditor(a: ConfigAgentView | null): void {
  writeError.value = ''
  formError.value = ''
  notice.value = ''
  badFields.value = new Set()
  editing.value = a
  editor.value = a === null ? 'agent-create' : 'agent-edit'
  const interactiveArgs = a?.interactive_args ?? (a?.interactive ? a.args : null)
  Object.assign(agentForm, {
    key: a?.key ?? '',
    type: a?.type ?? 'cli-agent',
    command: a?.command ?? '',
    argsText: linesText(a?.args),
    // 有效交互模式 = interactive_args 非 null，或旧式 interactive
    interactiveMode: (a?.interactive_args ?? null) !== null || (a?.interactive ?? false),
    interactiveArgsText: linesText(interactiveArgs),
    sessionCapture: a?.session_capture ?? '',
    sessionInjectText: linesText(a?.session_inject),
    sessionResumeText: linesText(a?.session_resume),
    maxConcurrent: a && a.max_concurrent > 0 ? String(a.max_concurrent) : '',
    stallTimeoutSec: a?.stall_timeout_sec != null ? String(a.stall_timeout_sec) : '',
    fallbackAgentsText: linesText(a?.fallback_agents),
    outputFormat: a?.output_format ?? '',
    retryMaxAttempts: a?.retry ? String(a.retry.max_attempts) : '',
    retryBackoffText: linesText(a?.retry?.backoff_sec?.map(String) ?? null),
    acpPermissionPolicy: a?.acp?.permission_policy ?? '',
  })
  void refreshPreview()
}

function openServerEditor(): void {
  const sc = config.value?.server
  if (!sc) {
    return
  }
  writeError.value = ''
  formError.value = ''
  notice.value = ''
  badFields.value = new Set()
  editing.value = null
  editor.value = 'server'
  Object.assign(serverForm, {
    maxJobTimeoutSec: sc.max_job_timeout_sec > 0 ? String(sc.max_job_timeout_sec) : '',
    stallTimeoutSec: sc.stall_timeout_sec != null ? String(sc.stall_timeout_sec) : '',
    autoResumeMax: sc.auto_resume_max != null ? String(sc.auto_resume_max) : '',
    probeIntervalSec: String(sc.runner_probe.interval_seconds || ''),
    probeTimeoutSec: String(sc.runner_probe.timeout_seconds || ''),
    retryMaxAttempts: sc.retry ? String(sc.retry.max_attempts) : '',
    retryBackoffText: linesText(sc.retry?.backoff_sec?.map(String) ?? null),
  })
  void refreshPreview()
}

function closeEditor(): void {
  editor.value = null
  editing.value = null
  preview.value = ''
  previewRestart.value = []
  formError.value = ''
  badFields.value = new Set()
}

// editableAgentBody 把"当前 agent 的整份可编辑字段集"作为基底：PUT 是整体替换语义
// （body 里没有的可编辑字段会被清空），所以表单没覆盖的字段必须原样回发。字段清单来自
// 服务端的策略表（agent_policy），控制台不自己维护第二份白名单。
function editableAgentBody(base: Partial<ConfigAgentView>): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  const src = base as unknown as Record<string, unknown>
  for (const [name, fp] of Object.entries(agentPolicy.value)) {
    if (!fp.editable) {
      continue
    }
    body[name] = src[name] ?? null
  }
  return body
}

// buildAgentWrite：表单值覆盖基底。interactive_args 的 null/[] 区别由交互模式开关决定
// （null = 批处理；[] = 交互且无额外 argv，AGT-02）。
function buildAgentWrite(): Record<string, unknown> {
  const base = editing.value
  const body = editableAgentBody(base ?? ({} as Partial<ConfigAgentView>))
  const retry = optionalInt(agentForm.retryMaxAttempts)
  body.type = agentForm.type
  body.command = agentForm.command.trim()
  body.args = lines(agentForm.argsText)
  body.interactive = agentForm.interactiveMode ? (base?.interactive ?? false) : false
  body.interactive_args = agentForm.interactiveMode ? lines(agentForm.interactiveArgsText) : null
  body.session_capture = agentForm.sessionCapture.trim()
  body.session_inject = lines(agentForm.sessionInjectText)
  body.session_resume = lines(agentForm.sessionResumeText)
  body.max_concurrent = optionalInt(agentForm.maxConcurrent) ?? 0
  body.stall_timeout_sec = optionalInt(agentForm.stallTimeoutSec)
  body.fallback_agents = lines(agentForm.fallbackAgentsText)
  body.output_format = agentForm.outputFormat
  body.retry = retry === null ? null : { max_attempts: retry, backoff_sec: lines(agentForm.retryBackoffText).map(Number) }
  if (agentForm.type === 'acp-agent') {
    // acp 服务端是"补丁式"应用（见 handler）：这里带上表单能显示的成员，其余
    // （load_session / mcp_servers）由服务端保留，不会被清掉。
    body.acp = { permission_policy: agentForm.acpPermissionPolicy, modes: base?.acp?.modes ?? null }
  }
  return body
}

function buildServerWrite(): Record<string, unknown> {
  return {
    max_job_timeout_sec: optionalInt(serverForm.maxJobTimeoutSec) ?? 0,
    stall_timeout_sec: optionalInt(serverForm.stallTimeoutSec),
    auto_resume_max: optionalInt(serverForm.autoResumeMax),
    runner_probe: {
      interval_seconds: optionalInt(serverForm.probeIntervalSec) ?? 0,
      timeout_seconds: optionalInt(serverForm.probeTimeoutSec) ?? 0,
    },
    retry:
      optionalInt(serverForm.retryMaxAttempts) === null
        ? null
        : {
            max_attempts: optionalInt(serverForm.retryMaxAttempts),
            backoff_sec: lines(serverForm.retryBackoffText).map(Number),
          },
  }
}

let previewTimer: number | null = null

// refreshPreview 用服务端的干跑接口拿预览与影响面（不在前端本地拼 YAML）：
// 校验失败时它同时给出要标红的字段路径。
function refreshPreview(): void {
  if (previewTimer != null) {
    window.clearTimeout(previewTimer)
  }
  previewTimer = window.setTimeout(() => {
    void runPreview()
  }, 250)
}

async function runPreview(): Promise<void> {
  if (editor.value === null || config.value === null) {
    return
  }
  validating.value = true
  try {
    const result: ConfigValidateResult = await validateConfig(
      isServer.value
        ? { section: 'server', value: buildServerWrite() }
        : { section: 'agents', key: agentForm.key.trim(), value: buildAgentWrite() },
    )
    preview.value = result.preview ?? ''
    previewRestart.value = result.restart_required ?? []
    if (result.ok) {
      badFields.value = new Set()
      formError.value = ''
    } else {
      markBadFields(result.error_fields)
      formError.value = result.detail || result.errors.join('; ')
    }
  } catch (e) {
    // 干跑本身失败（网络/403）：不阻塞保存，保存时还会有一次服务端判定。
    formError.value = classifyWriteError(e)
  } finally {
    validating.value = false
  }
}

async function saveEditor(): Promise<void> {
  formError.value = ''
  writeError.value = ''
  saving.value = true
  try {
    if (isServer.value) {
      const resp = await putConfigServer(buildServerWrite())
      notice.value = `server 配置已保存（${resp.fields.join(', ') || '无字段'}）· 已重载`
    } else {
      const key = agentForm.key.trim()
      if (key === '') {
        formError.value = '请填写 agent key'
        return
      }
      const resp = await putConfigAgent(key, buildAgentWrite())
      notice.value = `agent ${key} 已保存（${resp.created ? '新增' : '更新'}）· 已重载`
    }
    closeEditor()
    await loadConfig(true)
  } catch (e) {
    if (e instanceof ApiError) {
      markBadFields(e.fields)
      formError.value = classifyWriteError(e)
    } else {
      formError.value = errorMessage(e)
    }
  } finally {
    saving.value = false
  }
}

async function removeAgent(a: ConfigAgentView): Promise<void> {
  const fallback = a.injected || agentLevelHint(a.key)
  const msg = fallback
    ? `删除 agent「${a.key}」？该 key 对应内置/运行时注入定义，删除后**回落到内置定义**（能力仍在），不是彻底移除。`
    : `删除 agent「${a.key}」？该定义将从 config.yaml 中移除。`
  if (!window.confirm(msg)) {
    return
  }
  writeError.value = ''
  notice.value = ''
  try {
    const resp = await deleteConfigAgent(a.key)
    notice.value = resp.fell_back_to_builtin
      ? `agent ${a.key} 已删除 · 回落到内置定义 · 已重载`
      : `agent ${a.key} 已删除 · 已重载`
    if (editing.value?.key === a.key) {
      closeEditor()
    }
    await loadConfig(true)
  } catch (e) {
    writeError.value = classifyWriteError(e)
  }
}

// agentLevelHint：内置模板 key 的粗判（claude/codex/omp/…-acp/exec）仅用于确认文案措辞；
// 真正的回落标记来自服务端响应 fell_back_to_builtin。
function agentLevelHint(key: string): boolean {
  return key === 'exec' || key === 'claude' || key === 'codex' || key === 'omp' || key.endsWith('-acp')
}

async function reloadNow(): Promise<void> {
  writeError.value = ''
  notice.value = ''
  try {
    await reloadConfig()
    notice.value = '已重新读取 config.yaml（手工编辑的文件改动已生效）'
    await loadConfig(true)
  } catch (e) {
    writeError.value = classifyWriteError(e)
  }
}

function classifyWriteError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) {
      return '无 can_admin 权限，当前 caller 不允许编辑配置。'
    }
    if (e.status === 400) {
      return `配置校验失败：${e.detail || e.message}`
    }
    if (e.status === 404) {
      return `目标不存在：${e.detail || e.message}`
    }
    if (e.status === 503) {
      return `服务端未启用配置写：${e.detail || e.message}`
    }
  }
  return e instanceof Error ? e.message : String(e)
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
      <h1 class="title mono">系统配置</h1>
      <button class="mini-btn mono" type="button" :disabled="loading" @click="reloadNow()">
        重新读取文件
      </button>
      <span class="poll mono" :class="{ 'poll--on': loading }">●</span>
    </div>

    <p v-if="loadError" class="error mono">{{ loadError }}</p>
    <p v-else-if="loading && !loaded" class="placeholder mono">加载配置中...</p>
    <p v-if="notice" class="notice mono">{{ notice }}</p>
    <p v-if="writeError" class="error mono">{{ writeError }}</p>

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
            <h3 class="panel-title mono">
              SERVER
              <button class="mini-btn mono inline-btn" type="button" @click="openServerEditor()">编辑</button>
            </h3>
            <dl class="kv mono">
              <dt>addr</dt><dd>{{ config.server.addr || '-' }}</dd>
              <dt>path_view</dt><dd>{{ config.server.path_view || '-' }}</dd>
              <dt>web_enabled</dt><dd><span class="flag" :class="config.server.web_enabled ? 'flag--yes' : 'flag--no'">{{ yesNo(config.server.web_enabled) }}</span></dd>
              <dt>server token</dt><dd><span class="flag" :class="config.server.token_set ? 'flag--yes' : 'flag--no'">{{ setText(config.server.token_set) }}</span></dd>
              <dt>allow_empty_token</dt><dd><span class="flag" :class="config.server.allow_empty_token ? 'flag--warn' : 'flag--no'">{{ yesNo(config.server.allow_empty_token) }}</span></dd>
              <dt>max_job_timeout_sec</dt><dd>{{ config.server.max_job_timeout_sec || '-' }}</dd>
              <dt>stall_timeout_sec</dt><dd>{{ config.server.stall_timeout_sec ?? '默认' }}</dd>
              <dt>auto_resume_max</dt><dd>{{ config.server.auto_resume_max ?? '默认' }}</dd>
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
            <h3 class="panel-title mono">
              AGENTS
              <button class="mini-btn mono inline-btn" type="button" @click="openAgentEditor(null)">新增 agent</button>
            </h3>
            <ul class="mini-list">
              <li v-for="a in agents" :key="a.key" class="mini-row mono">
                <span class="item-main">{{ a.key }} · {{ a.type }}</span>
                <span v-if="a.command" class="muted">{{ a.command }}</span>
                <span v-if="a.injected" class="tag tag--builtin">内置</span>
                <span v-for="k in a.env_keys" :key="`${a.key}:${k}`" class="tag">{{ k }}</span>
                <span class="row-actions">
                  <button class="mini-btn mono" type="button" @click="openAgentEditor(a)">编辑</button>
                  <button class="mini-btn mono" type="button" @click="removeAgent(a)">删除</button>
                </span>
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
    </template>

    <div v-if="isEdit" class="scrim" @click.self="closeEditor()">
      <div class="modal" role="dialog" aria-modal="true">
        <div class="modal-head">
          <h2 class="modal-title mono">{{ isServer ? '编辑 server 配置' : agentTitle }}</h2>
          <span v-if="validating" class="muted mono">校验中...</span>
          <button class="mini-btn mono" type="button" @click="closeEditor()">关闭</button>
        </div>

        <div class="modal-body">
          <form class="edit-form mono" @submit.prevent="saveEditor()">
            <template v-if="!isServer">
              <label class="field">
                <span class="field-name">key <span class="req">*</span></span>
                <input
                  v-model="agentForm.key"
                  class="input"
                  :disabled="editor === 'agent-edit'"
                  placeholder="jcode"
                  @change="refreshPreview()"
                />
              </label>
              <label class="field">
                <span class="field-name">type</span>
                <select v-model="agentForm.type" class="input" @change="refreshPreview()">
                  <option value="cli-agent">cli-agent</option>
                  <option value="exec">exec</option>
                  <option value="acp-agent">acp-agent</option>
                </select>
              </label>
              <label class="field">
                <span class="field-name">command</span>
                <input
                  v-model="agentForm.command"
                  class="input"
                  :class="{ 'input--bad': fieldBad('command') }"
                  placeholder="jcode"
                  @change="refreshPreview()"
                />
              </label>
              <label class="field">
                <span class="field-name">args（每行一个，批处理 argv）</span>
                <textarea
                  v-model="agentForm.argsText"
                  class="input textarea"
                  :class="{ 'input--bad': fieldBad('args') }"
                  rows="3"
                  @change="refreshPreview()"
                ></textarea>
              </label>
              <label class="field field--check">
                <input v-model="agentForm.interactiveMode" type="checkbox" @change="refreshPreview()" />
                <span class="field-name">交互模式（pty/TUI）</span>
              </label>
              <label v-if="agentForm.interactiveMode" class="field">
                <span class="field-name">interactive_args（每行一个，留空 = 无额外 argv）</span>
                <textarea
                  v-model="agentForm.interactiveArgsText"
                  class="input textarea"
                  :class="{ 'input--bad': fieldBad('interactive_args') }"
                  rows="3"
                  @change="refreshPreview()"
                ></textarea>
              </label>
              <label class="field">
                <span class="field-name">session_capture（正则，第一个非空捕获组 = session id）</span>
                <input
                  v-model="agentForm.sessionCapture"
                  class="input"
                  :class="{ 'input--bad': fieldBad('session_capture') }"
                  @change="refreshPreview()"
                />
              </label>
              <label class="field">
                <span class="field-name">session_inject（每行一个，&#123;&#123;session_id&#125;&#125; 占位）</span>
                <textarea v-model="agentForm.sessionInjectText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label class="field">
                <span class="field-name">session_resume（每行一个，续接 argv）</span>
                <textarea v-model="agentForm.sessionResumeText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label class="field">
                <span class="field-name">max_concurrent（0 = 不限）</span>
                <input v-model="agentForm.maxConcurrent" class="input" :class="{ 'input--bad': fieldBad('max_concurrent') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">stall_timeout_sec（留空 = 继承 server）</span>
                <input v-model="agentForm.stallTimeoutSec" class="input" :class="{ 'input--bad': fieldBad('stall_timeout_sec') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">fallback_agents（每行一个）</span>
                <textarea v-model="agentForm.fallbackAgentsText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label class="field">
                <span class="field-name">output_format</span>
                <input v-model="agentForm.outputFormat" class="input" :class="{ 'input--bad': fieldBad('output_format') }" placeholder="text / ndjson" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">retry.max_attempts（留空 = 不重试）</span>
                <input v-model="agentForm.retryMaxAttempts" class="input" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">retry.backoff_sec（每行一个秒数）</span>
                <textarea v-model="agentForm.retryBackoffText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label v-if="agentForm.type === 'acp-agent'" class="field">
                <span class="field-name">acp.permission_policy</span>
                <input v-model="agentForm.acpPermissionPolicy" class="input" :class="{ 'input--bad': fieldBad('acp') }" placeholder="auto_allow / ask / strict" @change="refreshPreview()" />
              </label>

              <p v-if="editing" class="hint mono">
                不在本表单内的字段由服务端原样保留：env_keys {{ joinOrDash(editing.env_keys) }} ·
                detect {{ editing.detect.command || '-' }} · mcp_server_name {{ editing.mcp_server_name || '-' }}
                （含 secret 的 env / acp.mcp_servers 值不进控制台，也不会被改写）
              </p>
              <p v-else class="hint mono">
                新增 agent：只需 command（交互模式再加 interactive_args）。会话捕获/续接有内置与兜底默认，
                不写也能用。
              </p>
            </template>

            <template v-else>
              <label class="field">
                <span class="field-name">max_job_timeout_sec（job 超时上限，0 = 默认 1h）</span>
                <input v-model="serverForm.maxJobTimeoutSec" class="input" :class="{ 'input--bad': fieldBad('max_job_timeout_sec') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">stall_timeout_sec（无输出看门狗，留空 = 默认 900s）</span>
                <input v-model="serverForm.stallTimeoutSec" class="input" :class="{ 'input--bad': fieldBad('stall_timeout_sec') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">auto_resume_max（留空 = 默认 1）</span>
                <input v-model="serverForm.autoResumeMax" class="input" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">runner_probe.interval_seconds</span>
                <input v-model="serverForm.probeIntervalSec" class="input" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">runner_probe.timeout_seconds</span>
                <input v-model="serverForm.probeTimeoutSec" class="input" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">retry.max_attempts（留空 = 全站不重试）</span>
                <input v-model="serverForm.retryMaxAttempts" class="input" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">retry.backoff_sec（每行一个秒数）</span>
                <textarea v-model="serverForm.retryBackoffText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <div v-if="restartOnly.length > 0" class="field">
                <span class="field-name">以下字段只在启动时读取，控制台不可编辑（需重启）</span>
                <div class="badges">
                  <span v-for="name in restartOnly" :key="name" class="badge mono">{{ name }} · 需重启</span>
                </div>
              </div>
              <p class="hint mono">
                它们要改请编辑 config.yaml 后在主机执行 <code>start.ps1 -Action restart</code>；
                改完想立刻生效的文件改动也可以点顶部「重新读取文件」（仅对可热重载项有效）。
              </p>
            </template>

            <div class="form-actions">
              <button class="mini-btn mono" type="submit" :disabled="saving">
                {{ saving ? '保存中...' : '保存' }}
              </button>
              <span v-if="previewRestart.length > 0" class="impact mono">
                保存后立即生效；以下项仍需改文件 + 重启：{{ previewRestart.join(', ') }}
              </span>
              <span v-else class="impact mono">保存后立即生效（写事务内含热重载）</span>
            </div>
            <p v-if="formError" class="error mono">{{ formError }}</p>
          </form>

          <div class="preview">
            <div class="preview-head mono">
              <span>YAML 预览（来自服务端干跑校验）</span>
            </div>
            <pre class="preview-body mono">{{ preview || '（校验通过后显示）' }}</pre>
          </div>
        </div>
      </div>
    </div>
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
.block-title,
.modal-title {
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
.inline-btn {
  float: right;
  margin-left: 8px;
  padding: 1px 8px;
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
.panel {
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
.tag--builtin {
  color: var(--queue);
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
.mini-list {
  list-style: none;
  margin: 0;
  padding: 0;
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
.row-actions {
  display: inline-flex;
  gap: 6px;
  margin-top: 4px;
}
.row-actions .mini-btn {
  margin-left: 0;
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
.error,
.notice {
  font-size: 12px;
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 10px;
  word-break: break-word;
}
.error {
  color: var(--fail);
  border: 1px solid var(--fail);
}
.notice {
  color: var(--done);
  border: 1px solid var(--done);
}

/* 编辑弹窗 */
.scrim {
  position: fixed;
  inset: 0;
  z-index: 40;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 40px 16px;
  overflow: auto;
}
.modal {
  width: min(1080px, 100%);
  background: var(--bg, var(--panel));
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.modal-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--line);
}
.modal-body {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 14px;
  padding: 14px;
}
.edit-form {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 11px;
}
.field--check {
  flex-direction: row;
  align-items: center;
  gap: 8px;
}
.field-name {
  color: var(--queue);
}
.req {
  color: var(--fail);
}
.input {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px 8px;
  font-family: inherit;
  font-size: 12px;
}
.input:disabled {
  color: var(--queue);
}
.input--bad {
  border-color: var(--fail);
}
.textarea {
  resize: vertical;
}
.hint,
.impact {
  font-size: 11px;
  color: var(--queue);
  margin: 2px 0 0;
  line-height: 1.6;
}
.badges {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 2px;
}
.badge {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 6px;
  font-size: 10px;
  color: var(--run);
}
.form-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 4px;
}
.form-actions .mini-btn {
  margin-left: 0;
}
.preview {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px;
  min-height: 240px;
}
.preview-head {
  font-size: 11px;
  color: var(--queue);
  margin-bottom: 8px;
}
.preview-body {
  font-size: 11px;
  color: var(--paper);
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
}
@media (max-width: 900px) {
  .overview-grid,
  .cards-3,
  .modal-body {
    grid-template-columns: 1fr;
  }
}
</style>
