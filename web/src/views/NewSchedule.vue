<script setup lang="ts">
// 新建调度（AUTO-02）：在「新建 job」表单基础上加 name + 触发配置 + enabled，
// 去掉 sync（调度触发的 job 由 serve 循环异步提交，channel=cron）。
//  - project 下拉 -> 联动限定可选 agent / runner（按 project.allowed_*）
//  - agent=exec 显 command 输入；否则显 prompt 文本域
//  - runner=worker -> 指定 worker_id 或 worker_labels
//  - cron 用标准 5 段表达式，也支持 @hourly/@daily/@every 描述符（robfig/cron 标准解析）
// 提交成功跳 /schedules。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { createSchedule, getMeta } from '../api/client'
import type {
  CreateScheduleReq,
  MetaAgent,
  MetaProject,
  MetaRunner,
  MetaWorker,
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

// 常用 cron 预设（快速填充）
const cronPresets: Array<{ expr: string; label: string }> = [
  { expr: '*/5 * * * *', label: '每 5 分钟' },
  { expr: '0 * * * *', label: '每小时' },
  { expr: '0 9 * * *', label: '每天 09:00' },
  { expr: '0 9 * * 1', label: '每周一 09:00' },
  { expr: '@daily', label: '每天零点' },
]

async function loadMeta() {
  loading.value = true
  loadError.value = ''
  try {
    const m = await getMeta()
    projects.value = m.projects ?? []
    agents.value = m.agents ?? []
    runners.value = m.runners ?? []
    workers.value = m.workers ?? []
    if (projects.value.length > 0) {
      selectProject(projects.value[0].key)
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
// 注：调度表单无 interactive 开关（区别于 NewJob），故此处不做 interactive 过滤。
const agentOptions = computed<MetaAgent[]>(() => {
  let list = agents.value
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

// project 下拉：worker runner 选定 worker 后只列该 worker 上报的 projects；
// 无上报/交集为空则回落全量（T5.3b fail-safe）。
const projectOptions = computed<MetaProject[]>(() => {
  const w = selectedWorker.value
  const wp = w?.projects ?? []
  if (wp.length > 0) {
    const set = new Set(wp)
    const narrowed = projects.value.filter((p) => set.has(p.key))
    if (narrowed.length > 0) {
      return narrowed
    }
  }
  return projects.value
})

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
// 不留悬空非法选择。project 失效则整体重收敛（selectProject 内再收 agent/runner）；否则只补收 agent。
function reconvergeToWorker() {
  const projs = projectOptions.value
  if (projs.length > 0 && !projs.some((p) => p.key === projectKey.value)) {
    selectProject(projs[0].key)
    return
  }
  const ags = agentOptions.value
  if (ags.length > 0 && !ags.some((a) => a.key === agentKey.value)) {
    const def = selectedProject.value?.default_agent
    agentKey.value = def && ags.some((a) => a.key === def) ? def : ags[0].key
  }
}

const validationError = computed<string>(() => {
  if (name.value.trim() === '') {
    return '请填写调度名称 name'
  }
  if (triggerType.value === 'cron' && cron.value.trim() === '') {
    return '请填写 cron 表达式'
  }
  if (triggerType.value === 'once') {
    if (cron.value.trim() !== '') {
      return '一次性调度不能填写 cron'
    }
    const now = Math.floor(Date.now() / 1000)
    if (onceMode.value === 'delay') {
      const sec = delaySeconds.value
      if (sec <= 0) {
        return '请填写大于 0 的延迟时间'
      }
      if (now + sec < now + 3) {
        return '一次性调度至少需要在 3 秒后触发'
      }
    } else {
      const sec = runAtSeconds.value
      if (sec <= 0) {
        return '请选择触发时间'
      }
      if (sec < now + 3) {
        return '触发时间至少需要在 3 秒后'
      }
    }
  }
  if (!projectKey.value) {
    return '请选择 project'
  }
  if (!agentKey.value) {
    return '请选择 agent'
  }
  if (!runnerName.value) {
    return '请选择 runner'
  }
  if (isCliAgent.value && prompt.value.trim() === '') {
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
  <div class="newjob">
    <div class="newjob-head">
      <RouterLink to="/schedules" class="back mono">← schedules</RouterLink>
      <h1 class="title mono">新建调度</h1>
    </div>

    <p v-if="loadError" class="error mono">表单选项加载失败：{{ loadError }}</p>
    <p v-else-if="loading" class="hint mono">加载选项中…</p>

    <form v-else class="card" @submit.prevent="onSubmit">
      <!-- name / trigger -->
      <div class="row">
        <div class="field">
          <label class="label mono" for="ns-name">NAME（调度名称）</label>
          <input
            id="ns-name"
            v-model="name"
            class="control mono"
            autocomplete="off"
            placeholder="nightly-report"
          />
        </div>
        <div class="field">
          <label class="label mono">触发类型</label>
          <div class="seg mono">
            <button
              type="button"
              class="seg-btn"
              :class="{ 'seg-btn--on': triggerType === 'cron' }"
              @click="triggerType = 'cron'"
            >
              cron 定时
            </button>
            <button
              type="button"
              class="seg-btn"
              :class="{ 'seg-btn--on': triggerType === 'once' }"
              @click="triggerType = 'once'; cron = ''"
            >
              一次性
            </button>
          </div>
        </div>
      </div>

      <div v-if="triggerType === 'cron'" class="field">
          <label class="label mono" for="ns-cron">CRON（5 段表达式）</label>
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
          <p class="field-hint mono">分 时 日 月 周；也支持 @hourly / @daily / @every 1h30m</p>
      </div>

      <div v-else class="advanced">
        <div class="seg mono">
          <button
            type="button"
            class="seg-btn"
            :class="{ 'seg-btn--on': onceMode === 'delay' }"
            @click="onceMode = 'delay'"
          >
            延迟
          </button>
          <button
            type="button"
            class="seg-btn"
            :class="{ 'seg-btn--on': onceMode === 'at' }"
            @click="onceMode = 'at'"
          >
            指定时间
          </button>
        </div>

        <div v-if="onceMode === 'delay'" class="row once-row">
          <div class="field">
            <label class="label mono" for="ns-delay">延迟时间</label>
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
            <label class="label mono" for="ns-delay-unit">单位</label>
            <select id="ns-delay-unit" v-model="delayUnit" class="control mono">
              <option value="s">秒</option>
              <option value="m">分钟</option>
              <option value="h">小时</option>
            </select>
          </div>
        </div>

        <div v-else class="field once-row">
          <label class="label mono" for="ns-run-at">触发时间</label>
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
        <label class="label mono" for="ns-project">PROJECT</label>
        <select
          id="ns-project"
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
          <label class="label mono" for="ns-agent">AGENT</label>
          <select id="ns-agent" v-model="agentKey" class="control mono">
            <option v-for="a in agentOptions" :key="a.key" :value="a.key">
              {{ a.key }} · {{ a.type }}
            </option>
          </select>
        </div>
        <div class="field">
          <label class="label mono" for="ns-runner">RUNNER</label>
          <select
            id="ns-runner"
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
        <label class="label mono" for="ns-prompt">PROMPT（可贴 markdown）</label>
        <textarea
          id="ns-prompt"
          v-model="prompt"
          class="control mono area"
          rows="8"
          spellcheck="false"
          placeholder="描述任务，正文即 prompt..."
        ></textarea>
      </div>

      <!-- exec: command 输入 -->
      <div v-else-if="isExec" class="field">
        <label class="label mono" for="ns-cmd">COMMAND（空格分词为 argv）</label>
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
            <label class="label mono" for="ns-wid">WORKER_ID（仅 connected）</label>
            <select id="ns-wid" v-model="workerId" class="control mono" @change="reconvergeToWorker">
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
            <label class="label mono" for="ns-labels">WORKER_LABELS（逗号分隔）</label>
            <input
              id="ns-labels"
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

      <!-- cwd / title -->
      <div class="row">
        <div class="field">
          <label class="label mono" for="ns-cwd">CWD</label>
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
          <label class="label mono" for="ns-title">TITLE（可选）</label>
          <input
            id="ns-title"
            v-model="title"
            class="control mono"
            autocomplete="off"
            placeholder="人类可读任务名"
          />
        </div>
      </div>

      <!-- tags -->
      <div class="field">
        <label class="label mono" for="ns-tags">TAGS（逗号分隔，可选）</label>
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
          <label class="label mono" for="ns-timeout">TIMEOUT_SEC（可选）</label>
          <input
            id="ns-timeout"
            v-model.number="timeoutSec"
            class="control mono"
            type="number"
            min="1"
            placeholder="默认无超时"
          />
        </div>
        <div class="field field--check">
          <label v-if="triggerType === 'cron'" class="check mono">
            <input v-model="catchUp" type="checkbox" />
            <span>catch_up（错过一次触发后在 grace 窗口内补跑一次）</span>
          </label>
          <label class="check mono">
            <input v-model="enabled" type="checkbox" />
            <span>enabled（创建后即启用）</span>
          </label>
        </div>
      </div>

      <p v-if="validationError" class="field-hint field-hint--warn mono">
        {{ validationError }}
      </p>
      <p v-if="submitError" class="error mono">{{ submitError }}</p>

      <button class="submit" type="submit" :disabled="!canSubmit">
        {{ submitting ? '创建中…' : '创建定时调度' }}
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
  gap: 8px;
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
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 11px;
  cursor: pointer;
}
.preset:hover {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

.field-hint {
  color: var(--queue);
  font-size: 11px;
  margin: 6px 0 0;
}
.field-hint--warn {
  color: var(--run);
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

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
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
}
</style>
