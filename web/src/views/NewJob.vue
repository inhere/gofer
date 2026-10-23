<script setup lang="ts">
// 新建 job 表单（G4，design §6.4 / P3-b）：
//  - project 下拉 -> 联动限定可选 agent / runner（按 project.allowed_*）
//  - agent=cli-agent 显 prompt 文本域（可贴 md）；agent=exec 显 command 输入（空格分词为 cmd[]）
//  - runner=worker -> 二选一：指定 worker_id（connected）或 worker_labels（逗号），默认折叠高级项
//  - cwd（默认 .）/ title / timeout / sync 勾选
// 提交成功跳详情；202（仍在后台）提示后仍跳详情（详情页自有 SSE 续看）。
import { runnerOptionText } from '../utils/runnerDisplay'
import { CEILING_LABEL,
  CEILING_READ_ONLY_NOTICE,
  ceilingAllowsAgent,
  ceilingAllowsExec,
  ceilingAllowsProject,
  ceilingAllowsRunner,
  ceilingDeniedText,
} from '../utils/ceiling'
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  computeRunnerBlocks,
  pickRunner,
  projectRunnerOptions,
  sessionRunnerBlock,
  type BlockNote,
} from '../utils/runnerChoice'
import {
  dispatchWorkItem,
  getJobRequest,
  getWorkItem,
  getTemplate,
  listRepos,
  listTemplates,
  rebuildJob,
  stageXfer,
  submitJob,
} from '../api/client'
import { linkJobAfterSubmit, newJobPrefill, readFromItemQuery, workItemLink } from '../utils/dispatch'
import { useWhoami } from '../store/whoami'
import { getAgentsCached, getMetaCached } from '../api/metaCache'
import { fromSessionFormError, readFromSessionQuery, withFromSession } from '../utils/fromSession'
import { DIR_LOCK_HINT, formatLockPaths, parseLockPaths } from '../utils/lockPaths'
import { defaultProject, rememberProject } from '../ui/projectFilter'
import { t } from '../i18n'
import BackLink from '../ui/BackLink.vue'
import type {
  JobBudget,
  JobUpload,
  MetaAgent,
  MetaProject,
  MetaRunner,
  MetaWorker,
  RebuildBody,
  TemplateInfo,
  TemplatePreview,
  XferPutMeta,
} from '../api/types'

const router = useRouter()
const route = useRoute()

const loading = ref(true)
const loadError = ref('')
const submitting = ref(false)
const submitError = ref('')
const notice = ref('')

const projects = ref<MetaProject[]>([])
const agents = ref<MetaAgent[]>([])
const runners = ref<MetaRunner[]>([])
const workers = ref<MetaWorker[]>([])

// 调用方上限（ceiling，上限）：本人 token 的范围上界，判定语义见 utils/ceiling.ts。
// 没有上限（未配置 / 旧 server）时 ceil 为 null，下面每条判定都放行；ceilingReadOnly 时不可提交。
// （readOnly 这个名字被本页的「只读 job」开关占了，故这里叫 ceilingReadOnly。）
const { holds, ceiling: ceil, readOnly: ceilingReadOnly } = useWhoami()

// 表单字段
const projectKey = ref('')
const agentKey = ref('')
const runnerName = ref('')
const prompt = ref('')
const command = ref('')
// per-job cli-agent flags（xu64.12 §14）：每行一个完整 argv 元素，追加到 agent argv 末尾。
const agentArgs = ref('')
// N1 §B：可选模型（空 = agent 自身默认）；cli-agent 与 acp-agent 都生效，exec 不支持。
const model = ref('')
const cwd = ref('.')
// 目录锁 / worktree / 等锁时长（gofer-jkd2）：与 CLI `job run` 的 --lock / --worktree /
// --lock-wait 一一对应，wire 字段见 SubmitJobReq.lock_paths / worktree / lock_wait_sec。
// 留空 = 不声明锁（服务端仍锁整个 CWD），留空等锁时长 = 用服务端默认。
const lockPaths = ref('')
const lockWaitSec = ref<number | null>(null)
const worktree = ref(false)
const title = ref('')
const tags = ref('')
const timeoutSec = ref<number | null>(null)
const sync = ref(false)
const interactive = ref(false)
const continuousSession = ref(false)
const idleTimeoutSec = ref<number | null>(null)
const maxSessionSec = ref<number | null>(null)
// 只读 job（bd h-aii-0ql3）：审查/分析类任务，agent 不能写文件。
const readOnly = ref(false)
// N2 §B 预算熔断（可选，空 = 不限；越线 job 被终止并标 failure_class=budget）。
const maxTokens = ref<number | null>(null)
const maxCostUsd = ref<number | null>(null)
const maxTurns = ref<number | null>(null)
function budgetFromForm(): JobBudget | undefined {
  const b: JobBudget = {}
  if (maxTokens.value != null && maxTokens.value > 0) b.max_tokens = Math.floor(maxTokens.value)
  if (maxCostUsd.value != null && maxCostUsd.value > 0) b.max_cost_usd = maxCostUsd.value
  if (maxTurns.value != null && maxTurns.value > 0) b.max_turns = Math.floor(maxTurns.value)
  return Object.keys(b).length > 0 ? b : undefined
}
const recordPty = ref(false)
const cols = ref(120)
const rows = ref(32)
const sessionMode = computed(() => route.query.mode === 'session' || route.query.interactive === '1')
const rebuildFrom = computed(() => (typeof route.query.from === 'string' ? route.query.from : ''))
const isRebuild = computed(() => rebuildFrom.value !== '')
// 「从此会话新开」（gofer-ldmp）：?from_session=<id> → 提交带 from_session，新会话继承其上下文。
// 重建走 rebuild 接口（继承源请求），不叠加 from_session。
const fromSession = computed(() => (isRebuild.value ? '' : readFromSessionQuery(route.query.from_session)))
// /v1/agents 的 from_session 能力（meta 不带）；只在带 from_session 时才取。
const agentFromSessionCaps = ref<Record<string, boolean | undefined>>({})
const rebuildRedacted = ref(false)
const planId = ref('')   // 隐藏：rebuild 继承/可覆盖源 plan_id（若已有则复用）
// 标题跟随模式变化（设计稿 §7.1）：新建任务 / 快速重建 / 从会话新开。
const pageTitle = computed(() =>
  isRebuild.value ? t('newjob.title.rebuild') : fromSession.value ? t('newjob.title.fromSession') : t('newjob.title'),
)
const promptLabel = computed(() => {
  if (interactive.value) {
    return t('newjob.prompt.labelInteractive')
  }
  return templateName.value !== '' ? t('newjob.prompt.labelAppend') : t('newjob.prompt.label')
})
const promptPlaceholder = computed(() =>
  interactive.value
    ? t('newjob.prompt.placeholderInteractive')
    : templateName.value !== ''
      ? t('newjob.prompt.placeholderAppend')
      : t('newjob.prompt.placeholder'),
)
const timeoutPlaceholder = computed(() =>
  continuousSession.value
    ? t('newjob.timeout.placeholderSession')
    : interactive.value
      ? t('newjob.timeout.placeholderInteractive')
      : t('newjob.timeout.placeholder'),
)

// 任务书模板（SUP-01 P5）：选中后在服务端渲染正文预览 —— include/head 只有服务端
// 能展开（它才知道 job 会在哪个 checkout 里跑），所以预览由 GET .../templates/{name}?var=
// 返回，前端只负责把变量值送过去并显示。提交只发 template + vars 两个字段。
const templates = ref<TemplateInfo[]>([])
const templateName = ref('')
const templateVars = ref<Record<string, string>>({})
const templatePreview = ref<TemplatePreview | null>(null)
const templateError = ref('')

const selectedTemplate = computed<TemplateInfo | undefined>(() =>
  templates.value.find((t) => t.name === templateName.value),
)
// 变量输入按模板声明的 vars 生成（顺序稳定：按名字排序，表单不会每次重排）。
const templateVarsList = computed<Array<[string, NonNullable<TemplateInfo['vars']>[string]]>>(() => {
  const vars = selectedTemplate.value?.vars ?? {}
  return Object.entries(vars).sort((a, b) => a[0].localeCompare(b[0]))
})
const templateWarnings = computed(() => templatePreview.value?.render.warnings ?? [])
const templateMissing = computed(() => templatePreview.value?.render.missing ?? [])
const templatePrompt = computed(() => templatePreview.value?.render.prompt ?? '')

async function loadTemplates(): Promise<void> {
  templateName.value = ''
  templateVars.value = {}
  templatePreview.value = null
  templateError.value = ''
  templates.value = []
  if (!projectKey.value) {
    return
  }
  try {
    // 解析失败的文件也列出来（带 error），否则"我的模板去哪了"无从回答。
    const r = await listTemplates(projectKey.value)
    templates.value = r.templates ?? []
  } catch (e) {
    templateError.value = e instanceof Error ? e.message : String(e)
  }
}

async function loadTemplatePreview(): Promise<void> {
  templatePreview.value = null
  if (!projectKey.value || !templateName.value) {
    return
  }
  try {
    templatePreview.value = await getTemplate(projectKey.value, templateName.value, templateVars.value)
    templateError.value = ''
  } catch (e) {
    templateError.value = e instanceof Error ? e.message : String(e)
  }
}

// 换模板 = 换一批变量：清空旧值，再按新模板的默认值拉一次预览。
watch(templateName, () => {
  templateVars.value = {}
  void loadTemplatePreview()
})
watch(templateVars, () => void loadTemplatePreview(), { deep: true })
watch(projectKey, () => {
  void loadTemplates()
  void loadLockCandidates()
})

// 可选锁定目录（gofer-jkd2）：复用已有的项目子仓库发现接口（E32）提示项目根下的嵌套 git
// 仓库 —— 顶层目录装多个仓库时，锁子仓库才不会让整个项目根的 job 排队。失败不报错（只是提示）。
const lockCandidates = ref<string[]>([])
async function loadLockCandidates(): Promise<void> {
  lockCandidates.value = []
  if (!projectKey.value) {
    return
  }
  try {
    const r = await listRepos(projectKey.value)
    lockCandidates.value = (r.repos ?? [])
      .map((repo) => repo.rel_path)
      .filter((rel) => rel !== '' && rel !== '.')
      .sort((a, b) => a.localeCompare(b))
  } catch {
    lockCandidates.value = []
  }
}
function addLockPath(path: string): void {
  const list = parseLockPaths(lockPaths.value)
  if (!list.includes(path)) {
    list.push(path)
  }
  lockPaths.value = formatLockPaths(list)
}

// runner=worker 高级项
const advancedOpen = ref(false)
const mobileAdvancedOpen = ref(false)
const mobileViewport = ref(false)
const workerMode = ref<'id' | 'labels'>('id')
const workerId = ref('')
const workerLabels = ref('')

// 快照：预填后记下各标量初值，提交时 diff（只发改动过的字段，N6.1——未编辑字段不回传，
// 占位符没机会回传；服务端继承源真值）。
const baseline = ref<Record<string, unknown>>({})

// env 编辑器（N5）：源 env 每 key 一行，值脱敏、初始 action='keep'（不发）。
type EnvAction = 'keep' | 'set' | 'unset'
const envRows = ref<Array<{ key: string; action: EnvAction; value: string }>>([])
const envAdds = ref<Array<{ key: string; value: string }>>([])
const redactedPlaceholder = '***REDACTED***'
const promptRedacted = computed(() => isRebuild.value && prompt.value.includes(redactedPlaceholder))
const commandRedacted = computed(() => isRebuild.value && command.value.includes(redactedPlaceholder))
const cwdRedacted = computed(() => isRebuild.value && cwd.value.includes(redactedPlaceholder))

async function loadMeta() {
  loading.value = true
  loadError.value = ''
  try {
    const m = await getMetaCached()
    projects.value = m.projects ?? []
    agents.value = m.agents ?? []
    runners.value = m.runners ?? []
    workers.value = m.workers ?? []
    // 默认选中首个 NON-worker_only project（baseline 是 local runner，worker-only 项此时不可选）。
    // 若只有 worker-only 项（无 host project），projectKey 留空、不崩——用户切到 worker 后再选。
    const firstHost = projects.value.find((p) => !p.worker_only)
    if (firstHost) {
      selectProject(firstHost.key)
    }
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const selectedProject = computed<MetaProject | undefined>(() =>
  projects.value.find((p) => p.key === projectKey.value),
)

// 当前 runner（先于 agent 定：agent 候选由「project 白名单 ∩ 执行侧真实能力」共同决定，
// 执行侧 = runner(+worker)，故表单顺序 project → runner → worker → agent）。
const selectedRunner = computed<MetaRunner | undefined>(() =>
  runners.value.find((r) => r.name === runnerName.value),
)
const runnerType = computed(() => selectedRunner.value?.type ?? '')
const isWorkerRunner = computed(() => runnerType.value === 'worker')
const isLocalRunner = computed(() => runnerType.value === 'local')

// connected 的 worker（指定 worker_id 下拉用）
const connectedWorkers = computed<MetaWorker[]>(() =>
  workers.value.filter((w) => w.connected),
)

// runner 在 config 里 pin 的 worker（type=worker）。后端提交时 worker_id 留空即回落到它
// （job/capabilities.go 的 capabilitiesFor + selectTargetWorker，D4），故前端同序回落：
// 有 pin 就无需用户再手选一遍，能力收窄也立刻生效。
const pinnedWorkerId = computed(() =>
  isWorkerRunner.value ? (selectedRunner.value?.worker_id ?? '') : '',
)
const effectiveWorkerId = computed(() => {
  if (!isWorkerRunner.value || workerMode.value !== 'id') {
    return ''
  }
  return workerId.value || pinnedWorkerId.value
})
const selectedWorker = computed<MetaWorker | undefined>(() => {
  const id = effectiveWorkerId.value
  return id === '' ? undefined : workers.value.find((w) => w.id === id)
})

// labels 模式：AND 匹配的在线候选 worker（与后端 selectWorker 的 AND 语义一致）。
const labelCandidates = computed<MetaWorker[]>(() => {
  if (!isWorkerRunner.value || workerMode.value !== 'labels') {
    return []
  }
  const want = parseLabels(workerLabels.value)
  if (want.length === 0) {
    return []
  }
  return workers.value.filter(
    (w) => w.connected && want.every((l) => (w.labels ?? []).includes(l)),
  )
})

// 本次提交可能落到的 worker 集合：id 模式=选定/pin 的那一台；labels 模式=全部候选。
// labels 模式取能力「并集」——只要有一台候选能跑该 agent，后端 selectWorker 就会挑中它。
const targetWorkers = computed<MetaWorker[]>(() => {
  if (!isWorkerRunner.value) {
    return []
  }
  const w = selectedWorker.value
  return w ? [w] : labelCandidates.value
})

// 目标 worker 上报的 agent key 并集：优先 typed agent_caps，回落 bare agents[]。
// 全部未上报能力（离线/旧 worker）返回 null = 无能力视图 → 不收窄，与后端 online=false 时
// 放行、留给派发期失败的语义对齐（job/config.go G2）。
const workerAgentKeys = computed<Set<string> | null>(() => {
  const out = new Set<string>()
  for (const w of targetWorkers.value) {
    const caps = w.agent_caps ?? []
    if (caps.length > 0) {
      for (const c of caps) {
        out.add(c.key)
      }
      continue
    }
    for (const k of w.agents ?? []) {
      out.add(k)
    }
  }
  return out.size > 0 ? out : null
})


// host 已定义的 agent key（meta.agents = server config 的 resolved registry）。
// 交互 job 的准入由 HOST 解析（job/config.go 用 host 的 ResolveAgent 查 interactive/no_raw_cmd），
// 故交互模式下只有 host 也认识的 agent 才可能通过——worker 独有的 agent 只能跑非交互 job。
const hostAgentKeys = computed(() => new Set(agents.value.map((a) => a.key)))

// agent 候选池：host agents ∪ 目标 worker 上报的 agent_caps（worker 独有的 agent 也可提交：
// 远端 job 的 agent 由执行侧解析，host 不做 agent-must-be-known 校验）。
const agentPool = computed<MetaAgent[]>(() => {
  const out = new Map<string, MetaAgent>()
  for (const a of agents.value) {
    out.set(a.key, a)
  }
  for (const w of targetWorkers.value) {
    for (const c of w.agent_caps ?? []) {
      if (!out.has(c.key)) {
        out.set(c.key, { key: c.key, type: c.type ?? '', interactive: c.interactive, batch: c.batch })
      }
    }
  }
  return [...out.values()].sort((a, b) => a.key.localeCompare(b.key))
})

// project.allowed_agents ∩ 执行侧能力 —— 模式(interactive/exec)过滤之前的候选池。
// agentEmptyReason 据此区分「池子本来就空」与「被模式闸掉了」。
const agentCandidates = computed<MetaAgent[]>(() => {
  let list = agentPool.value
  const allowed = selectedProject.value?.allowed_agents ?? []
  if (allowed.length > 0) {
    const set = new Set(allowed)
    list = list.filter((a) => set.has(a.key))
  }
  const wkeys = workerAgentKeys.value
  if (wkeys) {
    list = list.filter((a) => wkeys.has(a.key))
  }
  return list
})

// 项目是否允许交互 job（AGT-02 §2）：只看项目级总开关 allow_interactive。undefined = 旧 server
// 没有该字段（控制台热更、二进制未更新）→ 不当作"闸为假"，交给后端拒（与 allow_exec 的 undefined
// 处理同一条 fail-safe 纪律，宁可让后端拒，也不给出一个空下拉）；未选 project（无从判断）同 true。
// AGT-02 0.3 起不再有按 agent 收窄的第二份名单（旧字段已移除）。
function projectAllowsInteractive(proj: MetaProject | undefined): boolean {
  if (!proj) {
    return true
  }
  return proj.allow_interactive ?? true
}

// 联动：agent 候选 = agentCandidates（allowed_agents ∩ 执行侧能力）
//                  ∩ [interactive: host 已知 & 有交互模式 & 项目 allow_interactive 为真]
//                  ∩ [非 interactive: 排除 interactive-only agent]
//                  ∩ [local runner 且 allow_exec=false: 排除 exec 型]
// 不再做「交集为空就回落不收窄」：那会列出提交必被拒的假选项（原 T5.3b fail-safe 的反效果）。
// 空列表由 agentEmptyReason 说明原因。
const agentOptions = computed<MetaAgent[]>(() => {
  const proj = selectedProject.value
  let list = agentCandidates.value
  if (interactive.value) {
    list = list.filter((a) => a.interactive && hostAgentKeys.value.has(a.key))
    // 项目级总开关（AGT-02 §2）：关掉就一个交互 agent 都不给选。
    if (!projectAllowsInteractive(proj)) {
      list = []
    }
  } else {
    // 反向闸（对齐 job/config.go 的 interactive-only 校验）：interactive agent 是裸启动进
    // pty 的（arg 模板里没有 {{prompt}}），非交互提交会把 prompt 静默丢掉、跑一个空转到超时的
    // 裸 CLI —— 后端已拒，这里就不该让它出现在下拉里（能选但提交必挂 = 最差体验）。
    list = list.filter((a) => a.batch === true || (a.batch === undefined && !a.interactive))
  }
  // exec 安全闸只在 host 执行时由 host 把关（worker/peer 是 remote，各自校验自己的 allow_exec）。
  // 严格比 false：undefined = 旧 server 没这个字段，不能当成"闸为假"把 exec 全藏掉。
  if (isLocalRunner.value && proj?.allow_exec === false) {
    list = list.filter((a) => a.type !== 'exec')
  }
  // 调用方上限（ceiling）：范围外的 agent 不出现（下拉没有"灰显原因"的位置，选中即提交必被拒）；
  // exec 类还要 allow_exec（上限没写 allow_exec 就是不许可，与后端的严格默认一致）。
  list = list.filter((a) => ceilingAllowsAgent(ceil.value, a.key))
  if (!ceilingAllowsExec(ceil.value)) {
    list = list.filter((a) => a.type !== 'exec')
  }
  return list
})

// 被上限挡下的 agent 数：用来说明空下拉是上限所致，不让人去怀疑 project / worker。
const agentCeilingBlocked = computed(
  () =>
    agentCandidates.value.filter(
      (a) => !ceilingAllowsAgent(ceil.value, a.key) || (!ceilingAllowsExec(ceil.value) && a.type === 'exec'),
    ).length,
)

// 空候选的原因（明示 > 静默回落）：按收窄顺序给出第一个成立的解释。
const agentEmptyReason = computed<string>(() => {
  if (agentOptions.value.length > 0 || !selectedProject.value) {
    return ''
  }
  // 上限先在池子那一层就挡掉了候选，故它是最根本的一层原因。
  if (agentCeilingBlocked.value > 0) {
    return `${CEILING_LABEL}：project ${selectedProject.value.key} 下可用的 agent 不在你的范围内`
  }
  const proj = selectedProject.value
  if (interactive.value) {
    if (proj.worker_only) {
      return `project ${proj.key} 是 worker-only：交互 job 由 host 校验准入，暂不支持`
    }
    if (!projectAllowsInteractive(proj)) {
      return `project ${proj.key} 未开启交互 job（allow_interactive）`
    }
    return `project ${proj.key} 没有可用的交互 agent（host 未安装有交互模式的 agent，或当前 runner/worker 上不可达）`
  }
  // 非交互模式：候选池非空、但全被 interactive-only 闸掉 —— 指路而不是让用户对着空下拉发呆。
  if (agentCandidates.value.length > 0 && agentCandidates.value.every((a) => a.interactive)) {
    return `project ${proj.key} 当前可选的 agent 全是交互式（interactive-only，非交互提交会被拒）：请打开「交互模式」`
  }
  if (workerAgentKeys.value) {
    const where =
      targetWorkers.value.length === 1
        ? `worker ${targetWorkers.value[0].id}`
        : `${targetWorkers.value.length} 台候选 worker`
    return `${where} 上没有 project ${proj.key} 允许的 agent`
  }
  if (isLocalRunner.value && proj.allow_exec === false) {
    return `project ${proj.key} 的 allowed_agents 在本机无可用项（allow_exec=false，exec 已排除）`
  }
  return `project ${proj.key} 无可用 agent`
})

const runnerOptions = computed<MetaRunner[]>(() =>
  projectRunnerOptions(selectedProject.value, runners.value),
)

// D5：选定 project 后按执行侧能力判定每个 runner 能否跑它 —— tools-de6 的镜像
// （那次用 runner/worker 收窄 agent，这次用 project 收窄 runner）。
// 返回 runner名 → 不可用原因；空 = 可用。**禁用并说明，不静默移除**：悄悄消失的选项比
// 灰掉的选项更难排查（用户不知道自己少了什么）。
//
// fail-safe（与 tools-de6 同一条纪律）：worker 离线 / 未上报 projects ⇒ 信息缺失 ≠ 不支持
// ⇒ 不判它不可用，照常放行、由后端拒。绝不把"不知道"当成"不行"。
// short 进 option 文本（不选中也能看见为什么不能用），full 在选中时展开成一行说明。
// 实现见 utils/runnerChoice.ts（与工作台、Sessions 页共用）。
const runnerBlocks = computed<Record<string, BlockNote>>(() => {
  const out = computeRunnerBlocks(selectedProject.value, runnerOptions.value, workers.value)
  // 调用方上限（ceiling）：范围外的 runner 与项目/worker 的阻塞一样「灰显 + 写原因」；
  // 上限是最根本的一层（项目让不行、上限管不着都不重要），故覆盖前一层的原因。
  for (const r of runnerOptions.value) {
    if (!ceilingAllowsRunner(ceil.value, r.name)) {
      out[r.name] = { short: CEILING_LABEL, full: ceilingDeniedText('runner', r.name) }
    }
  }
  return out
})

// 当前选中的 runner 若不可用，把完整原因摆到台面上（提交也会被 validationError 拦）
const selectedRunnerBlock = computed(
  () => (runnerName.value === '' ? '' : (runnerBlocks.value[runnerName.value]?.full ?? '')),
)

// 当前 agent 类型（候选池含 worker 独有 agent，故索引也取自 agentPool）
const agentType = computed(
  () => agentPool.value.find((a) => a.key === agentKey.value)?.type ?? '',
)
const isExec = computed(() => agentType.value === 'exec')
const isCliAgent = computed(() => agentType.value !== '' && agentType.value !== 'exec')
const canUseContinuousSession = computed(() =>
  agentType.value === 'acp-agent' && (isLocalRunner.value || runnerType.value === 'worker') && !interactive.value &&
  !(selectedRunner.value && sessionRunnerBlock(selectedRunner.value, workers.value)),
)
watch(canUseContinuousSession, (allowed) => {
  if (!allowed) continuousSession.value = false
})

// project 下拉 = 全部 project（含 worker-only），**不按选定的 worker 反向收窄**。
// project 是表单的锚点（第一个字段）：下游的 runner/worker/agent 去适应它，而不是反过来
// 把用户选好的 project 悄悄删掉——「该 worker 跑不了这个 project」这件事由 RUNNER 那一栏
// 禁用 + 给理由来表达（D5），信息一样不丢，但没有"我的选项去哪了"。
// worker-only 项也照常列出：它只在某台 worker 上存在，选中后 local runner 会被禁用并说明。
const projectOptions = computed<MetaProject[]>(() =>
  // 调用方上限（ceiling）：范围外的 project 不列出（下拉没有灰显原因的位置，选到就必被拒）。
  projects.value.filter((p) => ceilingAllowsProject(ceil.value, p.key)),
)

// 被上限藏起来的 project 数（给一句说明，免得用户以为项目不见了）。
const projectCeilingHidden = computed(() => projects.value.length - projectOptions.value.length)

// 选了 worker runner 但目标 worker 还未确定（id 模式未选且 runner 无 pin / labels 模式未填标签
// 或无匹配）→ 能力收窄尚未生效，提示用户先定机器。
const workerNarrowingPending = computed(
  () => isWorkerRunner.value && targetWorkers.value.length === 0,
)

// 收窄来源说明：让用户看见 agent 候选是被谁收窄的（pin / 显式选机 / 标签并集）。
// 归因先按 workerMode 分支——labels 模式下即使只匹配到一台、且恰好等于 runner 的 pin，
// 收窄来源也是标签匹配而非 pin（选机仍由服务端在提交时做）。
const capabilityHint = computed<string>(() => {
  const ws = targetWorkers.value
  if (!isWorkerRunner.value || ws.length === 0) {
    return ''
  }
  if (workerMode.value === 'labels') {
    return ws.length === 1
      ? `标签匹配到 1 台在线 worker（${ws[0].id}）：agent 候选已按其上报能力收窄`
      : `标签匹配到 ${ws.length} 台在线 worker：agent 候选取其能力并集，实际选机由服务端提交时决定`
  }
  return workerId.value === '' && pinnedWorkerId.value !== ''
    ? `agent 候选已按 runner 配置 pin 的 worker ${ws[0].id} 上报能力收窄（无需再指定）`
    : `agent 候选已按 worker ${ws[0].id} 上报能力收窄`
})

function selectProject(key: string) {
  projectKey.value = key
  // runner 先收敛（它决定 agent 的候选）：优先落在「跑得了这个 project」的 runner 上（D5）；
  // 一个都跑不了时仍保留首个允许项，让 validationError 把原因讲清楚，而不是留空下拉。
  runnerName.value = pickRunner(runnerName.value, selectedProject.value, runnerOptions.value, runnerBlocks.value)
  const ags = agentOptions.value
  if (!ags.some((a) => a.key === agentKey.value)) {
    const def = selectedProject.value?.default_agent
    agentKey.value =
      def && ags.some((a) => a.key === def) ? def : ags.length > 0 ? ags[0].key : ''
  }
}

// project select change 必须用最新 allowlist 重算，故包一层
function onProjectChange() {
  selectProject(projectKey.value)
  // 记住这次选的项目，下次新建时作为默认值（设计稿 §6.4-3）。
  rememberProject(projectKey.value)
}

// T5.2：runner / worker / 交互开关变更后，把 agent 收敛到（被执行侧能力收窄后的）合法候选内，
// 不留悬空非法选择；无候选则清空（不保留一个必被后端拒绝的值——原因见 agentEmptyReason）。
// **不动 projectKey**：project 是锚点，下游变化不得改写它（该 worker 跑不了它 → 由 RUNNER
// 栏禁用+给理由表达，D5）。
function reconverge() {
  const ags = agentOptions.value
  if (!ags.some((a) => a.key === agentKey.value)) {
    const def = selectedProject.value?.default_agent
    agentKey.value =
      def && ags.some((a) => a.key === def) ? def : ags.length > 0 ? ags[0].key : ''
  }
}

// runner/worker 三元组的任一变化都会改变「执行侧能力」→ 重新收敛（含程序性赋值，如
// selectProject 改 runnerName）。agent 排在最后定，故这里只收敛 project/agent，不反过来动 runner。
watch([runnerName, workerId, workerMode, workerLabels], () => {
  reconverge()
})

// pin 型 runner 不接受任何改派：显式 worker_id 或 worker_labels 都会被后端拒（pin 是授权，
// 不是默认路由）。切到这类 runner 时把选机状态复位到「跟随 pin」，别把上一个 runner 的
// labels 模式带过来变成一个必被拒的提交。
watch(pinnedWorkerId, (pin) => {
  if (pin !== '') {
    workerMode.value = 'id'
    workerId.value = ''
    workerLabels.value = ''
  }
})

// ── 文件传输（XFER-01 X2）──────────────────────────────────────────
// 上传：选好文件 + 目标路径（job cwd 相对），提交前先逐个暂存（POST /v1/xfer，stage_only）
// 拿到 xfer_id，随请求的 uploads[] 发出去 —— 执行机在 agent 起跑前把文件放到 dest（放不下
// 就 job failed，agent 不起）。收集：job 结束后按 cwd 匹配的 glob，命中项落到该 job 产物的
// collected/ 下（web 详情页可直接预览/下载）。
// 只在本表单提交时生效：rebuild 走另一条通道（只发改动字段），故那边不显示这两栏。
interface UploadRow {
  file: File
  dest: string
}

const uploadRows = ref<UploadRow[]>([])
const collectText = ref('')

// 收集 glob：每行一个（与 AGENT ARGS 的逐行风格一致），去空行。
function parseCollect(raw: string): string[] {
  return raw
    .split('\n')
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

// 暂存目标机器（xfer 的 runner 字段）：worker runner 必须给具体 worker id —— 暂存的文件
// 只会被那台机器取走；local（server/local 是同一个）。按标签自动时落点由服务端提交时决定，
// 预先定不了机器，故 uploads 会被 validationError 拦下（宁可说清楚，也不发一个必失败的 job）。
const xferRunner = computed<string>(() =>
  isWorkerRunner.value ? effectiveWorkerId.value : runnerName.value,
)

// 浏览器端算 sha256（server 只拿它做校验；空串 = 不校验内容、只比 size）。crypto.subtle
// 只在安全上下文（https / localhost）存在，取不到就交空串，不让它挡住提交。
async function sha256Hex(file: File): Promise<string> {
  if (!globalThis.crypto?.subtle) {
    return ''
  }
  const digest = await crypto.subtle.digest('SHA-256', await file.arrayBuffer())
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

// 逐个暂存选中的文件：任何一步失败就抛错中止提交 —— 半套上传的 job 起跑必挂，不如在这里
// 把「哪个文件、去哪里、为什么」说清楚。
async function stageUploads(): Promise<JobUpload[]> {
  const out: JobUpload[] = []
  for (const row of uploadRows.value) {
    const dest = row.dest.trim()
    const meta: XferPutMeta = {
      op: 'put',
      runner: xferRunner.value,
      project: projectKey.value,
      path: dest,
      size: row.file.size,
      sha256: await sha256Hex(row.file),
      force: false,
      // 只暂存、不派发：文件由这个 job 的执行机在起跑前取走。
      stage_only: true,
    }
    try {
      const staged = await stageXfer(meta, row.file, row.file.name)
      out.push({ xfer_id: staged.id, dest })
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      throw new Error(`暂存「${row.file.name} → ${dest}」失败：${msg}`)
    }
  }
  return out
}

function onFilesPicked(e: Event): void {
  const input = e.target as HTMLInputElement
  for (const f of Array.from(input.files ?? [])) {
    uploadRows.value.push({ file: f, dest: '' })
  }
  // 清空 input：同一个文件删掉后还能再选一次（否则 change 不再触发）。
  input.value = ''
}

// 校验 + 组装请求
const validationError = computed<string>(() => {
  if (!projectKey.value) {
    return '请选择 project'
  }
  // 上限：预填 / rebuild 绕过了下拉的过滤，这里再拦一道（服务端同口径 httppai 404）。
  if (!ceilingAllowsProject(ceil.value, projectKey.value)) {
    return ceilingDeniedText('project', projectKey.value)
  }
  if (agentKey.value !== '' && !ceilingAllowsAgent(ceil.value, agentKey.value)) {
    return ceilingDeniedText('agent', agentKey.value)
  }
  if (isExec.value && !ceilingAllowsExec(ceil.value)) {
    return `${CEILING_LABEL}：这个上限不允许 exec job`
  }
  if (!runnerName.value) {
    return '请选择 runner'
  }
  if (selectedRunnerBlock.value !== '') {
    return selectedRunnerBlock.value // D5：选中的 runner 跑不了这个 project
  }
  // worker 未定 → 能力未知，先定机器再选 agent（runner 已在 config 里 pin worker 时无需再指定）
  if (isWorkerRunner.value) {
    if (workerMode.value === 'id' && effectiveWorkerId.value === '') {
      return 'runner=worker：请指定 worker，或切换为按标签自动'
    }
    if (workerMode.value === 'labels' && workerLabels.value.trim() === '') {
      return 'runner=worker：请填写 worker_labels，或切换为指定 worker'
    }
    if (workerMode.value === 'labels' && labelCandidates.value.length === 0) {
      return `无在线 worker 同时具备标签 ${parseLabels(workerLabels.value).join(',')}`
    }
  }
  if (!agentKey.value) {
    return agentEmptyReason.value !== '' ? agentEmptyReason.value : '请选择 agent'
  }
  if (continuousSession.value && !canUseContinuousSession.value) {
    return '持续会话目前仅支持本机 ACP agent'
  }
  const fsErr = fromSessionFormError(fromSession.value, {
    agentType: agentType.value,
    agentFromSession: agentFromSessionCaps.value[agentKey.value],
    continuousSession: continuousSession.value,
  })
  if (fsErr !== '') {
    return fsErr
  }
  if (isCliAgent.value && !interactive.value && !continuousSession.value && prompt.value.trim() === '' && templateName.value === '') {
    return 'cli-agent 需填写 prompt（或选一个任务书模板）'
  }
  if (templateMissing.value.length > 0) {
    return `模板缺必填变量：${templateMissing.value.join(', ')}`
  }
  if (isExec.value && command.value.trim() === '') {
    return 'exec 需填写 command'
  }
  // 文件上传：暂存需要一台确定的执行机（暂存的文件只会被它取走），且每个文件都要有目标路径。
  if (uploadRows.value.length > 0) {
    if (xferRunner.value === '') {
      return '上传文件需要先指定 worker（按标签自动时落点由服务端决定，无法预先暂存）'
    }
    if (uploadRows.value.some((r) => r.dest.trim() === '')) {
      return '请为每个上传文件填写目标路径（执行机 cwd 相对，如 tmp/in/a.bin）'
    }
  }
  return ''
})

// 从收集箱条目新建 job（?from_item=<id>）：预填标题 / 目标 / 项目 / 目录，提交成功后
// 把 job 关联回条目（dispatch to=job）；关联失败不丢 job，留在页面上重试。
const fromItem = computed(() => readFromItemQuery(route.query.from_item))
const itemNote = ref('')
const submittedJobId = ref('')
const itemLinked = ref(false)
const linkError = ref('')
const linking = ref(false)
const canSubmitJobs = computed(() => holds('job.submit'))

async function linkSubmittedJob(): Promise<void> {
  if (!fromItem.value || !submittedJobId.value || linking.value) return
  linking.value = true
  linkError.value = ''
  const r = await linkJobAfterSubmit((req) => dispatchWorkItem(fromItem.value, req), submittedJobId.value)
  itemLinked.value = r.linked
  linkError.value = r.error
  linking.value = false
}

async function prefillFromItem(id: string): Promise<void> {
  try {
    const it = await getWorkItem(id)
    if ((it.status as string) !== 'captured') itemNote.value = '这条已经不在收集箱了，提交后无法再关联。'
    const pre = newJobPrefill(it)
    if (typeof route.query.prompt !== 'string' && pre.prompt) prompt.value = pre.prompt
    if (typeof route.query.title !== 'string' && it.title) title.value = it.title.slice(0, 80)
    if (pre.project && projects.value.some((p) => p.key === pre.project)) selectProject(pre.project)
    if (pre.cwd) cwd.value = pre.cwd
  } catch (e) {
    itemNote.value = `读取收集箱条目失败：${e instanceof Error ? e.message : String(e)}`
  }
}

const canSubmit = computed(() => !ceilingReadOnly.value && !submitting.value && validationError.value === '' && submittedJobId.value === '' && (fromItem.value === '' || canSubmitJobs.value))

function parseLabels(raw: string): string[] {
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

function parseCmd(raw: string): string[] {
  return raw
    .trim()
    .split(/\s+/)
    .filter((s) => s !== '')
}

// agent_args 每行一个完整 argv 元素（不做空格分词：flag 值可能含空格，如
// --allowedTools=Bash(git log)），trim 空白、去空行。
function parseAgentArgs(raw: string): string[] {
  return raw
    .split('\n')
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

async function onSubmit() {
  submitError.value = ''
  notice.value = ''
  if (validationError.value !== '') {
    submitError.value = validationError.value
    return
  }
  submitting.value = true
  try {
    if (isRebuild.value) {
      const job = await rebuildJob(rebuildFrom.value, buildRebuildBody())
      void router.push(`/jobs/${job.id}`)
      return
    }
    const req = {
      project_key: projectKey.value,
      agent: agentKey.value,
      runner: runnerName.value,
      cwd: cwd.value.trim() || '.',
      sync: sync.value && !continuousSession.value,
      // 提交来源（provenance）：web 控制台固定 channel=web；client(来源 IP)由 server 盖章。
      channel: 'web',
    } as Parameters<typeof submitJob>[0]
    if (isCliAgent.value && interactive.value) {
      if (prompt.value.trim() !== '') {
        req.system_prompt = prompt.value.trim()
      }
    } else if (isCliAgent.value) {
      req.prompt = prompt.value
    }
    if (isCliAgent.value) {
      const m = model.value.trim()
      if (m !== '') req.model = m
      // per-job agent flags：仅 cli-agent 生效（exec 由后端拒绝 exec+agent_args）。
      const aa = parseAgentArgs(agentArgs.value)
      if (aa.length > 0) {
        req.agent_args = aa
      }
    }
    if (isExec.value) {
      req.cmd = parseCmd(command.value)
    }
    if (templateName.value !== '') {
      req.template = templateName.value
      const tv: Record<string, string> = {}
      for (const [k, v] of Object.entries(templateVars.value)) {
        if (v !== '') {
          tv[k] = v
        }
      }
      if (Object.keys(tv).length > 0) {
        req.vars = tv
      }
    }
    if (title.value.trim() !== '') {
      req.title = title.value.trim()
    }
    const tagList = parseLabels(tags.value)
    if (tagList.length > 0) {
      req.tags = tagList
    }
    if (timeoutSec.value != null && timeoutSec.value > 0) {
      req.timeout_sec = timeoutSec.value
    }
    if (continuousSession.value) {
      req.session = true
      if (idleTimeoutSec.value != null && idleTimeoutSec.value > 0) req.idle_timeout_sec = idleTimeoutSec.value
      if (maxSessionSec.value != null && maxSessionSec.value > 0) req.max_session_sec = maxSessionSec.value
    }
    if (readOnly.value) {
      req.read_only = true
    }
    // gofer-jkd2：锁声明 / 等锁时长 / worktree。空值一律不发（保持既有默认行为）。
    const lockList = parseLockPaths(lockPaths.value)
    if (lockList.length > 0) {
      req.lock_paths = lockList
    }
    if (lockWaitSec.value != null && lockWaitSec.value >= 0) {
      req.lock_wait_sec = lockWaitSec.value
    }
    if (worktree.value) {
      req.worktree = true
    }
    const budget = budgetFromForm()
    if (budget) {
      req.budget = budget
    }
    if (interactive.value) {
      req.interactive = true
      if (recordPty.value) {
        req.record_pty = true
      }
      if (cols.value > 0) {
        req.cols = cols.value
      }
      if (rows.value > 0) {
        req.rows = rows.value
      }
    }
    // 文件传输（XFER-01 X2）：先把选中的文件逐个暂存（stage_only）拿到 xfer_id，再随
    // uploads[] 提交 —— 任何一步暂存失败都会抛错，整单不提交（见 stageUploads）。
    if (uploadRows.value.length > 0) {
      req.uploads = await stageUploads()
    }
    const collect = parseCollect(collectText.value)
    if (collect.length > 0) {
      req.collect = collect
    }
    // pin 型 runner：一个字段都不发——worker_id/worker_labels 都是"改派"，后端一律拒。
    if (isWorkerRunner.value && pinnedWorkerId.value === '') {
      if (workerMode.value === 'id') {
        if (workerId.value !== '') {
          req.worker_id = workerId.value
        }
      } else {
        req.worker_labels = parseLabels(workerLabels.value)
      }
    }
    withFromSession(req, fromSession.value, agentType.value === 'cli-agent')
    const { job, async } = await submitJob(req)
    if (fromItem.value) {
      submittedJobId.value = job.id
      notice.value = async ? '已提交，仍在后台执行。' : '已提交。'
      await linkSubmittedJob()
      return
    }
    if (async) {
      notice.value = '已提交，仍在后台执行，正在跳转详情…'
    }
    void router.push(`/jobs/${job.id}`)
  } catch (e) {
    submitError.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}

// R8：selectProject 会按 allowlist 把 agent/runner 收敛到默认——必须先 selectProject 触发联动，
// 再显式覆盖 agentKey/runnerName，否则被联动重置。env 只填 key（值脱敏、不进表单值）。
async function prefillFrom(from: string): Promise<void> {
  try {
    const { request: r, redacted } = await getJobRequest(from)
    if (r.project_key) selectProject(r.project_key)   // 先联动
    if (r.agent) agentKey.value = r.agent             // 再覆盖
    if (r.runner) runnerName.value = r.runner
    if (r.cwd) cwd.value = r.cwd
    if (r.lock_paths && r.lock_paths.length) lockPaths.value = formatLockPaths(r.lock_paths)
    if (r.lock_wait_sec != null) lockWaitSec.value = r.lock_wait_sec
    if (r.worktree) worktree.value = true
    if (r.interactive) interactive.value = true
    if (r.read_only) readOnly.value = true
    if (r.system_prompt) prompt.value = r.system_prompt
    else if (r.prompt) prompt.value = r.prompt             // 可能含占位；用户改则校验、不改则不发
    if (r.cmd && r.cmd.length) command.value = r.cmd.join(' ')
    if (r.agent_args && r.agent_args.length) agentArgs.value = r.agent_args.join('\n')
    if (r.model) model.value = r.model
    if (r.budget?.max_tokens) maxTokens.value = r.budget.max_tokens
    if (r.budget?.max_cost_usd) maxCostUsd.value = r.budget.max_cost_usd
    if (r.budget?.max_turns) maxTurns.value = r.budget.max_turns
    if (r.title) title.value = r.title
    if (r.tags && r.tags.length) tags.value = r.tags.join(', ')
    if (r.timeout_sec) timeoutSec.value = r.timeout_sec
    if (r.interactive) { if (r.cols) cols.value = r.cols; if (r.rows) rows.value = r.rows }
    if (r.worker_id) { workerMode.value = 'id'; workerId.value = r.worker_id; advancedOpen.value = true }
    else if (r.worker_labels?.length) { workerMode.value = 'labels'; workerLabels.value = r.worker_labels.join(', '); advancedOpen.value = true }
    envRows.value = Object.keys(r.env ?? {}).map((k) => ({ key: k, action: 'keep' as EnvAction, value: '' }))
    if (r.plan_id) planId.value = r.plan_id
    rebuildRedacted.value = redacted
    snapshotBaseline()   // 记初值，供提交 diff
  } catch (e) {
    submitError.value = e instanceof Error ? e.message : String(e)
  }
}

function snapshotBaseline(): void {
  baseline.value = {
    project_key: projectKey.value, agent: agentKey.value, runner: runnerName.value,
    prompt: prompt.value, command: command.value, cwd: cwd.value, title: title.value,
    lock_paths: lockPaths.value, lock_wait_sec: lockWaitSec.value, worktree: worktree.value,
    tags: tags.value, timeout: timeoutSec.value, interactive: interactive.value,
    read_only: readOnly.value,
    cols: cols.value, rows: rows.value, worker_id: workerId.value,
    worker_labels: workerLabels.value, plan_id: planId.value,
    agent_args: agentArgs.value,
    model: model.value,
    max_tokens: maxTokens.value, max_cost_usd: maxCostUsd.value, max_turns: maxTurns.value,
  }
}

// 只发改动过的标量（对比 baseline）+ env_set/env_unset（来自 envRows/envAdds）。
function buildRebuildBody(): RebuildBody {
  const b: RebuildBody = {}
  const chg = <T,>(key: string, cur: T, apply: (v: T) => void) => {
    if (baseline.value[key] !== cur) apply(cur)
  }
  chg('project_key', projectKey.value, (v) => (b.project_key = v))
  chg('agent', agentKey.value, (v) => (b.agent = v))
  chg('runner', runnerName.value, (v) => (b.runner = v))
  if (isCliAgent.value) {
    if (interactive.value) chg('prompt', prompt.value, (v) => (b.system_prompt = v))
    else chg('prompt', prompt.value, (v) => (b.prompt = v))
    chg('agent_args', agentArgs.value, () => (b.agent_args = parseAgentArgs(agentArgs.value)))
    chg('model', model.value, (v) => { if (String(v).trim() !== '') b.model = String(v).trim() })
  }
  if (isExec.value) chg('command', command.value, () => (b.cmd = parseCmd(command.value)))
  chg('read_only', readOnly.value, (v) => (b.read_only = v))
  // N2 §B：任一预算维度改过就整体重发（后端 rebuild 的 budget 覆盖是整体替换；清空 = 全 0 = 取消上限）。
  if (baseline.value.max_tokens !== maxTokens.value || baseline.value.max_cost_usd !== maxCostUsd.value || baseline.value.max_turns !== maxTurns.value) {
    b.budget = budgetFromForm() ?? {}
  }
  chg('cwd', cwd.value, (v) => (b.cwd = v))
  // gofer-jkd2：锁定目录 / 等锁时长 / worktree 也按「改过才发」回传。清空锁定目录会发一个
  // 空数组，= 覆盖掉继承的锁声明（服务端存 lock_paths:[]，回到锁整个 cwd）。
  chg('lock_paths', lockPaths.value, (v) => (b.lock_paths = parseLockPaths(v as string)))
  chg('lock_wait_sec', lockWaitSec.value, (v) => {
    if (v != null && (v as number) >= 0) b.lock_wait_sec = v as number
  })
  chg('worktree', worktree.value, (v) => (b.worktree = v as boolean))
  chg('title', title.value, (v) => { if (String(v).trim() !== '') b.title = String(v).trim() })
  chg('tags', tags.value, () => (b.tags = parseLabels(tags.value)))
  // v0.3 零值陷阱规避：原 `v ?? 0` 会把「清空 timeout」发成 0（服务端显式清零而非继承）。
  // 改为仅当填了正整数才发；清空/0 → 不发该字段 → 服务端继承源 timeout。（真要「无超时」须另设
  // 显式选项，本期不做——见风险 R15。）
  chg('timeout', timeoutSec.value, (v) => { if (v != null && (v as number) > 0) b.timeout_sec = v as number })
  chg('interactive', interactive.value, (v) => (b.interactive = v))
  if (interactive.value) {
    chg('cols', cols.value, (v) => { if (Number(v) > 0) b.cols = Number(v) })
    chg('rows', rows.value, (v) => { if (Number(v) > 0) b.rows = Number(v) })
  }
  if (isWorkerRunner.value) {
    if (workerMode.value === 'id') {
      chg('worker_id', workerId.value, (v) => { if (v.trim() !== '') b.worker_id = v })
    } else {
      chg('worker_labels', workerLabels.value, () => {
        const labels = parseLabels(workerLabels.value)
        if (labels.length > 0) b.worker_labels = labels
      })
    }
  }
  chg('plan_id', planId.value, (v) => { if (v.trim() !== '') b.plan_id = v })
  b.channel = 'web'
  // env：keep 不发；set → env_set；unset → env_unset；新增行 → env_set。
  const envSet: Record<string, string> = {}
  const envUnset: string[] = []
  for (const row of envRows.value) {
    const key = row.key.trim()
    if (key === '') continue
    if (row.action === 'set') {
      if (row.value !== '' && row.value !== '••••') envSet[key] = row.value
    } else if (row.action === 'unset') envUnset.push(key)
  }
  for (const a of envAdds.value) {
    const key = a.key.trim()
    if (key && a.value !== '' && a.value !== '••••') envSet[key] = a.value
  }
  // v0.3：同一 key 不同时进 env_set 与 env_unset（避免歧义；服务端亦 unset 优先）。unset 赢，
  // 故从 envSet 剔除任何也在 envUnset 里的 key。
  for (const k of envUnset) delete envSet[k]
  if (Object.keys(envSet).length) b.env_set = envSet
  if (envUnset.length) b.env_unset = envUnset
  return b
}

onMounted(async () => {
  mobileViewport.value = window.matchMedia('(max-width: 560px)').matches
  if (sessionMode.value) {
    interactive.value = true
  }
  await loadMeta()
  const queryProject = typeof route.query.project === 'string' ? route.query.project : ''
  const queryAgent = typeof route.query.agent === 'string' ? route.query.agent : ''
  const queryRunner = typeof route.query.runner === 'string' ? route.query.runner : ''
  if (queryProject && projects.value.some((p) => p.key === queryProject)) selectProject(queryProject)
  // 设计稿 §6.4-3：没带项目来时默认取上次用过的项目（gofer.lastProject）；重建 / 从条目新开
  // 随后会由各自的 prefill 重新选定，这里只是给「直接打开 /new」一个有用的默认值。
  if (!queryProject && !isRebuild.value && !fromItem.value) {
    const last = defaultProject(route.query)
    if (last && projects.value.some((p) => p.key === last)) selectProject(last)
  }
  if (queryRunner && runners.value.some((r) => r.name === queryRunner)) runnerName.value = queryRunner
  if (queryAgent && agents.value.some((a) => a.key === queryAgent)) agentKey.value = queryAgent
  if (typeof route.query.title === 'string') title.value = route.query.title
  if (typeof route.query.prompt === 'string') prompt.value = route.query.prompt
  if (typeof route.query.model === 'string') model.value = route.query.model
  if (route.query.type === 'acp') continuousSession.value = true
  if (typeof route.query.cwd === 'string' && route.query.cwd.trim() !== '') cwd.value = route.query.cwd.trim()
  // gofer-jkd2：Sessions 页「完整配置」把已填的锁定目录带过来（逗号/换行分隔，同表单格式）。
  if (typeof route.query.lock === 'string' && route.query.lock.trim() !== '') lockPaths.value = route.query.lock.trim()
  if (fromSession.value !== '') {
    void getAgentsCached()
      .then((r) => {
        agentFromSessionCaps.value = Object.fromEntries((r.agents ?? []).map((a) => [a.key, a.from_session]))
      })
      .catch(() => {})
  }
  if (isRebuild.value) {
    await prefillFrom(rebuildFrom.value)
  } else if (fromItem.value) {
    await prefillFromItem(fromItem.value)
  }
})

watch(interactive, (on) => {
  if (!on) {
    recordPty.value = false
  }
  // interactive 切换改变 agentOptions（只列 interactive-capable）→ 收敛 agent，避免悬空非法选择。
  const ags = agentOptions.value
  if (ags.length > 0 && !ags.some((a) => a.key === agentKey.value)) {
    const def = selectedProject.value?.default_agent
    agentKey.value = def && ags.some((a) => a.key === def) ? def : ags[0].key
  }
})
</script>

<template>
  <div class="newjob" data-test="newjob-page">
    <header class="newjob-head">
      <BackLink list="jobs" to="/jobs" :label="t('newjob.back')" />
      <h1 class="title">{{ pageTitle }}</h1>
    </header>

    <p v-if="loadError" class="error">{{ t('newjob.loadFailed', { err: loadError }) }}</p>
    <p v-else-if="loading" class="hint">{{ t('newjob.loading') }}</p>

    <form v-else class="card" @submit.prevent="onSubmit">
      <div v-if="isRebuild && rebuildRedacted" class="redacted-banner">
        {{ t('newjob.redactedBanner') }}
      </div>
      <div v-if="fromItem" class="from-session-banner" data-test="from-item-banner">
        {{ t('newjob.fromItem.banner', { id: fromItem }) }}<RouterLink :to="workItemLink(fromItem)" :title="fromItem">{{ fromItem }}</RouterLink>
        <span v-if="itemNote" class="from-item-note" data-test="from-item-note">{{ itemNote }}</span>
        <span v-if="!canSubmitJobs" class="from-item-note" data-test="from-item-nosubmit">{{ t('newjob.fromItem.noSubmit') }}</span>
      </div>
      <div v-if="fromSession" class="from-session-banner" data-test="from-session-banner">
        {{ t('newjob.fromSession.banner', { id: fromSession }) }}
      </div>

      <!-- 常用：两栏（设计稿 §7.1「新建任务」）。左栏决定执行侧（项目 / runner / worker / agent），
           右栏是任务本身（模板 / 标题 / cwd / 任务说明）。 -->
      <section class="form-grid" :aria-label="t('newjob.title')">
        <!-- project -->
        <div class="field">
          <label class="label" for="nj-project">{{ t('newjob.field.project') }}</label>
          <select
            id="nj-project"
            v-model="projectKey"
            class="control"
            @change="onProjectChange"
          >
            <option v-for="p in projectOptions" :key="p.key" :value="p.key">{{ p.key }}</option>
          </select>
          <p v-if="projectOptions.length === 0" class="field-hint">{{ t('newjob.noProject') }}</p>
          <p v-else-if="projectCeilingHidden > 0" class="field-hint field-hint--warn" data-test="nj-project-ceiling">
            {{ t('newjob.projectCeiling', { n: projectCeilingHidden, label: CEILING_LABEL }) }}
          </p>
        </div>

        <!-- runner（必须先于 agent：agent 候选 = project 白名单 ∩ 执行侧真实能力） -->
        <div class="field">
          <label class="label" for="nj-runner">{{ t('newjob.field.runner') }}</label>
          <select id="nj-runner" v-model="runnerName" class="control">
            <option
              v-for="r in runnerOptions"
              :key="r.name"
              :value="r.name"
              :disabled="!!runnerBlocks[r.name]"
            >
              {{ runnerOptionText(r) }}<template v-if="runnerBlocks[r.name]"> · {{ runnerBlocks[r.name].short }}</template>
            </option>
          </select>
          <p v-if="selectedRunnerBlock" class="field-hint field-hint--warn">
            {{ selectedRunnerBlock }}
          </p>
        </div>

        <!-- runner=worker 选机（agent 之前——它决定 agent 候选）。
             pin 型 runner 没有"选机"可言：配置已把它绑死到一台，且那是授权而非默认路由
             （请求不得改路由，后端会拒），所以这里只陈述事实，不给选项。 -->
        <div v-if="isWorkerRunner && pinnedWorkerId" class="field">
          <label class="label">{{ t('newjob.field.worker') }}</label>
          <p class="field-hint">
            {{ t('newjob.pinnedWorker', { id: pinnedWorkerId }) }}
          </p>
        </div>

        <details v-else-if="isWorkerRunner" class="advanced" :open="advancedOpen">
          <summary @click.prevent="advancedOpen = !advancedOpen">
            {{ t('newjob.worker.title') }}
          </summary>
          <div class="advanced-body">
            <div class="seg">
              <button
                type="button"
                class="seg-btn"
                :class="{ 'seg-btn--on': workerMode === 'id' }"
                :aria-pressed="workerMode === 'id'"
                @click="workerMode = 'id'"
              >
                {{ t('newjob.worker.byId') }}
              </button>
              <button
                type="button"
                class="seg-btn"
                :class="{ 'seg-btn--on': workerMode === 'labels' }"
                :aria-pressed="workerMode === 'labels'"
                @click="workerMode = 'labels'"
              >
                {{ t('newjob.worker.byLabels') }}
              </button>
            </div>

            <div v-if="workerMode === 'id'" class="field">
              <label class="label" for="nj-wid">{{ t('newjob.worker.id') }}</label>
              <select id="nj-wid" v-model="workerId" class="control">
                <option value="">{{ t('newjob.worker.idPlaceholder') }}</option>
                <option v-for="w in connectedWorkers" :key="w.id" :value="w.id">
                  {{ w.id }}<template v-if="w.labels && w.labels.length"> · {{ w.labels.join(',') }}</template>
                </option>
              </select>
              <p v-if="connectedWorkers.length === 0" class="field-hint">
                {{ t('newjob.worker.noConnected') }}
              </p>
            </div>

            <div v-else class="field">
              <label class="label" for="nj-labels">{{ t('newjob.worker.labels') }}</label>
              <input
                id="nj-labels"
                v-model="workerLabels"
                class="control"
                spellcheck="false"
                autocomplete="off"
                placeholder="gpu, linux"
              />
              <p class="field-hint">{{ t('newjob.worker.labelsHint') }}</p>
            </div>
          </div>
        </details>

        <p v-if="workerNarrowingPending" class="field-hint field-hint--warn span2">
          {{ t('newjob.worker.pending') }}
        </p>
        <p v-else-if="capabilityHint" class="field-hint span2">{{ capabilityHint }}</p>

        <!-- agent（最后定：受 project 白名单与执行侧能力双向约束） -->
        <div class="field">
          <label class="label" for="nj-agent">{{ t('newjob.field.agent') }}</label>
          <select
            id="nj-agent"
            v-model="agentKey"
            class="control"
            :disabled="agentOptions.length === 0"
          >
            <option v-for="a in agentOptions" :key="a.key" :value="a.key">
              {{ a.key }} · {{ a.type }}
            </option>
          </select>
          <p v-if="agentEmptyReason" class="field-hint field-hint--warn">{{ agentEmptyReason }}</p>
        </div>

        <div class="field">
          <label class="label" for="nj-title">{{ t('newjob.field.title') }}</label>
          <input
            id="nj-title"
            v-model="title"
            class="control"
            autocomplete="off"
            :placeholder="t('newjob.title.placeholder')"
          />
        </div>

        <!-- 任务书模板（SUP-01 P5）：选中后由服务端渲染正文预览（include/head 只有服务端
             展开得了），变量输入按模板声明的 vars 生成；提交只发 template + vars，
             prompt 栏此时是"追加正文"。 -->
        <div class="field">
          <label class="label" for="nj-template">{{ t('newjob.field.template') }}</label>
          <select id="nj-template" v-model="templateName" class="control">
            <option value="">{{ t('newjob.template.none') }}</option>
            <option
              v-for="tpl in templates"
              :key="tpl.name"
              :value="tpl.name"
              :disabled="!!tpl.error"
            >
              {{ tpl.name }} · {{ tpl.source }}{{ tpl.desc ? ` · ${tpl.desc}` : ''
              }}{{ tpl.error ? t('newjob.template.parseFailed') : '' }}
            </option>
          </select>
          <p v-if="templateError" class="field-hint field-hint--warn">{{ templateError }}</p>
          <p v-else-if="selectedTemplate?.error" class="field-hint field-hint--warn">
            {{ selectedTemplate.error }}
          </p>
          <p v-else-if="selectedTemplate" class="field-hint">{{ selectedTemplate.path }}</p>

          <div v-if="templateVarsList.length > 0" class="tmpl-vars">
            <div v-for="[vn, spec] in templateVarsList" :key="vn" class="tmpl-var">
              <label class="label" :for="`nj-tvar-${vn}`">
                {{ vn }}{{ spec.required ? ' *' : '' }}
              </label>
              <input
                :id="`nj-tvar-${vn}`"
                v-model="templateVars[vn]"
                class="control"
                spellcheck="false"
                :placeholder="spec.default ?? ''"
              />
              <p v-if="spec.desc" class="field-hint">{{ spec.desc }}</p>
            </div>
          </div>

          <template v-if="templateName !== '' && !templateError">
            <p v-if="templateMissing.length > 0" class="field-hint field-hint--warn">
              {{ t('newjob.template.missing', { names: templateMissing.join(', ') }) }}
            </p>
            <p
              v-for="(w, i) in templateWarnings"
              :key="i"
              class="field-hint field-hint--warn"
            >
              {{ w }}
            </p>
            <p class="field-hint">{{ t('newjob.template.previewHint') }}</p>
            <pre class="tmpl-preview">{{ templatePrompt }}</pre>
          </template>
        </div>

        <div class="field">
          <label class="label" for="nj-cwd">{{ t('newjob.field.cwd') }}</label>
          <input
            id="nj-cwd"
            v-model="cwd"
            class="control"
            :class="{ 'control--redacted': cwdRedacted }"
            spellcheck="false"
            autocomplete="off"
            placeholder="."
          />
          <p v-if="cwdRedacted" class="field-hint field-hint--warn">
            {{ t('newjob.hint.mustReplace') }}
          </p>
        </div>

        <!-- cli-agent: prompt 文本域 -->
        <div v-if="isCliAgent" class="field span2">
          <label class="label" for="nj-prompt">{{ promptLabel }}</label>
          <textarea
            id="nj-prompt"
            v-model="prompt"
            class="control area"
            :class="{ 'control--redacted': promptRedacted }"
            rows="8"
            spellcheck="false"
            :placeholder="promptPlaceholder"
          ></textarea>
          <p v-if="promptRedacted" class="field-hint field-hint--warn">
            {{ t('newjob.hint.mustReplace') }}
          </p>
          <p v-if="interactive" class="field-hint">
            {{ t('newjob.hint.interactiveWrites') }}
          </p>
        </div>

        <!-- exec: command 输入 -->
        <div v-else-if="isExec" class="field span2">
          <label class="label" for="nj-cmd">{{ t('newjob.field.command') }}</label>
          <input
            id="nj-cmd"
            v-model="command"
            class="control"
            :class="{ 'control--redacted': commandRedacted }"
            spellcheck="false"
            autocomplete="off"
            :placeholder="t('newjob.command.placeholder')"
          />
          <p v-if="commandRedacted" class="field-hint field-hint--warn">
            {{ t('newjob.hint.mustReplace') }}
          </p>
        </div>
      </section>

      <!-- 高级：模型 / agent 参数 / 锁 / 会话 / 上限 / PTY / 上传 / 收集 / env / 超时（设计稿 §7.1）。
           桌面上是可折叠分组；手机上默认收起（原来就是这套行为）。 -->
      <details class="mobile-advanced" :open="!mobileViewport || mobileAdvancedOpen" @toggle="mobileAdvancedOpen = ($event.target as HTMLDetailsElement).open">
        <summary>{{ t('newjob.advanced') }}</summary>

        <div class="form-grid">
          <!-- N1 §B：可选模型；cli-agent 走 model_args，acp-agent 走协议 -->
          <div v-if="isCliAgent" class="field span2">
            <label class="label" for="nj-model">{{ t('newjob.field.model') }}</label>
            <input
              id="nj-model"
              v-model="model"
              class="control"
              spellcheck="false"
              autocomplete="off"
              :placeholder="t('newjob.model.placeholder')"
            />
            <p class="field-hint">{{ t('newjob.model.hint') }}</p>
          </div>

          <!-- cli-agent: per-job agent flags（xu64.12 §14），每行一个完整参数 -->
          <div v-if="isCliAgent" class="field span2">
            <label class="label" for="nj-agent-args">{{ t('newjob.field.agentArgs') }}</label>
            <textarea
              id="nj-agent-args"
              v-model="agentArgs"
              class="control area"
              rows="3"
              spellcheck="false"
              :placeholder="t('newjob.agentArgs.placeholder')"
            ></textarea>
            <p class="field-hint">{{ t('newjob.agentArgs.hint') }}</p>
          </div>

          <!-- 锁定目录 / 等锁时长 / worktree（gofer-jkd2）：对应 CLI 的 --lock / --lock-wait / --worktree。
               锁定目录按项目根相对（与后端 SafeJoin 同口径：不许逃出项目根）。 -->
          <div class="field">
            <label class="label" for="nj-lock">{{ t('newjob.field.lock') }}</label>
            <textarea
              id="nj-lock"
              v-model="lockPaths"
              class="control area"
              rows="2"
              spellcheck="false"
              placeholder="gookit/ini&#10;gookit/cli"
            ></textarea>
            <p class="field-hint">{{ DIR_LOCK_HINT }}</p>
            <p v-if="lockCandidates.length" class="field-hint" data-test="lock-candidates">
              {{ t('newjob.lock.candidates') }}
              <button
                v-for="candidate in lockCandidates"
                :key="candidate"
                class="lock-chip"
                type="button"
                @click="addLockPath(candidate)"
              >{{ candidate }}</button>
            </p>
          </div>
          <div class="field">
            <label class="label" for="nj-lock-wait">{{ t('newjob.field.lockWait') }}</label>
            <input
              id="nj-lock-wait"
              v-model.number="lockWaitSec"
              class="control"
              type="number"
              min="0"
              :placeholder="t('newjob.lockWait.placeholder')"
            />
            <p class="field-hint">{{ t('newjob.lockWait.hint') }}</p>
          </div>

          <div class="field span2">
            <label class="check">
              <input v-model="worktree" type="checkbox" />
              <span>{{ t('newjob.field.worktree') }}</span>
            </label>
            <p class="field-hint">{{ t('newjob.worktree.hint') }}</p>
          </div>

          <!-- interactive pty：通常配合 runner=worker，本地/准入规则由后端最终校验。 -->
          <div class="field span2">
            <label class="check">
              <input v-model="interactive" type="checkbox" />
              <span>{{ t('newjob.field.interactive') }}</span>
            </label>
          </div>
          <div v-if="canUseContinuousSession && !isRebuild" class="field span2">
            <label class="check">
              <input v-model="continuousSession" type="checkbox" />
              <span>{{ t('newjob.field.session') }}</span>
            </label>
          </div>
          <template v-if="continuousSession">
            <div class="field">
              <label class="label" for="nj-idle-timeout">{{ t('newjob.field.idleTimeout') }}</label>
              <input id="nj-idle-timeout" v-model.number="idleTimeoutSec" class="control" type="number" min="1" placeholder="1800" />
            </div>
            <div class="field">
              <label class="label" for="nj-max-session">{{ t('newjob.field.maxSession') }}</label>
              <input id="nj-max-session" v-model.number="maxSessionSec" class="control" type="number" min="1" :placeholder="t('newjob.placeholder.unlimited')" />
            </div>
          </template>

          <!-- 只读：agent 不能写文件（cli-agent 追加沙箱参数 / acp-agent set_mode）。 -->
          <div class="field span2">
            <label class="check">
              <input v-model="readOnly" type="checkbox" />
              <span>{{ t('newjob.field.readOnly') }}</span>
            </label>
          </div>

          <!-- N2 §B 预算熔断（可选）：执行侧按 agent 上报的用量计量，越线即终止整个 job 并标 failure_class=budget。
               需要 agent 上报可读用量（claude/omp 的 ndjson、acp-agent、codex），否则后端 400。 -->
          <div class="field">
            <label class="label" for="nj-max-tokens">{{ t('newjob.field.maxTokens') }}</label>
            <input id="nj-max-tokens" v-model.number="maxTokens" class="control" type="number" min="1" :placeholder="t('newjob.placeholder.unlimited')" />
          </div>
          <div class="field">
            <label class="label" for="nj-max-cost">{{ t('newjob.field.maxCost') }}</label>
            <input id="nj-max-cost" v-model.number="maxCostUsd" class="control" type="number" min="0" step="0.01" :placeholder="t('newjob.placeholder.unlimited')" />
          </div>
          <div class="field">
            <label class="label" for="nj-max-turns">{{ t('newjob.field.maxTurns') }}</label>
            <input id="nj-max-turns" v-model.number="maxTurns" class="control" type="number" min="1" :placeholder="t('newjob.placeholder.unlimited')" />
          </div>

          <template v-if="interactive">
            <div class="field">
              <label class="label" for="nj-cols">{{ t('newjob.field.cols') }}</label>
              <input
                id="nj-cols"
                v-model.number="cols"
                class="control"
                type="number"
                min="1"
                placeholder="120"
              />
            </div>
            <div class="field">
              <label class="label" for="nj-rows">{{ t('newjob.field.rows') }}</label>
              <input
                id="nj-rows"
                v-model.number="rows"
                class="control"
                type="number"
                min="1"
                placeholder="32"
              />
            </div>
            <div class="field span2">
              <label class="check">
                <input v-model="recordPty" type="checkbox" />
                <span>{{ t('newjob.field.record') }}</span>
              </label>
            </div>
          </template>

          <!-- tags：逗号分隔，提交解析为数组（E5 检索维度） -->
          <div class="field">
            <label class="label" for="nj-tags">{{ t('newjob.field.tags') }}</label>
            <input
              id="nj-tags"
              v-model="tags"
              class="control"
              spellcheck="false"
              autocomplete="off"
              :placeholder="t('newjob.tags.placeholder')"
            />
            <p class="field-hint">{{ t('newjob.tags.hint') }}</p>
          </div>

          <div class="field">
            <label class="label" for="nj-timeout">{{ t('newjob.field.timeout') }}</label>
            <input
              id="nj-timeout"
              v-model.number="timeoutSec"
              class="control"
              type="number"
              min="1"
              :placeholder="timeoutPlaceholder"
            />
          </div>

          <!-- 文件传输（XFER-01 X2）：上传 = 提交前先暂存（stage_only），执行机在 agent 起跑前
               把文件放到目标路径；收集 = job 结束后按 cwd 匹配的 glob，命中项进本 job 产物的
               collected/（详情页可直接预览/下载）。rebuild 走只发改动字段的通道，故不显示。 -->
          <template v-if="!isRebuild">
            <div class="field span2">
              <label class="label" for="nj-upload">{{ t('newjob.field.upload') }}</label>
              <input
                id="nj-upload"
                class="control file-input"
                type="file"
                multiple
                @change="onFilesPicked"
              />
              <div v-for="(row, i) in uploadRows" :key="`up-${i}`" class="upload-row">
                <span class="upload-name" :title="row.file.name">{{ row.file.name }}</span>
                <input
                  v-model="row.dest"
                  class="control upload-dest"
                  spellcheck="false"
                  autocomplete="off"
                  :placeholder="t('newjob.upload.dest')"
                />
                <button type="button" class="env-btn" @click="uploadRows.splice(i, 1)">{{ t('newjob.upload.remove') }}</button>
              </div>
              <p v-if="uploadRows.length > 0 && xferRunner !== ''" class="field-hint">
                {{ t('newjob.upload.staged', { runner: xferRunner }) }}
              </p>
              <p v-else-if="uploadRows.length > 0" class="field-hint field-hint--warn">
                {{ t('newjob.upload.needWorker') }}
              </p>
              <p v-else class="field-hint">{{ t('newjob.upload.hint') }}</p>
            </div>

            <div class="field span2">
              <label class="label" for="nj-collect">{{ t('newjob.field.collect') }}</label>
              <textarea
                id="nj-collect"
                v-model="collectText"
                class="control area area--short"
                rows="2"
                spellcheck="false"
                :placeholder="t('newjob.collect.placeholder')"
              ></textarea>
              <p class="field-hint">{{ t('newjob.collect.hint') }}</p>
            </div>
          </template>
        </div>

        <div v-if="isRebuild" class="field">
          <label class="label">{{ t('newjob.env.label') }}</label>
          <div v-if="envRows.length === 0 && envAdds.length === 0" class="field-hint">
            {{ t('newjob.env.none') }}
          </div>
          <div v-for="row in envRows" :key="row.key" class="env-row">
            <span class="env-key">{{ row.key }}</span>
            <template v-if="row.action === 'set'">
              <input v-model="row.value" class="control env-value-input" :placeholder="t('newjob.env.value')" />
              <button type="button" class="env-btn" @click="row.action = 'keep'; row.value = ''">{{ t('newjob.env.undo') }}</button>
            </template>
            <template v-else>
              <span class="env-val" :class="{ struck: row.action === 'unset' }">{{ t('newjob.env.keep') }}</span>
              <button type="button" class="env-btn" @click="row.action = 'set'">{{ t('newjob.env.change') }}</button>
              <button type="button" class="env-btn" @click="row.action = row.action === 'unset' ? 'keep' : 'unset'">
                {{ row.action === 'unset' ? t('newjob.env.restore') : t('newjob.env.delete') }}
              </button>
            </template>
          </div>
          <div v-for="(a, i) in envAdds" :key="'add' + i" class="env-row">
            <input v-model="a.key" class="control env-key-input" placeholder="KEY" />
            <input v-model="a.value" class="control env-value-input" placeholder="value" />
            <button type="button" class="env-btn" @click="envAdds.splice(i, 1)">{{ t('newjob.env.delete') }}</button>
          </div>
          <button type="button" class="env-add" @click="envAdds.push({ key: '', value: '' })">{{ t('newjob.env.add') }}</button>
          <p v-if="rebuildRedacted" class="field-hint field-hint--warn">
            {{ t('newjob.env.hint') }}
          </p>
        </div>

        <div class="form-grid">
          <div class="field span2">
            <label class="check">
              <input v-model="sync" type="checkbox" :disabled="continuousSession" />
              <span>{{ t('newjob.field.sync') }}</span>
            </label>
          </div>
        </div>
      </details>

      <p v-if="validationError" class="field-hint field-hint--warn">
        {{ validationError }}
      </p>
      <p v-if="ceilingReadOnly" class="field-hint field-hint--warn" data-test="nj-readonly">{{ CEILING_READ_ONLY_NOTICE }}</p>
      <p v-if="submitError" class="error">{{ submitError }}</p>
      <p v-if="notice" class="notice">{{ notice }}</p>

      <div v-if="submittedJobId" class="link-result" data-test="link-result">
        <p class="link-line">job <RouterLink :to="`/jobs/${encodeURIComponent(submittedJobId)}`" data-test="link-job">{{ submittedJobId }}</RouterLink> — {{ t('newjob.link.job', { id: submittedJobId }) }}</p>
        <p v-if="itemLinked" class="link-line link-line--ok" data-test="link-ok">{{ t('newjob.link.ok', { id: fromItem }) }}</p>
        <template v-else>
          <p class="link-line link-line--err" data-test="link-error">{{ t('newjob.link.error', { err: linkError || t('newjob.link.unknown') }) }}</p>
          <button class="submit link-retry" type="button" :disabled="linking" data-test="link-retry" @click="linkSubmittedJob">{{ linking ? t('newjob.link.retrying') : t('newjob.link.retry') }}</button>
        </template>
      </div>
      <button v-else class="submit" type="submit" :disabled="!canSubmit">
        {{ submitting ? t('newjob.submitting') : isRebuild ? t('newjob.submitRebuild') : t('newjob.submit') }}
      </button>
    </form>
  </div>
</template>

<style scoped>
/* 新建任务：常用字段两栏、高级选项折叠（设计稿 §7.1）。手机单栏，提交按钮吸底。 */
.newjob {
  max-width: 1080px;
  margin: 0 auto;
  padding-bottom: var(--s6);
}
.newjob-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  margin-bottom: var(--s3);
}
.title {
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--ink);
}
.hint,
.field-hint {
  margin: 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.field-hint--warn {
  color: var(--need-ink);
}
.error {
  color: var(--fail);
  font-size: var(--fs-sm);
  border: 1px solid var(--fail);
  border-radius: var(--r-ctl);
  padding: var(--s2);
  margin: 0 0 var(--s2);
  overflow-wrap: anywhere;
}
.notice {
  color: var(--done);
  font-size: var(--fs-sm);
  margin: 0 0 var(--s2);
}
.card {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  padding: var(--s4);
}
.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--s3);
  align-items: start;
}
.span2 {
  grid-column: 1 / -1;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.label {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.control {
  min-height: 34px;
  width: 100%;
  padding: 6px var(--s2);
  background: var(--surface-2);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
}
.control:focus-visible {
  border-color: var(--accent);
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.control:disabled {
  opacity: 0.6;
}
.area {
  min-height: 64px;
  resize: vertical;
  font-family: var(--font-mono);
}
.area--short {
  min-height: 48px;
}
.control--redacted {
  border-color: var(--need);
  background: var(--need-soft);
}
.check {
  display: flex;
  align-items: flex-start;
  gap: var(--s2);
  color: var(--ink-2);
  font-size: var(--fs-sm);
  line-height: 1.4;
}
.check input {
  margin-top: 3px;
  accent-color: var(--accent);
  flex: none;
}
.advanced,
.mobile-advanced {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  background: var(--surface-2);
  padding: var(--s2) var(--s3);
}
.advanced summary,
.mobile-advanced summary {
  cursor: pointer;
  color: var(--ink-2);
  font-size: var(--fs-sm);
  min-height: 32px;
  display: flex;
  align-items: center;
}
.advanced-body {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  padding-top: var(--s2);
}
.mobile-advanced > .form-grid,
.mobile-advanced > .field {
  margin-top: var(--s3);
}
.seg {
  display: inline-flex;
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  overflow: hidden;
  width: fit-content;
}
.seg-btn {
  min-height: 30px;
  padding: 0 var(--s3);
  background: transparent;
  color: var(--ink-3);
  border: 0;
  border-right: 1px solid var(--line);
  font: inherit;
  font-size: var(--fs-sm);
}
.seg-btn:last-child {
  border-right: 0;
}
.seg-btn--on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}
.tmpl-vars {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  margin-top: var(--s2);
}
.tmpl-var {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.tmpl-preview {
  margin: 0;
  max-height: 260px;
  overflow: auto;
  padding: var(--s2);
  background: var(--sunken);
  color: var(--ink-2);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.upload-row {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin-top: var(--s2);
  min-width: 0;
}
.upload-name {
  flex: 0 1 30%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  color: var(--ink-2);
}
.upload-dest {
  flex: 1;
}
.file-input {
  padding: 4px;
}
.env-row {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin-top: var(--s2);
  min-width: 0;
}
.env-key {
  min-width: 8ch;
  color: var(--ink-2);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
}
.env-key-input {
  max-width: 180px;
}
.env-value-input {
  flex: 1;
}
.env-val {
  flex: 1;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.env-val.struck {
  text-decoration: line-through;
}
.env-btn,
.lock-chip {
  min-height: 28px;
  padding: 0 var(--s2);
  background: transparent;
  color: var(--accent);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-xs);
  white-space: nowrap;
}
.env-add {
  margin-top: var(--s2);
  background: none;
  border: 1px dashed var(--line-strong);
  border-radius: var(--r-ctl);
  color: var(--accent);
  font: inherit;
  font-size: var(--fs-sm);
  padding: var(--s1) var(--s2);
}
.lock-chip {
  margin-left: 4px;
}
.redacted-banner,
.from-session-banner {
  border: 1px solid var(--need);
  background: var(--need-soft);
  color: var(--need-ink);
  border-radius: var(--r-ctl);
  padding: var(--s2) var(--s3);
  font-size: var(--fs-sm);
  overflow-wrap: anywhere;
}
.from-item-note {
  display: block;
  color: var(--ink-2);
  font-size: var(--fs-xs);
}
.link-result {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  padding: var(--s2) var(--s3);
  font-size: var(--fs-sm);
}
.link-line {
  margin: 2px 0;
}
.link-line--ok {
  color: var(--done);
}
.link-line--err {
  color: var(--fail);
}
.submit {
  align-self: flex-start;
  min-height: 38px;
  padding: 0 var(--s5);
  background: var(--accent);
  color: var(--accent-ink);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  font-weight: 600;
}
.submit:hover:not(:disabled) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.submit:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.link-retry {
  margin-top: var(--s1);
}
/* 手机：单栏，工具条与提交按钮吸底（保留 env(safe-area-inset-bottom) 避让）。 */
@media (max-width: 760px) {
  .card {
    padding: var(--s3) var(--s3) calc(var(--s5) + env(safe-area-inset-bottom));
  }
  .form-grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .control,
  .seg-btn,
  .submit,
  .env-btn,
  .lock-chip {
    min-height: 40px;
  }
  .submit {
    position: sticky;
    bottom: 0;
    width: 100%;
    padding-bottom: env(safe-area-inset-bottom);
    z-index: 5;
  }
}
</style>
