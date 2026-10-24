<script setup lang="ts">
// 新建调度（AUTO-02）：在「新建 job」表单基础上加 name + 触发配置 + enabled，
// 去掉 sync（调度触发的 job 由 serve 循环异步提交，channel=cron）。
//  - project 下拉 -> 联动限定可选 agent / runner（按 project.allowed_*）
//  - agent=exec 显 command 输入；否则显 prompt 文本域
//  - runner=worker -> 指定 worker_id 或 worker_labels
//  - cron 用标准 5 段表达式，也支持 @hourly/@daily/@every 描述符（robfig/cron 标准解析）
// 提交成功跳 /schedules。
import { runnerOptionText } from '../utils/runnerDisplay'
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { t } from '../i18n'
import BackLink from '../ui/BackLink.vue'
import SearchSelect from '../ui/SearchSelect.vue'
import type { SearchOption } from '../ui/searchSelect'
import { createSchedule } from '../api/client'
import { getMetaCached } from '../api/metaCache'
import type {
  CreateScheduleReq,
  MetaAgent,
  MetaProject,
  MetaRunner,
  MetaWorker,
  ScheduleParam,
  SubmitJobReq,
} from '../api/types'

const router = useRouter()

const loading = ref(true)
const loadError = ref('')
const submitting = ref(false)
const submitError = ref('')

const projects = ref<MetaProject[]>([])
const agents = ref<MetaAgent[]>([])
const runners = ref<MetaRunner[]>([])
const workers = ref<MetaWorker[]>([])

// 调度专属字段
const name = ref('')
const triggerType = ref<'cron' | 'once'>('cron')
const cron = ref('')
const catchUp = ref(true)
const enabled = ref(true)
const onceMode = ref<'delay' | 'at'>('delay')
const delayValue = ref<number | null>(30)
const delayUnit = ref<'s' | 'm' | 'h'>('s')
const runAtLocal = ref('')

// 参数声明（设计 2026-10-22 §D1）：一行一个，值作为 job 的环境变量。
const params = ref<ScheduleParam[]>([])

function addParamRow(): void {
  params.value = [...params.value, { name: '', type: 'string', default: '', desc: '' }]
}

function removeParamRow(i: number): void {
  params.value = params.value.filter((_, idx) => idx !== i)
}

function setParamField(i: number, key: 'name' | 'type' | 'default' | 'desc', value: string): void {
  params.value = params.value.map((p, idx) => (idx === i ? { ...p, [key]: value } : p))
}

function setParamChoices(i: number, value: string): void {
  const choices = value
    .split(',')
    .map((c) => c.trim())
    .filter((c) => c !== '')
  params.value = params.value.map((p, idx) =>
    idx === i ? { ...p, choices: choices.length > 0 ? choices : undefined } : p,
  )
}

// declaredParams 是提交用的声明：去掉名字为空的行，并去掉空的可选值列表。
function declaredParams(): ScheduleParam[] {
  return params.value
    .filter((p) => p.name.trim() !== '')
    .map((p) => {
      const out: ScheduleParam = { name: p.name.trim(), type: p.type || 'string', default: p.default ?? '' }
      if (p.desc) {
        out.desc = p.desc
      }
      if (p.type === 'choice' && p.choices && p.choices.length > 0) {
        out.choices = p.choices
      }
      return out
    })
}

// job 请求字段
const projectKey = ref('')
const agentKey = ref('')
const runnerName = ref('')
const prompt = ref('')
const command = ref('')
const cwd = ref('.')
const title = ref('')
const tags = ref('')
const timeoutSec = ref<number | null>(null)

// runner=worker 高级项
const advancedOpen = ref(false)
const workerMode = ref<'id' | 'labels'>('id')
const workerId = ref('')
const workerLabels = ref('')

// 常用 cron 预设（快速填充）；文案走 i18n。
const cronPresets = computed<Array<{ expr: string; label: string }>>(() => [
  { expr: '*/5 * * * *', label: t('schedules.new.preset5m') },
  { expr: '0 * * * *', label: t('schedules.new.presetHourly') },
  { expr: '0 9 * * *', label: t('schedules.new.presetDaily9') },
  { expr: '0 9 * * 1', label: t('schedules.new.presetMonday9') },
  { expr: '@daily', label: t('schedules.new.presetMidnight') },
])

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

const agentTypeOf = computed<Record<string, string>>(() => {
  const m: Record<string, string> = {}
  for (const a of agents.value) {
    m[a.key] = a.type
  }
  return m
})

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

// project 选定后 agent 先取 allowed_agents 交集（空=全集）；再叠加联邦(G3)能力交集——
// worker runner 选定具体 worker 后只列该 worker 上报的 agent。收窄 fail-safe：交集为空则回落，绝不清空。
// 注：调度表单无 interactive 开关（区别于 NewJob）→ 触发的 job 恒为非交互，故 interactive-only 的
// agent 必须排除：后端 validate 会拒（interactive-only），列出来只会得到一个到点必失败的调度。
const agentOptions = computed<MetaAgent[]>(() => {
  let list = agents.value.filter((a) => !a.interactive)
  const allowed = selectedProject.value?.allowed_agents ?? []
  if (allowed.length > 0) {
    const set = new Set(allowed)
    list = list.filter((a) => set.has(a.key))
  }
  const wkeys = workerAgentKeys.value
  if (wkeys) {
    const narrowed = list.filter((a) => wkeys.has(a.key))
    if (narrowed.length > 0) {
      list = narrowed
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

const agentType = computed(() => agentTypeOf.value[agentKey.value] ?? '')
const isExec = computed(() => agentType.value === 'exec')
const isCliAgent = computed(() => agentType.value !== '' && agentType.value !== 'exec')
const runnerType = computed(() => runnerTypeOf.value[runnerName.value] ?? '')
const isWorkerRunner = computed(() => runnerType.value === 'worker')

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

// 可搜索的项目下拉（issue gofer-b8nh）。
const projectItems = computed<SearchOption[]>(() =>
  projectOptions.value.map((p) => ({ value: p.key, label: p.key })),
)

// T5.3a：选了 worker runner 但尚未指定具体 worker（id 模式）→ 提示先选 worker（下拉暂用全量，不锁死）。
const workerNarrowingPending = computed(
  () => isWorkerRunner.value && workerMode.value === 'id' && workerId.value === '',
)

function selectProject(key: string) {
  projectKey.value = key
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

const validationError = computed<string>(() => {
  if (name.value.trim() === '') {
    return t('schedules.new.needName')
  }
  if (triggerType.value === 'cron' && cron.value.trim() === '') {
    return t('schedules.new.needCron')
  }
  if (triggerType.value === 'once') {
    if (cron.value.trim() !== '') {
      return t('schedules.new.noCronForOnce')
    }
    const now = Math.floor(Date.now() / 1000)
    if (onceMode.value === 'delay') {
      const sec = delaySeconds.value
      if (sec <= 0) {
        return t('schedules.new.needDelay')
      }
      if (now + sec < now + 3) {
        return t('schedules.new.minDelay')
      }
    } else {
      const sec = runAtSeconds.value
      if (sec <= 0) {
        return t('schedules.new.needRunAt')
      }
      if (sec < now + 3) {
        return t('schedules.new.minRunAt')
      }
    }
  }
  if (!projectKey.value) {
    return t('schedules.new.needProject')
  }
  if (!agentKey.value) {
    return t('schedules.new.needAgent')
  }
  if (!runnerName.value) {
    return t('schedules.new.needRunner')
  }
  if (isCliAgent.value && prompt.value.trim() === '') {
    return t('schedules.new.needPrompt')
  }
  if (isExec.value && command.value.trim() === '') {
    return t('schedules.new.needCommand')
  }
  if (isWorkerRunner.value) {
    if (workerMode.value === 'id' && workerId.value === '') {
      return t('schedules.new.needWorkerId')
    }
    if (workerMode.value === 'labels' && workerLabels.value.trim() === '') {
      return t('schedules.new.needWorkerLabels')
    }
  }
  // 参数声明（与后端声明校验同一口径，先挡在前面给出人话提示）
  const seen = new Set<string>()
  for (const p of declaredParams()) {
    if (!/^[A-Z_][A-Z0-9_]*$/.test(p.name)) {
      return t('schedules.new.paramNameInvalid', { name: p.name })
    }
    if (p.name.startsWith('GOFER_')) {
      return t('schedules.new.paramReserved', { name: p.name })
    }
    if (seen.has(p.name)) {
      return t('schedules.new.paramDup', { name: p.name })
    }
    seen.add(p.name)
    if (p.type === 'int' && (p.default ?? '') !== '' && !/^-?\d+$/.test(p.default ?? '')) {
      return t('schedules.new.paramInt', { name: p.name })
    }
    if (p.type === 'bool' && !['', '0', '1'].includes(p.default ?? '')) {
      return t('schedules.new.paramBool', { name: p.name })
    }
    if (p.type === 'choice' && (!p.choices || p.choices.length === 0)) {
      return t('schedules.new.paramChoice', { name: p.name })
    }
  }
  return ''
})

const canSubmit = computed(() => !submitting.value && validationError.value === '')

const delaySeconds = computed(() => {
  const value = delayValue.value ?? 0
  if (delayUnit.value === 'h') {
    return value * 3600
  }
  if (delayUnit.value === 'm') {
    return value * 60
  }
  return value
})

const runAtSeconds = computed(() => {
  if (runAtLocal.value === '') {
    return 0
  }
  const ms = new Date(runAtLocal.value).getTime()
  if (!Number.isFinite(ms)) {
    return 0
  }
  return Math.floor(ms / 1000)
})

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

function fillCron(expr: string) {
  triggerType.value = 'cron'
  cron.value = expr
}

async function onSubmit() {
  submitError.value = ''
  if (validationError.value !== '') {
    submitError.value = validationError.value
    return
  }
  submitting.value = true
  try {
    const request: SubmitJobReq = {
      project_key: projectKey.value,
      agent: agentKey.value,
      runner: runnerName.value,
      cwd: cwd.value.trim() || '.',
    }
    if (isCliAgent.value) {
      request.prompt = prompt.value
    }
    if (isExec.value) {
      request.cmd = parseCmd(command.value)
    }
    if (title.value.trim() !== '') {
      request.title = title.value.trim()
    }
    const tagList = parseLabels(tags.value)
    if (tagList.length > 0) {
      request.tags = tagList
    }
    if (timeoutSec.value != null && timeoutSec.value > 0) {
      request.timeout_sec = timeoutSec.value
    }
    if (isWorkerRunner.value) {
      if (workerMode.value === 'id') {
        request.worker_id = workerId.value
      } else {
        request.worker_labels = parseLabels(workerLabels.value)
      }
    }
    const req: CreateScheduleReq = {
      name: name.value.trim(),
      type: triggerType.value,
      cron: triggerType.value === 'cron' ? cron.value.trim() : '',
      request,
      enabled: enabled.value,
      catch_up: triggerType.value === 'cron' ? catchUp.value : false,
    }
    if (triggerType.value === 'once') {
      if (onceMode.value === 'delay') {
        req.delay_sec = delaySeconds.value
      } else {
        req.run_at = runAtSeconds.value
      }
    }
    const declared = declaredParams()
    if (declared.length > 0) {
      req.params = declared
    }
    await createSchedule(req)
    void router.push('/schedules')
  } catch (e) {
    submitError.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}

onMounted(() => {
  void loadMeta()
})
</script>

<template>
  <div class="newjob page">
    <div class="newjob-head">
      <BackLink list="schedules" to="/schedules" :label="t('schedules.title')" />
      <h1 class="title mono">{{ t('schedules.new.title') }}</h1>
    </div>

    <p v-if="loadError" class="error mono">{{ t('schedules.new.loadFailed', { err: loadError }) }}</p>
    <p v-else-if="loading" class="hint mono">{{ t('schedules.new.loading') }}</p>

    <form v-else class="card" @submit.prevent="onSubmit">
      <!-- name / trigger -->
      <div class="row">
        <div class="field">
          <label class="label mono" for="ns-name">{{ t('schedules.new.name') }}</label>
          <input
            id="ns-name"
            v-model="name"
            class="control mono"
            autocomplete="off"
            placeholder="nightly-report"
          />
        </div>
        <div class="field">
          <label class="label mono">{{ t('schedules.new.triggerType') }}</label>
          <div class="seg mono">
            <button
              type="button"
              class="seg-btn"
              :class="{ 'seg-btn--on': triggerType === 'cron' }"
              @click="triggerType = 'cron'"
            >
              {{ t('schedules.new.triggerCron') }}
            </button>
            <button
              type="button"
              class="seg-btn"
              :class="{ 'seg-btn--on': triggerType === 'once' }"
              @click="triggerType = 'once'; cron = ''"
            >
              {{ t('schedules.new.triggerOnce') }}
            </button>
          </div>
        </div>
      </div>

      <div v-if="triggerType === 'cron'" class="field">
          <label class="label mono" for="ns-cron">{{ t('schedules.new.cron') }}</label>
          <input
            id="ns-cron"
            v-model="cron"
            class="control mono"
            spellcheck="false"
            autocomplete="off"
            placeholder="0 9 * * *"
          />
          <div class="presets mono">
            <button
              v-for="p in cronPresets"
              :key="p.expr"
              type="button"
              class="preset"
              :title="p.expr"
              @click="fillCron(p.expr)"
            >
              {{ p.label }}
            </button>
          </div>
          <p class="field-hint mono">{{ t('schedules.new.cronHelp') }}</p>
      </div>

      <div v-else class="advanced">
        <div class="seg mono">
          <button
            type="button"
            class="seg-btn"
            :class="{ 'seg-btn--on': onceMode === 'delay' }"
            @click="onceMode = 'delay'"
          >
            {{ t('schedules.new.delay') }}
          </button>
          <button
            type="button"
            class="seg-btn"
            :class="{ 'seg-btn--on': onceMode === 'at' }"
            @click="onceMode = 'at'"
          >
            {{ t('schedules.new.at') }}
          </button>
        </div>

        <div v-if="onceMode === 'delay'" class="row once-row">
          <div class="field">
            <label class="label mono" for="ns-delay">{{ t('schedules.new.delayValue') }}</label>
            <input
              id="ns-delay"
              v-model.number="delayValue"
              class="control mono"
              type="number"
              min="1"
              step="1"
            />
          </div>
          <div class="field">
            <label class="label mono" for="ns-delay-unit">{{ t('schedules.new.delayUnit') }}</label>
            <select id="ns-delay-unit" v-model="delayUnit" class="control mono">
              <option value="s">{{ t('schedules.new.unitSecond') }}</option>
              <option value="m">{{ t('schedules.new.unitMinute') }}</option>
              <option value="h">{{ t('schedules.new.unitHour') }}</option>
            </select>
          </div>
        </div>

        <div v-else class="field once-row">
          <label class="label mono" for="ns-run-at">{{ t('schedules.new.runAt') }}</label>
          <input
            id="ns-run-at"
            v-model="runAtLocal"
            class="control mono"
            type="datetime-local"
          />
        </div>
      </div>

      <!-- project -->
      <div class="field">
        <label class="label mono" for="ns-project">{{ t('schedules.new.project') }}</label>
        <SearchSelect
          trigger-id="ns-project"
          test-id="ns-project"
          variant="field"
          class="mono"
          v-model="projectKey"
          :options="projectItems"
          :label="t('schedules.new.project')"
          :disabled="projectOptions.length === 0"
          :empty-text="t('schedules.new.noProject')"
          @select="onProjectChange()"
        />
        <p v-if="projectOptions.length === 0" class="field-hint mono">{{ t('schedules.new.noProject') }}</p>
      </div>

      <!-- agent / runner 两列 -->
      <div class="row">
        <div class="field">
          <label class="label mono" for="ns-agent">{{ t('schedules.new.agent') }}</label>
          <select id="ns-agent" v-model="agentKey" class="control mono">
            <option v-for="a in agentOptions" :key="a.key" :value="a.key">
              {{ a.key }} · {{ a.type }}
            </option>
          </select>
          <p v-if="agentOptions.length === 0" class="field-hint mono">
            {{ t('schedules.new.noAgent') }}
          </p>
        </div>
        <div class="field">
          <label class="label mono" for="ns-runner">{{ t('schedules.new.runner') }}</label>
          <select
            id="ns-runner"
            v-model="runnerName"
            class="control mono"
            @change="reconvergeToWorker"
          >
            <option v-for="r in runnerOptions" :key="r.name" :value="r.name">
              {{ runnerOptionText(r) }}
            </option>
          </select>
        </div>
      </div>

      <p v-if="workerNarrowingPending" class="field-hint mono">
        {{ t('schedules.new.workerPending') }}
      </p>

      <!-- cli-agent: prompt 文本域 -->
      <div v-if="isCliAgent" class="field">
        <label class="label mono" for="ns-prompt">{{ t('schedules.new.prompt') }}</label>
        <textarea
          id="ns-prompt"
          v-model="prompt"
          class="control mono area"
          rows="8"
          spellcheck="false"
          :placeholder="t('schedules.new.promptPlaceholder')"
        ></textarea>
      </div>

      <!-- exec: command 输入 -->
      <div v-else-if="isExec" class="field">
        <label class="label mono" for="ns-cmd">{{ t('schedules.new.command') }}</label>
        <input
          id="ns-cmd"
          v-model="command"
          class="control mono"
          spellcheck="false"
          autocomplete="off"
          placeholder="echo hello"
        />
      </div>

      <!-- runner=worker 高级项：二选一 -->
      <details v-if="isWorkerRunner" class="advanced" :open="advancedOpen">
        <summary class="mono" @click.prevent="advancedOpen = !advancedOpen">
          {{ t('schedules.new.workerSection') }}
        </summary>
        <div class="advanced-body">
          <div class="seg mono">
            <button
              type="button"
              class="seg-btn"
              :class="{ 'seg-btn--on': workerMode === 'id' }"
              @click="workerMode = 'id'"
            >
              {{ t('schedules.new.workerById') }}
            </button>
            <button
              type="button"
              class="seg-btn"
              :class="{ 'seg-btn--on': workerMode === 'labels' }"
              @click="workerMode = 'labels'"
            >
              {{ t('schedules.new.workerByLabels') }}
            </button>
          </div>

          <div v-if="workerMode === 'id'" class="field">
            <label class="label mono" for="ns-wid">{{ t('schedules.new.workerId') }}</label>
            <select id="ns-wid" v-model="workerId" class="control mono" @change="reconvergeToWorker">
              <option value="" disabled>{{ t('schedules.new.workerPick') }}</option>
              <option v-for="w in connectedWorkers" :key="w.id" :value="w.id">
                {{ w.id }}<template v-if="w.labels && w.labels.length"> · {{ w.labels.join(',') }}</template>
              </option>
            </select>
            <p v-if="connectedWorkers.length === 0" class="field-hint mono">
              {{ t('schedules.new.noWorker') }}
            </p>
          </div>

          <div v-else class="field">
            <label class="label mono" for="ns-labels">{{ t('schedules.new.workerLabels') }}</label>
            <input
              id="ns-labels"
              v-model="workerLabels"
              class="control mono"
              spellcheck="false"
              autocomplete="off"
              placeholder="gpu, linux"
            />
            <p class="field-hint mono">{{ t('schedules.new.workerLabelsHint') }}</p>
          </div>
        </div>
      </details>

      <!-- cwd / title -->
      <div class="row">
        <div class="field">
          <label class="label mono" for="ns-cwd">{{ t('schedules.new.cwd') }}</label>
          <input
            id="ns-cwd"
            v-model="cwd"
            class="control mono"
            spellcheck="false"
            autocomplete="off"
            placeholder="."
          />
        </div>
        <div class="field">
          <label class="label mono" for="ns-title">{{ t('schedules.new.titleField') }}</label>
          <input
            id="ns-title"
            v-model="title"
            class="control mono"
            autocomplete="off"
            :placeholder="t('schedules.new.titlePlaceholder')"
          />
        </div>
      </div>

      <!-- tags -->
      <div class="field">
        <label class="label mono" for="ns-tags">{{ t('schedules.new.tags') }}{{ t('schedules.new.tagsHint') }}</label>
        <input
          id="ns-tags"
          v-model="tags"
          class="control mono"
          spellcheck="false"
          autocomplete="off"
          placeholder="cron, nightly"
        />
      </div>

      <!-- timeout / catch_up / enabled -->
      <div class="row">
        <div class="field">
          <label class="label mono" for="ns-timeout">{{ t('schedules.new.timeout') }}</label>
          <input
            id="ns-timeout"
            v-model.number="timeoutSec"
            class="control mono"
            type="number"
            min="1"
            :placeholder="t('schedules.new.timeoutPlaceholder')"
          />
        </div>
        <div class="field field--check">
          <label v-if="triggerType === 'cron'" class="check mono">
            <input v-model="catchUp" type="checkbox" />
            <span>{{ t('schedules.new.catchUp') }}</span>
          </label>
          <label class="check mono">
            <input v-model="enabled" type="checkbox" />
            <span>{{ t('schedules.new.enabled') }}</span>
          </label>
        </div>
      </div>

      <!-- 参数声明（设计 2026-10-22 §D1）：值以环境变量传给 job -->
      <div class="field">
        <span class="label mono">{{ t('schedules.new.params') }}</span>
        <div v-for="(p, i) in params" :key="i" class="param-row" data-test="new-param-row">
          <input
            class="control mono param-name"
            type="text"
            :placeholder="t('schedules.new.paramName')"
            :value="p.name"
            @input="setParamField(i, 'name', ($event.target as HTMLInputElement).value)"
          />
          <select
            class="control mono param-type"
            :value="p.type || 'string'"
            @change="setParamField(i, 'type', ($event.target as HTMLSelectElement).value)"
          >
            <option value="string">string</option>
            <option value="int">int</option>
            <option value="bool">bool</option>
            <option value="choice">choice</option>
          </select>
          <input
            class="control mono param-def"
            type="text"
            :placeholder="t('schedules.new.paramDefault')"
            :value="p.default"
            @input="setParamField(i, 'default', ($event.target as HTMLInputElement).value)"
          />
          <input
            v-if="p.type === 'choice'"
            class="control mono param-def"
            type="text"
            :placeholder="t('schedules.new.paramChoices')"
            :value="(p.choices || []).join(', ')"
            @input="setParamChoices(i, ($event.target as HTMLInputElement).value)"
          />
          <input
            class="control mono param-desc"
            type="text"
            :placeholder="t('schedules.new.paramDesc')"
            :value="p.desc"
            @input="setParamField(i, 'desc', ($event.target as HTMLInputElement).value)"
          />
          <button class="param-del mono" type="button" :title="t('schedules.new.removeParam')" @click="removeParamRow(i)">
            −
          </button>
        </div>
        <button class="param-add mono" type="button" data-test="new-param-add" @click="addParamRow()">
          {{ t('schedules.new.addParam') }}
        </button>
      </div>

      <p v-if="validationError" class="field-hint field-hint--warn mono">
        {{ validationError }}
      </p>
      <p v-if="submitError" class="error mono">{{ submitError }}</p>

      <button class="submit" type="submit" :disabled="!canSubmit">
        {{ submitting ? t('schedules.new.submitting') : t('schedules.new.submit') }}
      </button>
    </form>
  </div>
</template>

<style scoped>
.newjob-head {
  display: flex;
  align-items: baseline;
  gap: 16px;
  margin-bottom: 14px;
}
.back {
  font-size: 13px;
  color: var(--ink-3);
}
.back:hover {
  color: var(--accent);
}
.title {
  font-size: 16px;
  color: var(--ink);
  margin: 0;
}

.hint {
  color: var(--ink-3);
  font-size: 13px;
}

.card {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
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
  gap: 8px;
}

.label {
  font-size: 11px;
  letter-spacing: 0.08em;
  color: var(--ink-3);
  margin-bottom: 6px;
}

.control {
  width: 100%;
  background: var(--bg);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 9px 10px;
  font-size: 13px;
  outline: none;
}
.control:focus {
  border-color: var(--accent);
}
.area {
  resize: vertical;
  min-height: 120px;
}
select.control {
  cursor: pointer;
}

.presets {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}
.preset {
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 3px 8px;
  font-size: 11px;
  cursor: pointer;
}
.preset:hover {
  border-color: var(--accent);
  color: var(--accent);
}

.field-hint {
  color: var(--ink-3);
  font-size: 11px;
  margin: 6px 0 0;
}

/* 参数声明行（设计 2026-10-22 §D1）：名字 / 类型 / 默认值 / 可选值 / 说明 */
.param-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
}
.param-name {
  flex: 0 0 150px;
}
.param-type {
  flex: 0 0 90px;
}
.param-def {
  flex: 1 1 110px;
  min-width: 0;
}
.param-desc {
  flex: 1 1 150px;
  min-width: 0;
}
.param-del,
.param-add {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 8px;
  font-size: 11px;
  cursor: pointer;
}
.param-add {
  margin-top: 8px;
}
.param-del:hover,
.param-add:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.field-hint--warn {
  color: var(--run);
}

.advanced {
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 10px 12px;
}
.advanced > summary {
  cursor: pointer;
  color: var(--accent);
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
  border-radius: var(--r-chip);
  overflow: hidden;
  width: fit-content;
}
.seg-btn {
  background: transparent;
  color: var(--ink-3);
  border: none;
  padding: 6px 14px;
  font-size: 12px;
}
.seg-btn:hover {
  color: var(--ink);
}
.seg-btn--on {
  background: var(--accent);
  color: var(--bg);
}

.check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--ink);
  cursor: pointer;
}
.check input {
  accent-color: var(--accent);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
}

.submit {
  margin-top: 4px;
  background: var(--accent);
  color: var(--bg);
  border: none;
  border-radius: var(--r-chip);
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
}
</style>
