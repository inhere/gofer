<script setup lang="ts">
// 新建 job 表单（G4，design §6.4 / P3-b）：
//  - project 下拉 -> 联动限定可选 agent / runner（按 project.allowed_*）
//  - agent=cli-agent 显 prompt 文本域（可贴 md）；agent=exec 显 command 输入（空格分词为 cmd[]）
//  - runner=worker -> 二选一：指定 worker_id（connected）或 worker_labels（逗号），默认折叠高级项
//  - cwd（默认 .）/ title / timeout / sync 勾选
// 提交成功跳详情；202（仍在后台）提示后仍跳详情（详情页自有 SSE 续看）。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getMeta, submitJob } from '../api/client'
import type { MetaAgent, MetaProject, MetaRunner, MetaWorker } from '../api/types'

const router = useRouter()

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
const cwd = ref('.')
const title = ref('')
const tags = ref('')
const timeoutSec = ref<number | null>(null)
const sync = ref(false)

// runner=worker 高级项
const advancedOpen = ref(false)
const workerMode = ref<'id' | 'labels'>('id')
const workerId = ref('')
const workerLabels = ref('')

async function loadMeta() {
  loading.value = true
  loadError.value = ''
  try {
    const m = await getMeta()
    projects.value = m.projects ?? []
    agents.value = m.agents ?? []
    runners.value = m.runners ?? []
    workers.value = m.workers ?? []
    // 默认选中首个 project，触发联动
    if (projects.value.length > 0) {
      selectProject(projects.value[0].key)
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

// 联动：project 选定后，agent/runner 仅取该 project 的 allowed_*（与 meta 全集求交，
// 保证既在 allowlist 又确有配置）。allowlist 为空时回落到全集。
const agentOptions = computed<MetaAgent[]>(() => {
  const allowed = selectedProject.value?.allowed_agents ?? []
  if (allowed.length === 0) {
    return agents.value
  }
  const set = new Set(allowed)
  return agents.value.filter((a) => set.has(a.key))
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

async function onSubmit() {
  submitError.value = ''
  notice.value = ''
  if (validationError.value !== '') {
    submitError.value = validationError.value
    return
  }
  submitting.value = true
  try {
    const req = {
      project_key: projectKey.value,
      agent: agentKey.value,
      runner: runnerName.value,
      cwd: cwd.value.trim() || '.',
      sync: sync.value,
    } as Parameters<typeof submitJob>[0]
    if (isCliAgent.value) {
      req.prompt = prompt.value
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

onMounted(() => {
  void loadMeta()
})
</script>

<template>
  <div class="newjob">
    <div class="newjob-head">
      <RouterLink to="/board" class="back mono">← board</RouterLink>
      <h1 class="title mono">新建 job</h1>
    </div>

    <p v-if="loadError" class="error mono">表单选项加载失败：{{ loadError }}</p>
    <p v-else-if="loading" class="hint mono">加载选项中…</p>

    <form v-else class="card" @submit.prevent="onSubmit">
      <!-- project -->
      <div class="field">
        <label class="label mono" for="nj-project">PROJECT</label>
        <select
          id="nj-project"
          v-model="projectKey"
          class="control mono"
          @change="onProjectChange"
        >
          <option v-for="p in projects" :key="p.key" :value="p.key">{{ p.key }}</option>
        </select>
        <p v-if="projects.length === 0" class="field-hint mono">无可用 project</p>
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
          <select id="nj-runner" v-model="runnerName" class="control mono">
            <option v-for="r in runnerOptions" :key="r.name" :value="r.name">
              {{ r.name }} · {{ r.type }}
            </option>
          </select>
        </div>
      </div>

      <!-- cli-agent: prompt 文本域 -->
      <div v-if="isCliAgent" class="field">
        <label class="label mono" for="nj-prompt">PROMPT（可贴 markdown）</label>
        <textarea
          id="nj-prompt"
          v-model="prompt"
          class="control mono area"
          rows="8"
          spellcheck="false"
          placeholder="描述任务，正文即 prompt..."
        ></textarea>
      </div>

      <!-- exec: command 输入 -->
      <div v-else-if="isExec" class="field">
        <label class="label mono" for="nj-cmd">COMMAND（空格分词为 argv）</label>
        <input
          id="nj-cmd"
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
            <label class="label mono" for="nj-wid">WORKER_ID（仅 connected）</label>
            <select id="nj-wid" v-model="workerId" class="control mono">
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
            spellcheck="false"
            autocomplete="off"
            placeholder="."
          />
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

      <div class="row">
        <div class="field">
          <label class="label mono" for="nj-timeout">TIMEOUT_SEC（可选）</label>
          <input
            id="nj-timeout"
            v-model.number="timeoutSec"
            class="control mono"
            type="number"
            min="1"
            placeholder="默认无超时"
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
        {{ submitting ? '提交中…' : '提交 job' }}
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
}
</style>
