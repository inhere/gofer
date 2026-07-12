<script setup lang="ts">
// 新建 job 表单（G4，design §6.4 / P3-b）：
//  - project 下拉 -> 联动限定可选 agent / runner（按 project.allowed_*）
//  - agent=cli-agent 显 prompt 文本域（可贴 md）；agent=exec 显 command 输入（空格分词为 cmd[]）
//  - runner=worker -> 二选一：指定 worker_id（connected）或 worker_labels（逗号），默认折叠高级项
//  - cwd（默认 .）/ title / timeout / sync 勾选
// 提交成功跳详情；202（仍在后台）提示后仍跳详情（详情页自有 SSE 续看）。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getJobRequest, getMeta, rebuildJob, submitJob } from '../api/client'
import type { MetaAgent, MetaProject, MetaRunner, MetaWorker, RebuildBody } from '../api/types'

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

// 表单字段
const projectKey = ref('')
const agentKey = ref('')
const runnerName = ref('')
const prompt = ref('')
const command = ref('')
// per-job cli-agent flags（xu64.12 §14）：每行一个完整 argv 元素，追加到 agent argv 末尾。
const agentArgs = ref('')
const cwd = ref('.')
const title = ref('')
const tags = ref('')
const timeoutSec = ref<number | null>(null)
const sync = ref(false)
const interactive = ref(false)
const recordPty = ref(false)
const cols = ref(120)
const rows = ref(32)
const sessionMode = computed(() => route.query.mode === 'session' || route.query.interactive === '1')
const rebuildFrom = computed(() => (typeof route.query.from === 'string' ? route.query.from : ''))
const isRebuild = computed(() => rebuildFrom.value !== '')
const rebuildRedacted = ref(false)
const planId = ref('')   // 隐藏：rebuild 继承/可覆盖源 plan_id（若已有则复用）
const promptLabel = computed(() => (interactive.value ? 'PROMPT（可选）' : 'PROMPT（可贴 markdown）'))
const promptPlaceholder = computed(() =>
  interactive.value
    ? '可留空；填写后会作为系统提示打开会话'
    : '描述任务，正文即 prompt...',
)
const timeoutPlaceholder = computed(() =>
  interactive.value ? '不填则无超时' : '不填则默认 300s',
)

// runner=worker 高级项
const advancedOpen = ref(false)
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
    const m = await getMeta()
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

// agent key -> type 索引
const agentTypeOf = computed<Record<string, string>>(() => {
  const m: Record<string, string> = {}
  for (const a of agents.value) {
    m[a.key] = a.type
  }
  return m
})

// runner name -> type 索引
const runnerTypeOf = computed<Record<string, string>>(() => {
  const m: Record<string, string> = {}
  for (const r of runners.value) {
    m[r.name] = r.type
  }
  return m
})

const selectedProject = computed<MetaProject | undefined>(() =>
  projects.value.find((p) => p.key === projectKey.value),
)

// 联动：project 选定后，agent 先取该 project 的 allowed_agents 交集（allowlist 为空=全集）；
// 再叠加联邦(G3)能力交集——worker runner 选定具体 worker 后只列该 worker 上报的 agent；
// interactive 模式只列 interactive-capable。两处收窄都 fail-safe：交集为空则回落上一层，绝不清空到不可选。
const agentOptions = computed<MetaAgent[]>(() => {
  let list = agents.value
  const allowed = selectedProject.value?.allowed_agents ?? []
  if (allowed.length > 0) {
    const set = new Set(allowed)
    list = list.filter((a) => set.has(a.key))
  }
  // 联邦：选定 worker 后按其上报能力收窄（无能力上报/交集为空则不收窄——T5.3b fail-safe）
  const wkeys = workerAgentKeys.value
  if (wkeys) {
    const narrowed = list.filter((a) => wkeys.has(a.key))
    if (narrowed.length > 0) {
      list = narrowed
    }
  }
  // interactive 模式只列 interactive-capable（全空则回落，避免锁死表单——见 P5 报告）
  if (interactive.value) {
    const ia = list.filter((a) => a.interactive)
    if (ia.length > 0) {
      list = ia
    }
  }
  return list
})

const runnerOptions = computed<MetaRunner[]>(() => {
  const allowed = selectedProject.value?.allowed_runners ?? []
  if (allowed.length === 0) {
    return runners.value
  }
  const set = new Set(allowed)
  return runners.value.filter((r) => set.has(r.name))
})

// 当前 agent / runner 类型
const agentType = computed(() => agentTypeOf.value[agentKey.value] ?? '')
const isExec = computed(() => agentType.value === 'exec')
const isCliAgent = computed(() => agentType.value !== '' && agentType.value !== 'exec')
const runnerType = computed(() => runnerTypeOf.value[runnerName.value] ?? '')
const isWorkerRunner = computed(() => runnerType.value === 'worker')

// connected 的 worker（指定 worker_id 下拉用）
const connectedWorkers = computed<MetaWorker[]>(() =>
  workers.value.filter((w) => w.connected),
)

// 联邦级联(G3)：worker runner 且「指定 worker」模式选定 worker 时，取该 worker 条目用于能力收窄。
// labels 模式（可匹配多台、能力可能不同）与未选时返回 undefined → 不收窄（fail-safe 用全量）。
const selectedWorker = computed<MetaWorker | undefined>(() => {
  if (!isWorkerRunner.value || workerMode.value !== 'id' || workerId.value === '') {
    return undefined
  }
  return workers.value.find((w) => w.id === workerId.value)
})

// 选定 worker 上报的 agent key 集合：优先 typed agent_caps，回落 bare agents[]。
// 都为空（离线/旧 worker 未上报能力）返回 null = 不收窄（T5.3b fail-safe，绝不清空下拉）。
const workerAgentKeys = computed<Set<string> | null>(() => {
  const w = selectedWorker.value
  if (!w) {
    return null
  }
  const caps = w.agent_caps ?? []
  if (caps.length > 0) {
    return new Set(caps.map((c) => c.key))
  }
  const keys = w.agents ?? []
  if (keys.length > 0) {
    return new Set(keys)
  }
  return null
})

// project 下拉。选定具体 worker → 列该 worker 上报的 projects（含仅该 worker 定义的 worker-only 项）；
// 未选 worker（local/peer/labels）或交集为空 → 回落 HOST-only（worker-only 项无 worker 上下文不能本地跑，
// 必须排除；绝不回落到含 worker-only 的全量——T5.3b fail-safe）。
const projectOptions = computed<MetaProject[]>(() => {
  const hostOnly = projects.value.filter((p) => !p.worker_only)
  const w = selectedWorker.value
  if (!w) {
    return hostOnly
  }
  const wp = w.projects ?? []
  if (wp.length > 0) {
    const set = new Set(wp)
    const narrowed = projects.value.filter((p) => set.has(p.key))
    if (narrowed.length > 0) {
      return narrowed
    }
  }
  return hostOnly
})

// T5.3a：选了 worker runner 但尚未指定具体 worker（id 模式）→ 提示先选 worker（下拉暂用全量，不锁死）。
const workerNarrowingPending = computed(
  () => isWorkerRunner.value && workerMode.value === 'id' && workerId.value === '',
)

function selectProject(key: string) {
  projectKey.value = key
  // project 切换后，把 agent/runner 收敛到新 allowlist 内的首项（或保留仍合法的选择）
  const ags = agentOptions.value
  if (!ags.some((a) => a.key === agentKey.value)) {
    const def = selectedProject.value?.default_agent
    agentKey.value =
      def && ags.some((a) => a.key === def) ? def : ags.length > 0 ? ags[0].key : ''
  }
  const rns = runnerOptions.value
  if (!rns.some((r) => r.name === runnerName.value)) {
    runnerName.value = rns.length > 0 ? rns[0].name : ''
  }
}

// project select change 必须用最新 allowlist 重算，故包一层
function onProjectChange() {
  selectProject(projectKey.value)
}

// T5.2：worker/runner 变更后，把 project/agent 收敛到（可能被 worker 收窄的）合法选项内，
// 不留悬空非法选择。切到 local 时 projectOptions 排除 worker-only → 悬空的 worker-only 选择被丢弃：
// 有 host 项则收敛到首个（selectProject 内再收 agent/runner），无则清空 projectKey（不崩）；否则只补收 agent。
function reconvergeToWorker() {
  const projs = projectOptions.value
  if (!projs.some((p) => p.key === projectKey.value)) {
    if (projs.length > 0) {
      selectProject(projs[0].key)
      return
    }
    projectKey.value = '' // 无合法 project（仅 worker-only 且当前无 worker 上下文）→ 清空，交给校验兜底
  }
  const ags = agentOptions.value
  if (ags.length > 0 && !ags.some((a) => a.key === agentKey.value)) {
    const def = selectedProject.value?.default_agent
    agentKey.value = def && ags.some((a) => a.key === def) ? def : ags[0].key
  }
}

// 校验 + 组装请求
const validationError = computed<string>(() => {
  if (!projectKey.value) {
    return '请选择 project'
  }
  if (!agentKey.value) {
    return '请选择 agent'
  }
  if (!runnerName.value) {
    return '请选择 runner'
  }
  if (isCliAgent.value && !interactive.value && prompt.value.trim() === '') {
    return 'cli-agent 需填写 prompt'
  }
  if (isExec.value && command.value.trim() === '') {
    return 'exec 需填写 command'
  }
  if (isWorkerRunner.value) {
    if (workerMode.value === 'id' && workerId.value === '') {
      return 'runner=worker：请指定 worker，或切换为按标签自动'
    }
    if (workerMode.value === 'labels' && workerLabels.value.trim() === '') {
      return 'runner=worker：请填写 worker_labels，或切换为指定 worker'
    }
  }
  return ''
})

const canSubmit = computed(() => !submitting.value && validationError.value === '')

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
      sync: sync.value,
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
      // per-job agent flags：仅 cli-agent 生效（exec 由后端拒绝 exec+agent_args）。
      const aa = parseAgentArgs(agentArgs.value)
      if (aa.length > 0) {
        req.agent_args = aa
      }
    }
    if (isExec.value) {
      req.cmd = parseCmd(command.value)
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
    if (isWorkerRunner.value) {
      if (workerMode.value === 'id') {
        req.worker_id = workerId.value
      } else {
        req.worker_labels = parseLabels(workerLabels.value)
      }
    }
    const { job, async } = await submitJob(req)
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
    if (r.interactive) interactive.value = true
    if (r.system_prompt) prompt.value = r.system_prompt
    else if (r.prompt) prompt.value = r.prompt             // 可能含占位；用户改则校验、不改则不发
    if (r.cmd && r.cmd.length) command.value = r.cmd.join(' ')
    if (r.agent_args && r.agent_args.length) agentArgs.value = r.agent_args.join('\n')
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
    tags: tags.value, timeout: timeoutSec.value, interactive: interactive.value,
    cols: cols.value, rows: rows.value, worker_id: workerId.value,
    worker_labels: workerLabels.value, plan_id: planId.value,
    agent_args: agentArgs.value,
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
  }
  if (isExec.value) chg('command', command.value, () => (b.cmd = parseCmd(command.value)))
  chg('cwd', cwd.value, (v) => (b.cwd = v))
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
  if (sessionMode.value) {
    interactive.value = true
  }
  await loadMeta()
  if (isRebuild.value) {
    await prefillFrom(rebuildFrom.value)
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
  <div class="newjob">
    <div class="newjob-head">
      <RouterLink to="/board" class="back mono">← board</RouterLink>
      <h1 class="title mono">{{ isRebuild ? '快速重建' : '新建 job' }}</h1>
    </div>

    <p v-if="loadError" class="error mono">表单选项加载失败：{{ loadError }}</p>
    <p v-else-if="loading" class="hint mono">加载选项中…</p>

    <form v-else class="card" @submit.prevent="onSubmit">
      <div v-if="isRebuild && rebuildRedacted" class="redacted-banner mono">
        部分字段含已脱敏的占位值，提交前必须替换；未改字段不会提交，将沿用源 job 原值。
      </div>

      <!-- project -->
      <div class="field">
        <label class="label mono" for="nj-project">PROJECT</label>
        <select
          id="nj-project"
          v-model="projectKey"
          class="control mono"
          @change="onProjectChange"
        >
          <option v-for="p in projectOptions" :key="p.key" :value="p.key">{{ p.key }}</option>
        </select>
        <p v-if="projectOptions.length === 0" class="field-hint mono">无可用 project</p>
      </div>

      <!-- agent / runner 两列 -->
      <div class="row">
        <div class="field">
          <label class="label mono" for="nj-agent">AGENT</label>
          <select id="nj-agent" v-model="agentKey" class="control mono">
            <option v-for="a in agentOptions" :key="a.key" :value="a.key">
              {{ a.key }} · {{ a.type }}
            </option>
          </select>
        </div>
        <div class="field">
          <label class="label mono" for="nj-runner">RUNNER</label>
          <select
            id="nj-runner"
            v-model="runnerName"
            class="control mono"
            @change="reconvergeToWorker"
          >
            <option v-for="r in runnerOptions" :key="r.name" :value="r.name">
              {{ r.name }} · {{ r.type }}
            </option>
          </select>
        </div>
      </div>

      <p v-if="workerNarrowingPending" class="field-hint mono">
        runner=worker：请在下方「worker 选机」指定具体 worker，agent / project 将按其上报能力收窄
      </p>

      <!-- cli-agent: prompt 文本域 -->
      <div v-if="isCliAgent" class="field">
        <label class="label mono" for="nj-prompt">{{ promptLabel }}</label>
        <textarea
          id="nj-prompt"
          v-model="prompt"
          class="control mono area"
          :class="{ 'control--redacted': promptRedacted }"
          rows="8"
          spellcheck="false"
          :placeholder="promptPlaceholder"
        ></textarea>
        <p v-if="promptRedacted" class="field-hint field-hint--warn mono">
          该字段含脱敏占位；如需改动，提交前必须替换。
        </p>
        <p v-if="interactive" class="field-hint mono">
          如果写入会作为系统提示打开会话
        </p>
      </div>

      <!-- cli-agent: per-job agent flags（xu64.12 §14），每行一个完整参数 -->
      <div v-if="isCliAgent" class="field">
        <label class="label mono" for="nj-agent-args">AGENT ARGS（每行一个，追加到 agent argv 末尾）</label>
        <textarea
          id="nj-agent-args"
          v-model="agentArgs"
          class="control mono area"
          rows="3"
          spellcheck="false"
          placeholder="每行一个完整参数，例如：&#10;--dangerously-skip-permissions"
        ></textarea>
        <p class="field-hint mono">仅 cli-agent 生效（exec 忽略）；不做空格分词，一行即一个 argv 元素。</p>
      </div>

      <!-- exec: command 输入 -->
      <div v-else-if="isExec" class="field">
        <label class="label mono" for="nj-cmd">COMMAND（空格分词为 argv）</label>
        <input
          id="nj-cmd"
          v-model="command"
          class="control mono"
          :class="{ 'control--redacted': commandRedacted }"
          spellcheck="false"
          autocomplete="off"
          placeholder="echo hello"
        />
        <p v-if="commandRedacted" class="field-hint field-hint--warn mono">
          该字段含脱敏占位；如需改动，提交前必须替换。
        </p>
      </div>

      <!-- runner=worker 高级项：二选一 -->
      <details v-if="isWorkerRunner" class="advanced" :open="advancedOpen">
        <summary class="mono" @click.prevent="advancedOpen = !advancedOpen">
          worker 选机（必填二选一）
        </summary>
        <div class="advanced-body">
          <div class="seg mono">
            <button
              type="button"
              class="seg-btn"
              :class="{ 'seg-btn--on': workerMode === 'id' }"
              @click="workerMode = 'id'"
            >
              指定 worker
            </button>
            <button
              type="button"
              class="seg-btn"
              :class="{ 'seg-btn--on': workerMode === 'labels' }"
              @click="workerMode = 'labels'"
            >
              按标签自动
            </button>
          </div>

          <div v-if="workerMode === 'id'" class="field">
            <label class="label mono" for="nj-wid">WORKER_ID（仅 connected）</label>
            <select id="nj-wid" v-model="workerId" class="control mono" @change="reconvergeToWorker">
              <option value="" disabled>选择一个已连接 worker</option>
              <option v-for="w in connectedWorkers" :key="w.id" :value="w.id">
                {{ w.id }}<template v-if="w.labels && w.labels.length"> · {{ w.labels.join(',') }}</template>
              </option>
            </select>
            <p v-if="connectedWorkers.length === 0" class="field-hint mono">
              无已连接 worker，可改用「按标签自动」或先连接 worker
            </p>
          </div>

          <div v-else class="field">
            <label class="label mono" for="nj-labels">WORKER_LABELS（逗号分隔）</label>
            <input
              id="nj-labels"
              v-model="workerLabels"
              class="control mono"
              spellcheck="false"
              autocomplete="off"
              placeholder="gpu, linux"
            />
            <p class="field-hint mono">worker 须包含全部标签（AND 匹配）</p>
          </div>
        </div>
      </details>

      <!-- cwd / title / timeout -->
      <div class="row">
        <div class="field">
          <label class="label mono" for="nj-cwd">CWD</label>
          <input
            id="nj-cwd"
            v-model="cwd"
            class="control mono"
            :class="{ 'control--redacted': cwdRedacted }"
            spellcheck="false"
            autocomplete="off"
            placeholder="."
          />
          <p v-if="cwdRedacted" class="field-hint field-hint--warn mono">
            该字段含脱敏占位；如需改动，提交前必须替换。
          </p>
        </div>
        <div class="field">
          <label class="label mono" for="nj-title">TITLE（可选）</label>
          <input
            id="nj-title"
            v-model="title"
            class="control mono"
            autocomplete="off"
            placeholder="人类可读任务名"
          />
        </div>
      </div>

      <!-- interactive pty：通常配合 runner=worker，本地/准入规则由后端最终校验。 -->
      <div class="field">
        <label class="check mono">
          <input v-model="interactive" type="checkbox" />
          <span>交互式（pty，可在详情页接入终端；通常需 runner=worker）</span>
        </label>
      </div>

      <div v-if="interactive" class="row">
        <div class="field">
          <label class="label mono" for="nj-cols">COLS</label>
          <input
            id="nj-cols"
            v-model.number="cols"
            class="control mono"
            type="number"
            min="1"
            placeholder="120"
          />
        </div>
        <div class="field">
          <label class="label mono" for="nj-rows">ROWS</label>
          <input
            id="nj-rows"
            v-model.number="rows"
            class="control mono"
            type="number"
            min="1"
            placeholder="32"
          />
        </div>
      </div>

      <div v-if="interactive" class="field">
        <label class="check mono">
          <input v-model="recordPty" type="checkbox" />
          <span>录制终端（保存 asciinema 回放；需要服务端开启 storage.cast.enabled）</span>
        </label>
      </div>

      <!-- tags：逗号分隔，提交解析为数组（E5 检索维度） -->
      <div class="field">
        <label class="label mono" for="nj-tags">TAGS（逗号分隔，可选）</label>
        <input
          id="nj-tags"
          v-model="tags"
          class="control mono"
          spellcheck="false"
          autocomplete="off"
          placeholder="ci, nightly"
        />
        <p class="field-hint mono">自由标签，提交后可按 tag 检索 / 行内徽标展示</p>
      </div>

      <div v-if="isRebuild" class="field">
        <label class="label mono">ENV（源 job 继承；值保留在服务端）</label>
        <div v-if="envRows.length === 0 && envAdds.length === 0" class="field-hint mono">
          源 job 无 env key；可新增覆盖项。
        </div>
        <div v-for="row in envRows" :key="row.key" class="env-row mono">
          <span class="env-key">{{ row.key }}</span>
          <template v-if="row.action === 'set'">
            <input v-model="row.value" class="control mono env-value-input" placeholder="新值" />
            <button type="button" class="env-btn" @click="row.action = 'keep'; row.value = ''">撤销</button>
          </template>
          <template v-else>
            <span class="env-val" :class="{ struck: row.action === 'unset' }">••••（保留原值）</span>
            <button type="button" class="env-btn" @click="row.action = 'set'">改值</button>
            <button type="button" class="env-btn" @click="row.action = row.action === 'unset' ? 'keep' : 'unset'">
              {{ row.action === 'unset' ? '恢复' : '删除' }}
            </button>
          </template>
        </div>
        <div v-for="(a, i) in envAdds" :key="'add' + i" class="env-row mono">
          <input v-model="a.key" class="control mono env-key-input" placeholder="KEY" />
          <input v-model="a.value" class="control mono env-value-input" placeholder="value" />
          <button type="button" class="env-btn" @click="envAdds.splice(i, 1)">删除</button>
        </div>
        <button type="button" class="env-add mono" @click="envAdds.push({ key: '', value: '' })">+ 新增 env</button>
        <p v-if="rebuildRedacted" class="field-hint field-hint--warn mono">
          源 job 的 env / 命令含敏感值，已在服务端保留；仅改动过的字段会提交，未改字段沿用原值
        </p>
      </div>

      <div class="row">
        <div class="field">
          <label class="label mono" for="nj-timeout">TIMEOUT_SEC（可选）</label>
          <input
            id="nj-timeout"
            v-model.number="timeoutSec"
            class="control mono"
            type="number"
            min="1"
            :placeholder="timeoutPlaceholder"
          />
        </div>
        <div class="field field--check">
          <label class="check mono">
            <input v-model="sync" type="checkbox" />
            <span>sync（同步等待终态再返回；超服务端上限退回后台）</span>
          </label>
        </div>
      </div>

      <p v-if="validationError" class="field-hint field-hint--warn mono">
        {{ validationError }}
      </p>
      <p v-if="submitError" class="error mono">{{ submitError }}</p>
      <p v-if="notice" class="notice mono">{{ notice }}</p>

      <button class="submit" type="submit" :disabled="!canSubmit">
        {{ submitting ? '提交中…' : isRebuild ? '提交重建' : '提交 job' }}
      </button>
    </form>
  </div>
</template>

<style scoped>
.newjob {
  max-width: 760px;
  margin: 0 auto;
}
.newjob-head {
  display: flex;
  align-items: baseline;
  gap: 16px;
  margin-bottom: 14px;
}
.back {
  font-size: 13px;
  color: var(--queue);
}
.back:hover {
  color: var(--phosphor);
}
.title {
  font-size: 16px;
  color: var(--paper);
  margin: 0;
}

.hint {
  color: var(--queue);
  font-size: 13px;
}

.card {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.row {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}
.row > .field {
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
.control--redacted {
  border-color: var(--run);
}
.area {
  resize: vertical;
  min-height: 120px;
}
select.control {
  cursor: pointer;
}

.field-hint {
  color: var(--queue);
  font-size: 11px;
  margin: 6px 0 0;
}
.field-hint--warn {
  color: var(--run);
}

.redacted-banner {
  color: var(--run);
  background: var(--ink);
  border: 1px solid var(--run);
  border-radius: var(--radius);
  padding: 9px 11px;
  font-size: 12px;
}

.advanced {
  background: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px 12px;
}
.advanced > summary {
  cursor: pointer;
  color: var(--phosphor);
  font-size: 12px;
  letter-spacing: 0.04em;
  list-style: revert;
}
.advanced-body {
  margin-top: 12px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.seg {
  display: inline-flex;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
  width: fit-content;
}
.seg-btn {
  background: transparent;
  color: var(--queue);
  border: none;
  padding: 6px 14px;
  font-size: 12px;
}
.seg-btn:hover {
  color: var(--paper);
}
.seg-btn--on {
  background: var(--phosphor);
  color: var(--ink);
}

.check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--paper);
  cursor: pointer;
}
.check input {
  accent-color: var(--phosphor);
}

.env-row {
  display: grid;
  grid-template-columns: minmax(120px, 0.7fr) minmax(180px, 1fr) auto auto;
  gap: 8px;
  align-items: center;
  background: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px;
  margin-top: 8px;
}
.env-key {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.env-val {
  color: var(--queue);
  font-size: 12px;
}
.env-val.struck {
  color: var(--fail);
  text-decoration: line-through;
}
.env-key-input,
.env-value-input {
  padding: 6px 8px;
}
.env-btn,
.env-add {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 5px 10px;
  font-size: 12px;
}
.env-btn:hover,
.env-add:hover {
  background: var(--phosphor);
  color: var(--ink);
}
.env-add {
  margin-top: 8px;
  align-self: flex-start;
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
}
.notice {
  color: var(--done);
  font-size: 12px;
  margin: 0;
}

.submit {
  margin-top: 4px;
  background: var(--phosphor);
  color: var(--ink);
  border: none;
  border-radius: var(--radius);
  padding: 10px 16px;
  font-size: 14px;
  font-weight: 600;
  align-self: flex-start;
}
.submit:disabled {
  opacity: 0.55;
  cursor: default;
}

@media (max-width: 560px) {
  .row {
    flex-direction: column;
    gap: 16px;
  }
  .env-row {
    grid-template-columns: 1fr;
  }
}
</style>
