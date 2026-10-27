<script setup lang="ts">
// Config：脱敏配置总览 + agents/server 编辑（WEB-04③ V1.1）。
// 只展示后端 bool 化后的 secret 状态，不接收、不缓存、不渲染任何 secret 值；
// 写请求体里也永远没有 secret 字段（服务端会以 `secret value not accepted` 拒绝字面值）。
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { t } from '../i18n'
import { createLiveTopic } from '../utils/useLiveTopic'
import { refreshWhoami, useWhoami } from '../store/whoami'
import { extendsBase, extendsLabel } from '../utils/agentExtends'
import { leaderRows, warningsSuffix } from '../utils/configLeader'
import { PAGE } from '../utils/uiText'
import { ChevronRight, Power, RefreshCw } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import {
  ApiError,
  cancelServerRestart,
  deleteConfigAgent,
  getConfig,
  getServerRestart,
  putConfigAgent,
  putConfigServer,
  reloadConfig,
  restartServer,
  validateConfig,
} from '../api/client'
import { fmtDateTime, fmtDuration } from '../api/time'
import {
  cfgItems as buildCfgItems,
  cfgRows as buildCfgRows,
  type CfgLabels,
  type CfgRow,
  type CfgSection,
} from './configRows'
import type {
  ConfigAgentView,
  ConfigView,
  ConfigValidateResult,
  FieldPolicy,
  ServerRestartState,
  ServerRestartView,
} from '../api/types'


// 写配置需要 config.write；没有就不摆编辑 / 新增 / 删除 / 重新读取（读仍可看）。
const { can } = useWhoami()
const config = ref<ConfigView | null>(null)
const loading = ref(false)
const loaded = ref(false)
const loadError = ref('')
const notice = ref('')
const writeError = ref('')


const agents = computed(() => config.value?.agents ?? [])
const runners = computed(() => config.value?.runners ?? [])
const roles = computed(() => config.value?.roles ?? [])
const callers = computed(() => config.value?.server.callers ?? [])
// 配置用 include 拆成多个文件时，按文件列出各自包含的顶层段，方便找到要改的文件。
const sourceGroups = computed(() => {
  const by: Record<string, string[]> = {}
  for (const [section, file] of Object.entries(config.value?.sources ?? {})) {
    ;(by[file] ??= []).push(section)
  }
  return Object.entries(by)
    .map(([file, sections]) => ({ file, sections: sections.sort() }))
    .sort((a, b) => a.file.localeCompare(b.file))
})
const workers = computed(() => config.value?.server.workers ?? [])
const eventHooks = computed(() => config.value?.server.event_hooks ?? [])
const decisionHooks = computed(() => config.value?.server.decision_hooks ?? [])

// ------------------------------------------------------- 完整配置（只读分组）
//
// gofer-4uk5：GET /v1/config 现在会把每个顶层段都发过来，下面把这些段按分组展示。除了
// 已有的 server / agents 编辑入口外，这些段在本接口上是只读的（要改就改 config.yaml +
// 「重新读取文件」或「重启 server」），所以每组都带「只读」标。字段名是配置键本身（技术
// 标识不翻译），值按类型格式化；`*_set` 字段是服务端的脉敏表示，只显示「已配置 / 未配置」。

// cfgLabels 把配置行的文案（是 / 否 / 已配置 / 未配置 / 默认 / 列表分隔符）交给纯函数
// configRows.ts，这样两种语言与格式化逻辑可以分别测。
const cfgLabels = computed<CfgLabels>(() => ({
  yes: t('settings.config.yes'),
  no: t('settings.config.no'),
  set: t('settings.config.set'),
  unset: t('settings.config.unset'),
  def: t('settings.config.default'),
  sep: t('util.sep.list'),
}))

// 两个薄包装：调用处只管传数据，文案从当前语言取。
function cfgRows(obj: Record<string, unknown> | undefined, prefix = ''): CfgRow[] {
  return buildCfgRows(obj, cfgLabels.value, prefix)
}

function cfgItems(items: Record<string, unknown>[] | undefined, titleKey: string): CfgSection['items'] {
  return buildCfgItems(items, titleKey, cfgLabels.value)
}

// extraSections 是「除已有总览卡片以外的全部配置段」。空段不渲染（没有配置就不占地方）。
const extraSections = computed<CfgSection[]>(() => {
  const c = config.value
  if (!c) {
    return []
  }
  const s = c.server
  const serverExtra = cfgRows({
    web_dir: s.web_dir,
    web_base_url: s.web_base_url,
    tls: s.tls,
    update: s.update,
    worker_enrollment: s.worker_enrollment,
    push: s.push,
    session_messaging: s.session_messaging,
    policy_repush: s.policy_repush,
    agent_fallback: s.agent_fallback,
    xfer: s.xfer,
    attachments: s.attachments,
    worker_queue_max_wait_sec: s.worker_queue_max_wait_sec,
    hold: s.hold,
    comment_trigger: s.comment_trigger,
    dir_lock: s.dir_lock,
    dir_lock_max_wait_sec: s.dir_lock_max_wait_sec,
    dir_lock_allow_unbounded_wait: s.dir_lock_allow_unbounded_wait,
    agent_health: s.agent_health,
    skill_limits: s.skill_limits,
    job_env_denylist: s.job_env_denylist,
    tunnel: s.tunnel,
    mcp: s.mcp,
  })
  const mailRaw = (c.mail ?? {}) as Record<string, unknown>
  const { agents: mailAgents, ...mailRest } = mailRaw
  const sections: CfgSection[] = [
    { key: 'serverExtra', title: t('settings.config.sec.serverExtra'), rows: serverExtra },
    {
      key: 'storageExtra',
      title: t('settings.config.sec.storageExtra'),
      rows: cfgRows({
        mail_retention_days: c.storage.mail_retention_days,
        attachment_retention_days: c.storage.attachment_retention_days,
        cast: c.storage.cast,
        prune_interval_minutes: c.storage.retention.prune_interval_minutes,
        workflow_max_age_days: c.storage.retention.workflow_max_age_days,
      }),
    },
    { key: 'log', title: t('settings.config.sec.log'), rows: cfgRows(c.log) },
    { key: 'wakeup', title: t('settings.config.sec.wakeup'), rows: cfgRows(c.wakeup) },
    { key: 'session', title: t('settings.config.sec.session'), rows: cfgRows(c.session) },
    { key: 'work', title: t('settings.config.sec.work'), rows: cfgRows(c.work) },
    { key: 'steward', title: t('settings.config.sec.steward'), rows: cfgRows(c.steward) },
    { key: 'pty', title: t('settings.config.sec.pty'), rows: cfgRows(c.pty) },
    {
      key: 'mail',
      title: t('settings.config.sec.mail'),
      rows: cfgRows(mailRest),
      items: cfgItems(mailAgents as Record<string, unknown>[] | undefined, 'key'),
    },
    { key: 'httpProxy', title: t('settings.config.sec.httpProxy'), rows: cfgRows(c.http_proxy) },
    {
      key: 'tools',
      title: t('settings.config.sec.tools'),
      rows: [],
      items: cfgItems(c.tools as Record<string, unknown>[] | undefined, 'name'),
    },
    {
      key: 'mcpServers',
      title: t('settings.config.sec.mcpServers'),
      rows: [],
      items: cfgItems(c.mcp_servers as Record<string, unknown>[] | undefined, 'key'),
    },
    {
      key: 'secrets',
      title: t('settings.config.sec.secrets'),
      rows: [],
      items: cfgItems(c.secrets as Record<string, unknown>[] | undefined, 'key'),
    },
    {
      key: 'pricing',
      title: t('settings.config.sec.pricing'),
      rows: [],
      items: cfgItems(c.pricing as Record<string, unknown>[] | undefined, 'model'),
    },
  ]
  return sections.filter((sec) => sec.rows.length > 0 || (sec.items?.length ?? 0) > 0)
})

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

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function yesNo(v: boolean): string {
  return v ? t('settings.config.yes') : t('settings.config.no')
}

function setText(v: boolean): string {
  return v ? t('settings.config.set') : t('settings.config.unset')
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
  exitKeysText: string
  exitGraceSec: string
  sessionStoreGlob: string
  sessionStoreIDRegex: string
  sessionResumeText: string
  maxConcurrent: string
  stallTimeoutSec: string
  fallbackAgentsText: string
  outputFormat: string
  retryMaxAttempts: string
  retryBackoffText: string
  acpPermissionPolicy: string
  skillsText: string
  rulesText: string
  // MCP-06：注册表 MCP 绑定、注入方式、claude-json 的参数片段。
  mcpText: string
  mcpFormat: string
  mcpArgsText: string
  // 自研 agent 接入字段（G 批）：用量路径 / transcript 方言 / tmux 送话进程 / 会话族 / 在线送话命令。
  ndjsonUsagePath: string
  transcriptDialect: string
  injectProcessText: string
  sessionFamily: string
  deliverCommandText: string
  deliverStdin: boolean
  deliverOfflineMatch: string
}

interface ServerForm {
  maxJobTimeoutSec: string
  stallTimeoutSec: string
  autoResumeMax: string
  probeIntervalSec: string
  probeTimeoutSec: string
  retryMaxAttempts: string
  retryBackoffText: string
  skillsText: string
  rulesText: string
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
  exitKeysText: '',
  exitGraceSec: '',
  sessionStoreGlob: '',
  sessionStoreIDRegex: '',
  sessionResumeText: '',
  maxConcurrent: '',
  stallTimeoutSec: '',
  fallbackAgentsText: '',
  outputFormat: '',
  retryMaxAttempts: '',
  retryBackoffText: '',
  acpPermissionPolicy: '',
  skillsText: '',
  rulesText: '',
  mcpText: '',
  mcpFormat: '',
  mcpArgsText: '',
  ndjsonUsagePath: '',
  transcriptDialect: '',
  injectProcessText: '',
  sessionFamily: '',
  deliverCommandText: '',
  deliverStdin: false,
  deliverOfflineMatch: '',
})

const serverForm = reactive<ServerForm>({
  maxJobTimeoutSec: '',
  stallTimeoutSec: '',
  autoResumeMax: '',
  probeIntervalSec: '',
  probeTimeoutSec: '',
  retryMaxAttempts: '',
  retryBackoffText: '',
  skillsText: '',
  rulesText: '',
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
const agentTitle = computed(() => (editor.value === 'agent-create'
  ? t('settings.config.agentCreateTitle')
  : t('settings.config.agentEditTitle', { key: agentForm.key })))

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
    exitKeysText: linesText(a?.exit_keys),
    exitGraceSec: a?.exit_grace_sec != null ? String(a.exit_grace_sec) : '',
    sessionStoreGlob: a?.session_store_glob ?? '',
    sessionStoreIDRegex: a?.session_store_id_regex ?? '',
    sessionResumeText: linesText(a?.session_resume),
    maxConcurrent: a && a.max_concurrent > 0 ? String(a.max_concurrent) : '',
    stallTimeoutSec: a?.stall_timeout_sec != null ? String(a.stall_timeout_sec) : '',
    fallbackAgentsText: linesText(a?.fallback_agents),
    outputFormat: a?.output_format ?? '',
    retryMaxAttempts: a?.retry ? String(a.retry.max_attempts) : '',
    retryBackoffText: linesText(a?.retry?.backoff_sec?.map(String) ?? null),
    acpPermissionPolicy: a?.acp?.permission_policy ?? '',
    skillsText: linesText(a?.skills),
    rulesText: linesText(a?.rules),
    mcpText: linesText(a?.mcp ?? null),
    mcpFormat: a?.mcp_format ?? '',
    mcpArgsText: linesText(a?.mcp_args ?? null),
    ndjsonUsagePath: a?.ndjson_usage_path ?? '',
    transcriptDialect: a?.transcript_dialect ?? '',
    injectProcessText: linesText(a?.inject_process),
    sessionFamily: a?.session_family ?? '',
    deliverCommandText: linesText(a?.deliver_command),
    deliverStdin: a?.deliver_stdin ?? false,
    deliverOfflineMatch: a?.deliver_offline_match ?? '',
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
    skillsText: linesText(sc.skills),
    rulesText: linesText(sc.rules),
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
  body.exit_keys = lines(agentForm.exitKeysText)
  body.exit_grace_sec = optionalInt(agentForm.exitGraceSec)
  body.session_store_glob = agentForm.sessionStoreGlob.trim()
  body.session_store_id_regex = agentForm.sessionStoreIDRegex.trim()
  body.session_resume = lines(agentForm.sessionResumeText)
  body.max_concurrent = optionalInt(agentForm.maxConcurrent) ?? 0
  body.stall_timeout_sec = optionalInt(agentForm.stallTimeoutSec)
  body.fallback_agents = lines(agentForm.fallbackAgentsText)
  body.output_format = agentForm.outputFormat
  // 旧 server 的策略表里没有这些字段 → 整条不发（否则会被写端点当未知字段拒掉整个保存）。
  const integ: Record<string, unknown> = {
    ndjson_usage_path: agentForm.ndjsonUsagePath.trim(),
    transcript_dialect: agentForm.transcriptDialect.trim(),
    inject_process: lines(agentForm.injectProcessText),
    session_family: agentForm.sessionFamily.trim(),
    deliver_command: lines(agentForm.deliverCommandText),
    deliver_stdin: agentForm.deliverStdin,
    deliver_offline_match: agentForm.deliverOfflineMatch.trim(),
  }
  for (const [name, value] of Object.entries(integ)) {
    if (agentPolicy.value[name]?.editable) {
      body[name] = value
    }
  }
  body.retry = retry === null ? null : { max_attempts: retry, backoff_sec: lines(agentForm.retryBackoffText).map(Number) }
  // JOB-10 skills：基底里带的是"上次读到的绑定"，这里用表单值覆盖（清空 = 解绑该 agent
  // 自己的清单；server/project 的绑定不受影响）。
  body.skills = lines(agentForm.skillsText)
  // JOB-06① rules：与 skills 同理（清空 = 该 agent 不再绑自己的规则；server/project 的绑定
  // 不受影响）。旧 server 的策略表里没有这个字段（视图也读不到）→ 整条不发，免得被写端点
  // 当成未知字段拒掉整个保存。
  if (agentPolicy.value['rules']?.editable) {
    body.rules = lines(agentForm.rulesText)
  }
  // MCP-06：与 rules 同理，策略表里没有（旧 server）就整条不发。
  if (agentPolicy.value['mcp']?.editable) {
    body.mcp = lines(agentForm.mcpText)
    body.mcp_format = agentForm.mcpFormat.trim()
    body.mcp_args = lines(agentForm.mcpArgsText)
  }
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
    // JOB-10：全局默认技能绑定（server.skills），逐行一个名字。
    skills: lines(serverForm.skillsText),
    // JOB-06①：部署级强制规则绑定（server.rules），逐行一个名字。server PUT 是**部分**更新，
    // 但整份表单照发（与 skills 一致，免得"改一个字段顺手抹掉别的"）；旧 server 的策略表里
    // 没有这个字段 → 不发，否则会被当成未知字段拒掉。
    ...(serverPolicy.value['rules']?.editable ? { rules: lines(serverForm.rulesText) } : {}),
    // notification（IM 机器人 / 桌面通知 / webhook 的 outbound 安全旋钮）不在这个表单里编辑：
    // 它有独立的一页「设置 · 通知」，两处编辑同一份配置只会互相覆盖。body 里干脆不带这个
    // 成员，服务端的补丁式写入就原样保留已配置的 webhook（含读不到的 secret_env 名字）、
    // desktop 与 max_text_runes 等。
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
      notice.value = t('settings.config.serverSaved', {
        fields: resp.fields.join(', ') || t('settings.config.noFields'),
        suffix: warningsSuffix(resp.warnings),
      })
    } else {
      const key = agentForm.key.trim()
      if (key === '') {
        formError.value = t('settings.config.needKey')
        return
      }
      const resp = await putConfigAgent(key, buildAgentWrite())
      notice.value = t('settings.config.agentSaved', {
        key,
        action: resp.created ? t('settings.config.created') : t('settings.config.updated'),
        suffix: warningsSuffix(resp.warnings),
      })
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
    ? t('settings.config.removeAgentFallback', { key: a.key })
    : t('settings.config.removeAgent', { key: a.key })
  if (!window.confirm(msg)) {
    return
  }
  writeError.value = ''
  notice.value = ''
  try {
    const resp = await deleteConfigAgent(a.key)
    notice.value = resp.fell_back_to_builtin
      ? t('settings.config.agentRemovedFallback', { key: a.key })
      : t('settings.config.agentRemoved', { key: a.key })
    if (editing.value?.key === a.key) {
      closeEditor()
    }
    await loadConfig(true)
    void refreshWhoami()
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
    const result = await reloadConfig()
    const restart = result.restart_required?.length
      ? t('settings.config.reloadRestart', { list: result.restart_required.join(t('util.sep.list')) })
      : ''
    notice.value = t('settings.config.reloaded', {
      rev: result.rev,
      changed: (result.changed || []).join(t('util.sep.list')) || t('settings.config.noChange'),
      restart,
    })
    // 配置推出的权限可能变了：重取 whoami，界面据此显隐操作。
    void refreshWhoami()
    await loadConfig(true)
  } catch (e) {
    writeError.value = classifyWriteError(e)
  }
}

// ---------------------------------------------------------------- 重启 server
//
// gofer-4uk5：「重新读取文件」只能让进程重读配置；有些项（监听地址、存储、TLS 等）启动时
// 才读，必须重启。这里走 POST /v1/server/restart：复用一键更新的「停放新 job → 等机器空闲
// → 交给 serve upgrade 换二进制」，只是不下载任何东西（候选就是当前这个可执行文件的副本）。
const restart = ref<ServerRestartView | null>(null)
const showRestart = ref(false)
const restarting = ref(false)
const restartError = ref('')
const restartWaitSec = ref(7200)
const restartUnreachable = ref(false)
const restartCancelling = ref(false)
const restartCancelError = ref('')
let restartTimer: ReturnType<typeof setTimeout> | null = null

const restartManaged = computed(() => restart.value?.managed === true)
const restartState = computed<ServerRestartState | null>(() => restart.value?.state ?? null)
// 只有这两个阶段算「进行中」：restarting 之后本进程会退出，界面靠「连不上 → 再连上」收尾。
const restartActive = computed(() => {
  const phase = restartState.value?.phase
  return phase === 'waiting_idle' || phase === 'restarting'
})
const restartWaiting = computed(() => restartState.value?.phase === 'waiting_idle')
const restartWaitingRows = computed(() => (restartWaiting.value ? (restartState.value?.waiting ?? []) : []))
const restartPhaseText = computed(() => {
  const phase = restartState.value?.phase
  return phase ? t(`settings.config.restartPhase.${phase}`) : ''
})
const restartDeadlineText = computed(() => {
  const at = restartState.value?.wait_deadline
  if (!at) {
    return ''
  }
  const left = at - Math.floor(Date.now() / 1000)
  return t('settings.config.restartGiveUpAt', {
    at: fmtDateTime(at),
    left: left > 0 ? t('settings.config.restartLeft', { d: fmtDuration(left) }) : t('settings.config.restartSoon'),
  })
})

function scheduleRestartPoll(): void {
  if (restartTimer) {
    clearTimeout(restartTimer)
  }
  restartTimer = setTimeout(() => void pollRestart(), 3000)
}

// pollRestart 读重启进度。server 真的重启那几秒请求会失败：记下 unreachable 并继续轮询，
// 等它回来后重新读配置（重启要生效的正是那些启动时读取的项）。
async function pollRestart(): Promise<void> {
  try {
    restart.value = await getServerRestart()
    restartUnreachable.value = false
    if (restartActive.value) {
      scheduleRestartPoll()
      return
    }
    // 重启完成（或失败/取消）：相位不再是进行中，重读配置与令牌能力。
    await loadConfig(true)
    void refreshWhoami()
  } catch {
    restartUnreachable.value = true
    scheduleRestartPoll()
  }
}

async function loadRestart(): Promise<void> {
  try {
    restart.value = await getServerRestart()
    if (restartActive.value) {
      scheduleRestartPoll()
    }
  } catch (e) {
    restartError.value = errorMessage(e)
  }
}

// startRestart 提交重启：confirm 对话框已经写清影响（在跑的 job 等它们结束、新 job 排队、
// 控制台断几秒），wait 是愿意为「等空闲」花多久。
async function startRestart(): Promise<void> {
  restarting.value = true
  restartError.value = ''
  try {
    await restartServer(restartWaitSec.value)
    showRestart.value = false
    await loadRestart()
    scheduleRestartPoll()
  } catch (e) {
    restartError.value = classifyWriteError(e)
  } finally {
    restarting.value = false
  }
}

// cancelRestartWait：停下「等空闲」的重启，停放的 job 立即放行；已进入重启则 409。
async function cancelRestartWait(): Promise<void> {
  restartCancelling.value = true
  restartCancelError.value = ''
  try {
    await cancelServerRestart()
    scheduleRestartPoll()
  } catch (e) {
    restartCancelError.value = classifyWriteError(e)
  } finally {
    restartCancelling.value = false
  }
}

function classifyWriteError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) {
      return t('settings.config.errForbidden')
    }
    if (e.status === 400) {
      return t('settings.config.errInvalid', { err: e.detail || e.message })
    }
    if (e.status === 404) {
      return t('settings.config.errMissing', { err: e.detail || e.message })
    }
    if (e.status === 503) {
      return t('settings.config.errDisabled', { err: e.detail || e.message })
    }
  }
  return e instanceof Error ? e.message : String(e)
}

// Q3：配置随 `meta` 主题（热重载 / 控制台写入）失效重拉；WS 断开超过 15s 才由 30s 兜底轮询接手。
// 编辑弹窗打开时跳过刷新：刷新会把表单底下的"当前值"换掉（还没保存就漂移）。
const liveMeta = createLiveTopic('meta', {
  initial: false,
  fetch: () => {
    if (editor.value === null) return loadConfig(true)
  },
})

onMounted(() => {
  void loadConfig()
  void loadRestart()
  liveMeta.start()
})

onUnmounted(() => {
  liveMeta.stop()
  if (restartTimer) {
    clearTimeout(restartTimer)
  }
})
</script>

<template>
  <div class="config-page">
    <!-- WEB-12：页面级标题与宽度交给 /settings 外壳（SettingsLayout），这里只留一条工具条
         ——「重新读取文件」让 server 重新读盘上的 config.yaml，与下面「刷新」只重读视图不同。
         两个按钮都在最右、彼此紧挨（刷新指示点跟在后）；图标走 ui/Icon（lucide）。 -->
    <div class="toolbar">
      <button v-if="can('config.write')" class="mini-btn mono" type="button" :disabled="loading" data-test="cfg-reload" @click="reloadNow()">
        <Icon :icon="RefreshCw" size="sm" />
        {{ t('settings.config.reloadFile') }}
      </button>
      <!-- gofer-4uk5：有些项只有重启才生效；非受管启动时按钮灰显并说明原因。
           点按钮只打开二次确认，真正提交在确认框里的「确认重启」（不会直接调接口）。 -->
      <button
        v-if="can('config.write')"
        class="mini-btn mono"
        type="button"
        data-test="cfg-restart"
        :disabled="!restartManaged || restartActive || restarting"
        :title="restartManaged ? t('settings.config.restartHint') : t('settings.config.restartUnmanaged')"
        @click="showRestart = true; restartError = ''"
      >
        <Icon :icon="Power" size="sm" />
        {{ restartActive ? t('settings.config.restarting') : t('settings.config.restartServer') }}
      </button>
      <span class="poll mono" :class="{ 'poll--on': loading }">●</span>
    </div>

    <p v-if="!restartManaged && can('config.write')" class="hint mono" data-test="cfg-restart-unmanaged">
      {{ t('settings.config.restartUnmanaged') }}
    </p>
    <p v-if="restartError" class="error mono">{{ restartError }}</p>

    <!-- 重启进度：等空闲时列出在等谁、停住了多少，可随时取消。 -->
    <div v-if="restartState" class="panel restart-panel" data-test="cfg-restart-state">
      <h3 class="panel-title mono">
        {{ t('settings.config.restartLast') }}
        <span class="flag" :class="restartState.phase === 'failed' ? 'flag--no' : 'flag--yes'">{{ restartPhaseText }}</span>
      </h3>
      <p v-if="restartUnreachable && restartActive" class="hint mono">{{ t('settings.config.restartUnreachable') }}</p>
      <p v-if="restartState.error" class="error mono">{{ restartState.error }}</p>
      <template v-if="restartWaiting">
        <p class="hint mono">
          {{ t('settings.config.restartWaiting', { n: restartWaitingRows.length, paused: restartState.paused ?? 0 }) }}
        </p>
        <ul class="mini-list">
          <li v-for="j in restartWaitingRows" :key="j.id" class="mini-row mono">
            <RouterLink class="item-main" :to="`/jobs/${j.id}`">{{ j.title || j.id }}</RouterLink>
            <span class="muted">{{ j.runner || '' }}</span>
            <span class="muted">{{ j.status }}</span>
          </li>
        </ul>
        <p v-if="restartState.wait_deadline" class="hint mono">{{ restartDeadlineText }}</p>
        <p class="hint mono">{{ t('settings.config.restartCancelHint') }}</p>
        <button
          class="mini-btn mono"
          type="button"
          data-test="cfg-restart-cancel"
          :disabled="restartCancelling"
          @click="cancelRestartWait()"
        >
          {{ restartCancelling ? t('settings.config.restartCancelling') : t('settings.config.restartCancelWait') }}
        </button>
        <p v-if="restartCancelError" class="error mono">{{ restartCancelError }}</p>
      </template>
    </div>

    <p v-if="loadError" class="error mono">{{ loadError }}</p>
    <p v-else-if="loading && !loaded" class="placeholder mono">{{ t('settings.config.loadingConfig') }}</p>
    <div v-if="config?.warnings?.length" class="warn-banner mono" role="alert">
      <strong>{{ t('settings.config.warnings') }}</strong>
      <span v-for="(w, i) in config.warnings" :key="i" class="warn-line">{{ w }}</span>
    </div>
    <p v-if="notice" class="notice mono">{{ notice }}</p>
    <p v-if="writeError" class="error mono">{{ writeError }}</p>

    <template v-if="config">
      <section class="section" :aria-label="t('settings.config.overviewTitle')">
        <div class="section-head">
          <h2 class="section-title mono">{{ t('settings.config.overviewTitle') }}</h2>
          <button class="mini-btn mono" type="button" :disabled="loading" @click="loadConfig()">
            {{ loading ? t('settings.config.refreshing') : t('settings.config.refresh') }}
          </button>
        </div>
        <p v-if="sourceGroups.length" class="sources mono">
          {{ t('settings.config.sources') }}
          <span v-for="g in sourceGroups" :key="g.file" class="source">{{ g.file }}（{{ g.sections.join(t('util.sep.list')) }}）</span>
        </p>

        <div class="overview-grid">
          <article class="panel">
            <h3 class="panel-title mono">
              {{ t('settings.config.server') }}
              <button v-if="can('config.write')" class="mini-btn mono inline-btn" type="button" @click="openServerEditor()">{{ t('settings.config.edit') }}</button>
            </h3>
            <dl class="kv mono">
              <dt>addr</dt><dd>{{ config.server.addr || '-' }}</dd>
              <dt>path_view</dt><dd>{{ config.server.path_view || '-' }}</dd>
              <dt>web_enabled</dt><dd><span class="flag" :class="config.server.web_enabled ? 'flag--yes' : 'flag--no'">{{ yesNo(config.server.web_enabled) }}</span></dd>
              <dt>server token</dt><dd><span class="flag" :class="config.server.token_set ? 'flag--yes' : 'flag--no'">{{ setText(config.server.token_set) }}</span></dd>
              <dt>allow_empty_token</dt><dd><span class="flag" :class="config.server.allow_empty_token ? 'flag--warn' : 'flag--no'">{{ yesNo(config.server.allow_empty_token) }}</span></dd>
              <dt>max_job_timeout_sec</dt><dd>{{ config.server.max_job_timeout_sec || '-' }}</dd>
              <dt>stall_timeout_sec</dt><dd>{{ config.server.stall_timeout_sec ?? t('settings.config.default') }}</dd>
              <dt>auto_resume_max</dt><dd>{{ config.server.auto_resume_max ?? t('settings.config.default') }}</dd>
              <dt>skills</dt><dd>{{ joinOrDash(config.server.skills) }}</dd>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">{{ t('settings.config.governance') }}</h3>
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
            <h3 class="panel-title mono">{{ t('settings.config.storage') }}</h3>
            <dl class="kv mono">
              <dt>root</dt><dd>{{ config.storage.root || '-' }}</dd>
              <dt>db_path</dt><dd>{{ config.storage.db_path || '-' }}</dd>
              <dt>exchange</dt><dd>{{ config.storage.default_exchange_subdir || '-' }}</dd>
              <dt>result</dt><dd>{{ config.storage.default_result_subdir || '-' }}</dd>
              <dt>retention</dt><dd>{{ config.storage.retention.max_age_days || '-' }}d / {{ config.storage.retention.max_count || '-' }}</dd>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">{{ t('settings.config.leader') }}</h3>
            <dl class="kv mono">
              <template v-if="leaderRows(config.leader)">
                <template v-for="[k, v] in leaderRows(config.leader)!" :key="k">
                  <dt>{{ k }}</dt><dd>{{ v }}</dd>
                </template>
              </template>
              <template v-else><dt>leader</dt><dd>{{ t('settings.config.leaderUnset') }}</dd></template>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">{{ t('settings.config.schedule') }}</h3>
            <dl class="kv mono">
              <dt>schedule sweep</dt><dd>{{ config.schedule.sweep_interval_sec }}s</dd>
              <dt>runner probe</dt><dd>{{ config.server.runner_probe.interval_seconds }}s / {{ config.server.runner_probe.timeout_seconds }}s</dd>
            </dl>
          </article>
        </div>

        <div class="block">
          <h3 class="block-title mono">{{ t('settings.config.callers') }}</h3>
          <div class="table-wrap">
            <table class="table mono">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>{{ t('settings.config.col.name') }}</th>
                  <th>token</th>
                  <th>{{ t('settings.config.col.canAnswer') }}</th>
                  <th>{{ t('settings.config.col.canAdmin') }}</th>
                  <th>{{ t('settings.config.col.quota') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in callers" :key="c.id">
                  <td>{{ c.id }}</td>
                  <td>{{ c.name || '-' }}</td>
                  <td><span class="flag" :class="c.token_set ? 'flag--yes' : 'flag--no'">{{ setText(c.token_set) }}</span></td>
                  <td><span class="flag" :class="c.can_answer ? 'flag--yes' : 'flag--no'">{{ yesNo(c.can_answer) }}</span></td>
                  <td><span class="flag" :class="c.can_admin ? 'flag--yes' : 'flag--no'">{{ yesNo(c.can_admin) }}</span></td>
                  <td>{{ c.max_concurrent_jobs || '-' }} / {{ c.rate_limit || '-' }} / {{ c.rate_burst || '-' }}</td>
                </tr>
                <tr v-if="callers.length === 0"><td colspan="6">{{ t('settings.config.noCallers') }}</td></tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="cards-3">
          <article class="panel">
            <h3 class="panel-title mono">
              {{ PAGE.agents }}
              <button v-if="can('config.write')" class="mini-btn mono inline-btn" type="button" @click="openAgentEditor(null)">{{ t('settings.config.addAgent') }}</button>
            </h3>
            <ul class="mini-list">
              <li v-for="a in agents" :key="a.key" class="mini-row mono">
                <span class="item-main">{{ a.key }} · {{ a.type }}</span>
                <span v-if="a.command" class="muted">{{ a.command }}</span>
                <span v-if="a.injected" class="tag tag--builtin">{{ t('settings.config.builtin') }}</span>
                <span
                  v-else-if="a.extends"
                  class="tag tag--builtin"
                  :title="t('settings.config.extendsTitle', { base: extendsBase(a.extends), list: (a.overrides ?? []).join(', ') || t('settings.config.none') })"
                  >{{ extendsLabel(a.extends) }}</span
                >
                <span v-for="k in a.env_keys" :key="`${a.key}:${k}`" class="tag">{{ k }}</span>
                <span v-for="s in a.skills" :key="`${a.key}:skill:${s}`" class="tag tag--skill" :title="t('settings.config.skillTagTitle')">{{ s }}</span>
                <span v-for="m in a.mcp ?? []" :key="`${a.key}:mcp:${m}`" class="tag tag--skill" :title="t('settings.config.mcpTagTitle')">mcp:{{ m }}</span>
                <span class="row-actions">
                  <button v-if="can('config.write')" class="mini-btn mono" type="button" @click="openAgentEditor(a)">{{ t('settings.config.edit') }}</button>
                  <button v-if="can('config.write')" class="mini-btn mono" type="button" @click="removeAgent(a)">{{ t('settings.config.delete') }}</button>
                </span>
              </li>
              <li v-if="agents.length === 0" class="empty mono">{{ t('settings.config.noAgents') }}</li>
            </ul>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">{{ PAGE.runners }}</h3>
            <ul class="mini-list">
              <li v-for="r in runners" :key="r.key" class="mini-row mono">
                <span class="item-main">{{ r.key }} · {{ r.type }}</span>
                <span v-if="r.base_url" class="muted">{{ r.base_url }}</span>
                <span class="flag" :class="r.token_set ? 'flag--yes' : 'flag--no'">{{ setText(r.token_set) }}</span>
              </li>
              <li v-if="runners.length === 0" class="empty mono">{{ t('settings.config.noRunners') }}</li>
            </ul>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">{{ t('settings.config.rolesWorkers') }}</h3>
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
              <li v-if="roles.length === 0 && workers.length === 0" class="empty mono">{{ t('settings.config.noRoles') }}</li>
            </ul>
          </article>
        </div>

        <!-- gofer-4uk5：其余全部配置段（只读，按分组折叠）。值来自 GET /v1/config；
             secret / 代理 URL 类字段服务端只给「已配置 / 未配置」。 -->
        <details v-for="sec in extraSections" :key="sec.key" class="panel cfg-group" :data-test="`cfg-group-${sec.key}`">
          <summary class="cfg-group-head mono">
            <Icon :icon="ChevronRight" size="sm" class="cfg-caret" />
            <span class="cfg-group-title">{{ sec.title }}</span>
            <span class="tag tag--builtin">{{ t('settings.config.readonly') }}</span>
          </summary>
          <dl v-if="sec.rows.length > 0" class="kv mono">
            <template v-for="row in sec.rows" :key="row.label">
              <dt>{{ row.label }}</dt>
              <dd>
                <span v-if="row.set !== undefined" class="flag" :class="row.set ? 'flag--yes' : 'flag--no'">{{ row.value }}</span>
                <span v-else>{{ row.value }}</span>
              </dd>
            </template>
          </dl>
          <div v-for="item in sec.items ?? []" :key="item.label" class="cfg-item">
            <p class="cfg-item-head mono">{{ item.label }}</p>
            <dl class="kv mono">
              <template v-for="row in item.rows" :key="`${item.label}:${row.label}`">
                <dt>{{ row.label }}</dt>
                <dd>
                  <span v-if="row.set !== undefined" class="flag" :class="row.set ? 'flag--yes' : 'flag--no'">{{ row.value }}</span>
                  <span v-else>{{ row.value }}</span>
                </dd>
              </template>
            </dl>
          </div>
        </details>
      </section>
    </template>

    <!-- gofer-4uk5：重启 server 的二次确认——写清影响，并让操作人选择愿意为「等空闲」花多久。 -->
    <div v-if="showRestart" class="scrim" data-test="cfg-restart-dialog" @click.self="showRestart = false">
      <div class="modal modal--narrow" role="dialog" aria-modal="true">
        <div class="modal-head">
          <h2 class="modal-title mono">{{ t('settings.config.restartConfirmTitle') }}</h2>
          <button class="mini-btn mono" type="button" @click="showRestart = false">{{ t('settings.config.close') }}</button>
        </div>
        <div class="modal-body">
          <p class="hint mono">{{ t('settings.config.restartImpact') }}</p>
          <label class="field">
            <span class="field-name">{{ t('settings.config.restartWait') }}</span>
            <select v-model.number="restartWaitSec" class="input" data-test="cfg-restart-wait">
              <option :value="1800">{{ t('settings.config.restartWaitOpt', { min: 30 }) }}</option>
              <option :value="3600">{{ t('settings.config.restartWaitOpt', { min: 60 }) }}</option>
              <option :value="7200">{{ t('settings.config.restartWaitOpt', { min: 120 }) }}</option>
              <option :value="14400">{{ t('settings.config.restartWaitOpt', { min: 240 }) }}</option>
            </select>
          </label>
          <p class="hint mono">{{ t('settings.config.restartHint') }}</p>
          <p v-if="restartError" class="error mono">{{ restartError }}</p>
          <div class="form-actions">
            <button class="mini-btn mono" type="button" data-test="cfg-restart-confirm" :disabled="restarting" @click="startRestart()">
              {{ restarting ? t('settings.config.restarting') : t('settings.config.restartSubmit') }}
            </button>
            <!-- 取消：只关掉确认框，不调接口（在跑的任务不受影响）。 -->
            <button class="mini-btn mono" type="button" data-test="cfg-restart-dismiss" :disabled="restarting" @click="showRestart = false">
              {{ t('act.cancel') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="isEdit" class="scrim" @click.self="closeEditor()">
      <div class="modal" role="dialog" aria-modal="true">
        <div class="modal-head">
          <h2 class="modal-title mono">{{ isServer ? t('settings.config.serverEditorTitle') : agentTitle }}</h2>
          <span v-if="validating" class="muted mono">{{ t('settings.config.validating') }}</span>
          <button class="mini-btn mono" type="button" @click="closeEditor()">{{ t('settings.config.close') }}</button>
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
                <span class="field-name">{{ t('settings.config.f.args') }}</span>
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
                <span class="field-name">{{ t('settings.config.f.interactive') }}</span>
              </label>
              <label v-if="agentForm.interactiveMode" class="field">
                <span class="field-name">{{ t('settings.config.f.interactiveArgs') }}</span>
                <textarea
                  v-model="agentForm.interactiveArgsText"
                  class="input textarea"
                  :class="{ 'input--bad': fieldBad('interactive_args') }"
                  rows="3"
                  @change="refreshPreview()"
                ></textarea>
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.sessionCapture') }}</span>
                <input
                  v-model="agentForm.sessionCapture"
                  class="input"
                  :class="{ 'input--bad': fieldBad('session_capture') }"
                  @change="refreshPreview()"
                />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.sessionInject') }}</span>
                <textarea v-model="agentForm.sessionInjectText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label v-if="agentForm.interactiveMode" class="field">
                <span class="field-name">{{ t('settings.config.f.exitKeys') }}</span>
                <textarea v-model="agentForm.exitKeysText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label v-if="agentForm.interactiveMode" class="field">
                <span class="field-name">{{ t('settings.config.f.exitGrace') }}</span>
                <input v-model="agentForm.exitGraceSec" class="input" :class="{ 'input--bad': fieldBad('exit_grace_sec') }" @change="refreshPreview()" />
              </label>
              <label v-if="agentForm.interactiveMode" class="field">
                <span class="field-name">{{ t('settings.config.f.sessionStoreGlob') }}</span>
                <input v-model="agentForm.sessionStoreGlob" class="input" @change="refreshPreview()" />
              </label>
              <label v-if="agentForm.interactiveMode" class="field">
                <span class="field-name">{{ t('settings.config.f.sessionStoreId') }}</span>
                <input v-model="agentForm.sessionStoreIDRegex" class="input" :class="{ 'input--bad': fieldBad('session_store_id_regex') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.sessionResume') }}</span>
                <textarea v-model="agentForm.sessionResumeText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.maxConcurrent') }}</span>
                <input v-model="agentForm.maxConcurrent" class="input" :class="{ 'input--bad': fieldBad('max_concurrent') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.stallInherit') }}</span>
                <input v-model="agentForm.stallTimeoutSec" class="input" :class="{ 'input--bad': fieldBad('stall_timeout_sec') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.fallbackAgents') }}</span>
                <textarea v-model="agentForm.fallbackAgentsText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label class="field">
                <span class="field-name">output_format</span>
                <input v-model="agentForm.outputFormat" class="input" :class="{ 'input--bad': fieldBad('output_format') }" placeholder="text / ndjson" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.ndjsonUsagePath') }}</span>
                <input v-model="agentForm.ndjsonUsagePath" class="input" :class="{ 'input--bad': fieldBad('ndjson_usage_path') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">transcript_dialect（claude / codex / omp / generic）</span>
                <input v-model="agentForm.transcriptDialect" class="input" :class="{ 'input--bad': fieldBad('transcript_dialect') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.injectProcess') }}</span>
                <textarea v-model="agentForm.injectProcessText" class="input textarea" rows="2" :class="{ 'input--bad': fieldBad('inject_process') }" @change="refreshPreview()"></textarea>
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.sessionFamily') }}</span>
                <input v-model="agentForm.sessionFamily" class="input" :class="{ 'input--bad': fieldBad('session_family') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.deliverCommand') }}</span>
                <textarea v-model="agentForm.deliverCommandText" class="input textarea" rows="2" :class="{ 'input--bad': fieldBad('deliver_command') }" @change="refreshPreview()"></textarea>
              </label>
              <label class="field field--check">
                <input v-model="agentForm.deliverStdin" type="checkbox" @change="refreshPreview()" />
                <span class="field-name">{{ t('settings.config.f.deliverStdin') }}</span>
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.deliverOfflineMatch') }}</span>
                <input v-model="agentForm.deliverOfflineMatch" class="input" :class="{ 'input--bad': fieldBad('deliver_offline_match') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.retryMax') }}</span>
                <input v-model="agentForm.retryMaxAttempts" class="input" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.retryBackoff') }}</span>
                <textarea v-model="agentForm.retryBackoffText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label v-if="agentForm.type === 'acp-agent'" class="field">
                <span class="field-name">acp.permission_policy</span>
                <input v-model="agentForm.acpPermissionPolicy" class="input" :class="{ 'input--bad': fieldBad('acp') }" placeholder="auto_allow / ask / strict" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.skillsAgent') }}</span>
                <textarea
                  v-model="agentForm.skillsText"
                  class="input textarea"
                  :class="{ 'input--bad': fieldBad('skills') }"
                  rows="3"
                  placeholder="windows-apply-patch"
                  @change="refreshPreview()"
                ></textarea>
              </label>
              <p class="hint mono">
                {{ t('settings.config.hintSkillsAgent') }}
                <RouterLink to="/skills">{{ t('settings.config.skillsLibrary') }}</RouterLink>{{ t('settings.config.hintSkillsAgentTail') }}
              </p>

              <label v-if="agentPolicy['rules']?.editable" class="field">
                <span class="field-name">{{ t('settings.config.f.rulesAgent') }}</span>
                <textarea
                  v-model="agentForm.rulesText"
                  class="input textarea"
                  :class="{ 'input--bad': fieldBad('rules') }"
                  rows="3"
                  placeholder="windows-host"
                  @change="refreshPreview()"
                ></textarea>
              </label>
              <p v-if="agentPolicy['rules']?.editable" class="hint mono">
                {{ t('settings.config.hintRulesAgentHead') }}<b>{{ t('settings.config.hintRulesAgentStrong') }}</b>{{ t('settings.config.hintRulesAgentTail') }}
                <RouterLink to="/settings/rules">{{ t('settings.config.rulesLibrary') }}</RouterLink>{{ t('settings.config.hintRulesAgentEnd') }}
              </p>

              <template v-if="agentPolicy['mcp']?.editable">
                <label class="field">
                  <span class="field-name">{{ t('settings.config.f.mcpAgent') }}</span>
                  <textarea
                    v-model="agentForm.mcpText"
                    class="input textarea"
                    :class="{ 'input--bad': fieldBad('mcp') }"
                    rows="2"
                    placeholder="docs-search"
                    @change="refreshPreview()"
                  ></textarea>
                </label>
                <label class="field">
                  <span class="field-name">{{ t('settings.config.f.mcpFormat') }}</span>
                  <select v-model="agentForm.mcpFormat" class="input" :class="{ 'input--bad': fieldBad('mcp_format') }" @change="refreshPreview()">
                    <option value="">{{ t('settings.config.mcpFormat.none') }}</option>
                    <option value="claude-json">{{ t('settings.config.mcpFormat.claudeJson') }}</option>
                    <option value="codex-config">{{ t('settings.config.mcpFormat.codexConfig') }}</option>
                    <option value="acp">{{ t('settings.config.mcpFormat.acp') }}</option>
                  </select>
                </label>
                <label class="field">
                  <span class="field-name">{{ t('settings.config.f.mcpArgs') }}</span>
                  <textarea
                    v-model="agentForm.mcpArgsText"
                    class="input textarea"
                    :class="{ 'input--bad': fieldBad('mcp_args') }"
                    rows="2"
                    placeholder="--mcp-config"
                    @change="refreshPreview()"
                  ></textarea>
                </label>
                <p class="hint mono">{{ t('settings.config.hintMcpHead') }}<RouterLink to="/settings/mcp">{{ t('settings.config.mcpLink') }}</RouterLink>{{ t('settings.config.hintMcpTail') }}</p>
              </template>

              <p v-if="editing" class="hint mono">
                {{ t('settings.config.hintPreserved', { env: joinOrDash(editing.env_keys), detect: editing.detect.command || '-', mcp: editing.mcp_server_name || '-' }) }}
                {{ t('settings.config.hintPreservedSecret') }}
              </p>
              <p v-else class="hint mono">
                {{ t('settings.config.hintNewAgent') }}
                {{ t('settings.config.hintNewAgentTail') }}
              </p>
            </template>

            <template v-else>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.maxJobTimeout') }}</span>
                <input v-model="serverForm.maxJobTimeoutSec" class="input" :class="{ 'input--bad': fieldBad('max_job_timeout_sec') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.stallServer') }}</span>
                <input v-model="serverForm.stallTimeoutSec" class="input" :class="{ 'input--bad': fieldBad('stall_timeout_sec') }" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.autoResume') }}</span>
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
                <span class="field-name">retry.max_attempts{{ t('settings.config.f.retryMaxServer') }}</span>
                <input v-model="serverForm.retryMaxAttempts" class="input" @change="refreshPreview()" />
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.retryBackoff') }}</span>
                <textarea v-model="serverForm.retryBackoffText" class="input textarea" rows="2" @change="refreshPreview()"></textarea>
              </label>
              <label class="field">
                <span class="field-name">{{ t('settings.config.f.skillsServer') }}</span>
                <textarea
                  v-model="serverForm.skillsText"
                  class="input textarea"
                  :class="{ 'input--bad': fieldBad('skills') }"
                  rows="3"
                  placeholder="house-rules"
                  @change="refreshPreview()"
                ></textarea>
              </label>
              <p class="hint mono">
                {{ t('settings.config.hintSkillsServerHead') }}<RouterLink to="/skills">{{ t('settings.config.skillsLibrary') }}</RouterLink>{{ t('settings.config.hintSkillsServerTail') }}
                {{ t('settings.config.hintSkillsServerEnd') }}
              </p>

              <label v-if="serverPolicy['rules']?.editable" class="field">
                <span class="field-name">{{ t('settings.config.f.rulesServer') }}</span>
                <textarea
                  v-model="serverForm.rulesText"
                  class="input textarea"
                  :class="{ 'input--bad': fieldBad('rules') }"
                  rows="3"
                  placeholder="house-rules"
                  @change="refreshPreview()"
                ></textarea>
              </label>
              <p v-if="serverPolicy['rules']?.editable" class="hint mono">
                {{ t('settings.config.hintRulesServerHead') }}<RouterLink to="/settings/rules">{{ t('settings.config.rulesLibrary') }}</RouterLink>{{ t('settings.config.hintRulesServerTail') }}
                <b>{{ t('settings.config.hintRulesServerStrong') }}</b>{{ t('settings.config.hintRulesServerSecret') }}
              </p>
              <div class="field">
                <span class="field-name">{{ t('settings.config.f.notification') }}</span>
                <p class="hint">
                  {{ t('settings.config.hintNotificationMoved') }}
                  <RouterLink to="/settings/notifications">{{ t('settings.config.notificationMovedLink') }}</RouterLink>{{ t('settings.config.hintNotificationMovedTail') }}
                </p>
              </div>
              <div class="field">
                <span class="field-name">{{ t('settings.config.f.eventHooks') }}</span>
                <ul class="mini-list">
                  <li v-for="h in eventHooks" :key="h.name" class="mini-row mono">
                    <span class="item-main">{{ h.name }}</span>
                    <span class="muted">{{ h.command.join(' ') }}</span>
                    <span class="flag" :class="h.enabled ? 'flag--yes' : 'flag--no'">{{ h.enabled ? t('settings.config.hookOn') : t('settings.config.hookOff') }}</span>
                    <span class="muted">events {{ h.events.length ? h.events.join(', ') : t('settings.config.defaultSet') }} · projects {{ h.projects.length ? h.projects.join(', ') : t('settings.config.allProjects') }}</span>
                    <span class="muted">{{ t('settings.config.hookTimeout', { sec: h.timeout_sec, n: h.max_attempts }) }}{{ h.dir ? ' · dir ' + h.dir : '' }}</span>
                    <span v-for="k in h.env_keys" :key="`${h.name}:${k}`" class="tag">{{ k }}</span>
                  </li>
                  <li v-if="eventHooks.length === 0" class="empty mono">{{ t('settings.config.noEventHooks') }}</li>
                </ul>
              </div>
              <div class="field">
                <span class="field-name">{{ t('settings.config.f.decisionHooks') }}<ExperimentalBadge /></span>
                <ul class="mini-list">
                  <li v-for="h in decisionHooks" :key="h.name" class="mini-row mono">
                    <span class="item-main">{{ h.name }}</span>
                    <span class="muted">{{ h.command.join(' ') }}</span>
                    <span class="flag" :class="h.enabled ? 'flag--yes' : 'flag--no'">{{ h.enabled ? t('settings.config.hookOn') : t('settings.config.hookOff') }}</span>
                    <span class="muted">points {{ h.points.join(', ') }} · projects {{ h.projects.length ? h.projects.join(', ') : t('settings.config.allProjects') }}</span>
                    <span class="muted">{{ t('settings.config.hookDecisionTimeout', { sec: h.timeout_sec, onError: h.on_error === 'allow' ? t('settings.config.hookAllow') : t('settings.config.hookDeny') }) }}{{ h.include_internal ? t('settings.config.hookIncludesInternal') : '' }}{{ h.dir ? ' · dir ' + h.dir : '' }}</span>
                    <span v-for="k in h.env_keys" :key="`${h.name}:${k}`" class="tag">{{ k }}</span>
                  </li>
                  <li v-if="decisionHooks.length === 0" class="empty mono">{{ t('settings.config.noDecisionHooks') }}</li>
                </ul>
              </div>
              <p class="hint mono">
                {{ t('settings.config.hintHooksHead') }} <code>gofer serve reload</code> {{ t('settings.config.hintHooksMid') }}
                {{ t('settings.config.hintHooksTail') }}
              </p>
              <p class="hint mono">
                {{ t('settings.config.hintPatchHead') }}
                {{ t('settings.config.hintPatchTail') }}
              </p>
              <div v-if="restartOnly.length > 0" class="field">
                <span class="field-name">{{ t('settings.config.f.restartOnly') }}</span>
                <div class="badges">
                  <span v-for="name in restartOnly" :key="name" class="badge mono">{{ t('settings.config.restartBadge', { name }) }}</span>
                </div>
              </div>
              <p class="hint mono">
                {{ t('settings.config.hintRestartHead') }} <code>gofer serve restart</code>{{ t('settings.config.hintRestartMid') }}
                {{ t('settings.config.hintRestartTail') }}
              </p>
            </template>

            <div class="form-actions">
              <button class="mini-btn mono" type="submit" :disabled="saving">
                {{ saving ? t('settings.config.saving') : t('settings.config.save') }}
              </button>
              <span v-if="previewRestart.length > 0" class="impact mono">
                {{ t('settings.config.impactRestart', { list: previewRestart.join(', ') }) }}
              </span>
              <span v-else class="impact mono">{{ t('settings.config.impactImmediate') }}</span>
            </div>
            <p v-if="formError" class="error mono">{{ formError }}</p>
          </form>

          <div class="preview">
            <div class="preview-head mono">
              <span>{{ t('settings.config.yamlPreview') }}</span>
            </div>
            <pre class="preview-body mono">{{ preview || t('settings.config.yamlPreviewEmpty') }}</pre>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.config-page {
  min-width: 0;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  margin-bottom: 14px;
}
/* 工具条里的按钮贴右排列：「重启 server」紧跟「重新读取文件」。.mini-btn 自带的
   margin-left: auto 会把第一个按钮推到中间，这里复位。 */
.toolbar .mini-btn {
  margin-left: 0;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.section-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
}
.poll {
  color: var(--line);
  font-size: 10px;
}
.poll--on {
  color: var(--accent);
}
.section {
  margin-bottom: 20px;
}
.sources {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  margin: 0 0 12px;
  font-size: 12px;
  color: var(--ink-3);
}

.section-title,
.panel-title,
.block-title,
.modal-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--ink-3);
  margin: 0;
}
.section-title {
  color: var(--ink);
  font-size: 14px;
}
.mini-btn {
  margin-left: auto;
  background: transparent;
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--accent);
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
/* gofer-4uk5：完整配置的只读分组卡片（原生 details，默认收起）。 */
.cfg-group {
  margin-top: 12px;
}
.cfg-group-head {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.cfg-group-head .cfg-group-title {
  flex: 1;
}
/* 展开箭头：折叠时向右，展开时向下（原生 marker 会被 display:flex 隐藏）。 */
.cfg-caret {
  color: var(--ink-3);
  transition: transform 150ms var(--ease);
}
.cfg-group[open] .cfg-caret {
  transform: rotate(90deg);
}
.cfg-group-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--ink);
}
/* 完整配置的行：配置键可能很长（worker_enrollment.per_source_per_min），标签列给宽一点
   并允许换行，不能让值盖在标签上。 */
.cfg-group .kv {
  grid-template-columns: minmax(0, 280px) minmax(0, 1fr);
}
.cfg-group .kv dt {
  word-break: break-word;
}
.cfg-item {
  border-top: 1px dashed var(--line);
  margin-top: 10px;
  padding-top: 8px;
}
.cfg-item-head {
  margin: 0;
  font-size: 12px;
  color: var(--ink);
}
.restart-panel {
  margin-bottom: 14px;
}
.modal--narrow {
  max-width: 520px;
}
.cards-3 {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.panel {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
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
  color: var(--ink-3);
}
.kv dd {
  color: var(--ink);
  margin: 0;
  word-break: break-all;
}
.flag,
.tag {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 7px;
  font-size: 11px;
}
.flag--yes {
  color: var(--done);
  border-color: var(--done);
}
.flag--no {
  color: var(--ink-3);
}
.flag--warn {
  color: var(--run);
  border-color: var(--run);
}
.tag {
  color: var(--accent);
  margin: 4px 4px 0 0;
}
.tag--builtin {
  color: var(--ink-3);
}
/* 技能绑定与 env_keys 同为"附加到这条定义上的东西"，用 done 色区分来源。 */
.tag--skill {
  color: var(--done);
}
.block {
  margin-top: 12px;
  padding: 14px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: var(--surface);
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
  color: var(--ink-3);
  font-weight: 400;
}
.table td {
  color: var(--ink);
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
  color: var(--ink);
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
  color: var(--ink-3);
}
.empty,
.placeholder {
  font-size: 12px;
}
.error,
.notice {
  font-size: 12px;
  border-radius: var(--r-chip);
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
.warn-banner {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0 0 10px;
  color: var(--ink);
  border: 1px solid var(--ink-3);
  word-break: break-word;
}
.warn-line {
  color: var(--ink-3);
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
  background: var(--bg, var(--surface));
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
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
  color: var(--ink-3);
}
.req {
  color: var(--fail);
}
.input {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 5px 8px;
  font-family: inherit;
  font-size: 12px;
}
.input:disabled {
  color: var(--ink-3);
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
  color: var(--ink-3);
  margin: 2px 0 0;
  line-height: 1.6;
}
.hook-row {
  display: flex;
  flex-direction: column;
  gap: 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 10px;
}
.hook-row__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.badges {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 2px;
}
.badge {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
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
  border-radius: var(--r-chip);
  padding: 10px;
  min-height: 240px;
}
.preview-head {
  font-size: 11px;
  color: var(--ink-3);
  margin-bottom: 8px;
}
.preview-body {
  font-size: 11px;
  color: var(--ink);
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
  /* 手机档：配置键比屏幕宽，键值上下堆。 */
  .cfg-group .kv {
    grid-template-columns: 1fr;
  }
  /* 手机档：调用方表格（6 列）横向滚动，不把列挤成一团（wrapper 已是 overflow-x: auto）。 */
  .table-wrap .table {
    min-width: 620px;
  }
}
</style>
