<script setup lang="ts">
// 新建会话（设计稿 §7.1 / §8）：/sessions 的唯一入口，底部抽屉表单（桌面居中）。
// 字段与校验沿用原 Sessions 页的内联表单：类型、项目、agent、runner（灰显原因）、模型、
// 工作目录、锁定目录、标题、第一句话；另给「完整配置」跳到 /new?mode=session。
import { computed, ref, watch } from 'vue'
import { t } from '../i18n'
import { runnerLabel } from '../utils/runnerDisplay'
import {
  CEILING_LABEL,
  ceilingAllowsAgent,
  ceilingAllowsProject,
  ceilingAllowsRunner,
  ceilingDeniedText,
} from '../utils/ceiling'
import { useWhoami } from '../store/whoami'
import { getMetaCached } from '../api/metaCache'
import { submitJob } from '../api/client'
import type { MetaAgent, MetaProject, MetaResp, MetaRunner, SubmitJobReq } from '../api/types'
import { DIR_LOCK_HINT, parseLockPaths } from '../utils/lockPaths'
import { computeRunnerBlocks, effectiveRunnerBlocks, pickRunner } from '../utils/runnerChoice'
import Sheet from './Sheet.vue'

export type NewSessionType = 'pty' | 'acp'

const props = withDefaults(defineProps<{ type?: NewSessionType }>(), { type: 'pty' })
const open = defineModel<boolean>({ default: false })
const emit = defineEmits<{ (e: 'created', payload: { jobID: string; sessionID: string; type: NewSessionType }): void }>()

const { ceiling: ceil } = useWhoami()
const meta = ref<MetaResp>({ projects: [], agents: [], runners: [], workers: [] })
const type = ref<NewSessionType>(props.type)
const projectKey = ref('')
const agentKey = ref('')
const runnerName = ref('local')
const title = ref('')
const prompt = ref('')
const model = ref('')
// 运行目录与目录锁（gofer-jkd2）：会话落在项目根会把整个项目根锁住，别的子目录 job 只能排队。
const cwd = ref('.')
const lockPaths = ref('')
const creating = ref(false)
const error = ref('')

const selectedProject = computed<MetaProject | undefined>(() =>
  meta.value.projects.find((p) => p.key === projectKey.value),
)
// 调用方上限（ceiling）：范围外的 project 不列出（下拉没有灰显原因的位置，选到就必被拒）。
const projects = computed<MetaProject[]>(() =>
  meta.value.projects.filter((p) => ceilingAllowsProject(ceil.value, p.key)),
)
const projectCeilingHidden = computed(() => meta.value.projects.length - projects.value.length)
const agents = computed<MetaAgent[]>(() => {
  const allowed = new Set(selectedProject.value?.allowed_agents ?? [])
  return meta.value.agents
    .filter((a) => allowed.size === 0 || allowed.has(a.key))
    .filter((a) => (type.value === 'acp' ? a.type === 'acp-agent' : !!a.interactive))
    // 调用方上限：范围外的 agent 不出现。
    .filter((a) => ceilingAllowsAgent(ceil.value, a.key))
    .sort((a, b) => a.key.localeCompare(b.key))
})
const runners = computed<MetaRunner[]>(() => {
  const allowed = new Set(selectedProject.value?.allowed_runners ?? [])
  return meta.value.runners
    .filter((r) => r.type === 'local' || allowed.size === 0 || allowed.has(r.name))
    // ACP 持续会话：local 与协议 >= v13 的 worker 可用，其它 runner 不放出。
    .filter((r) => type.value !== 'acp' || r.type === 'local' || r.type === 'worker')
})
// 灰显原因：project 级阻塞（worker 不具备该 project 等）+ ACP 会话的 worker 协议门槛
// + 调用方上限（范围外的 runner 同样灰显 + 写原因）。
const runnerBlocks = computed(() => {
  const base = computeRunnerBlocks(selectedProject.value, runners.value, meta.value.workers)
  for (const runner of runners.value) {
    if (!ceilingAllowsRunner(ceil.value, runner.name)) {
      base[runner.name] = { short: CEILING_LABEL, full: ceilingDeniedText('runner', runner.name) }
    }
  }
  return effectiveRunnerBlocks(runners.value, base, meta.value.workers, type.value === 'acp')
})

const fullConfigURL = computed(() => {
  const q = new URLSearchParams({ mode: 'session' })
  if (projectKey.value) q.set('project', projectKey.value)
  if (agentKey.value) q.set('agent', agentKey.value)
  if (runnerName.value) q.set('runner', runnerName.value)
  if (title.value.trim()) q.set('title', title.value.trim())
  if (prompt.value.trim()) q.set('prompt', prompt.value.trim())
  if (model.value.trim()) q.set('model', model.value.trim())
  if (cwd.value.trim() && cwd.value.trim() !== '.') q.set('cwd', cwd.value.trim())
  if (lockPaths.value.trim()) q.set('lock', lockPaths.value.trim())
  return `/new?${q.toString()}`
})

function chooseDefaults(): void {
  if (!projectKey.value) projectKey.value = projects.value[0]?.key ?? ''
  if (!agentKey.value || !agents.value.some((a) => a.key === agentKey.value)) {
    agentKey.value = agents.value[0]?.key ?? ''
  }
  runnerName.value =
    pickRunner(runnerName.value, selectedProject.value, runners.value, runnerBlocks.value) ||
    runners.value[0]?.name ||
    'local'
}

async function loadMeta(): Promise<void> {
  try {
    meta.value = await getMetaCached()
    chooseDefaults()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function create(): Promise<void> {
  if (creating.value || !projectKey.value || !agentKey.value) return
  creating.value = true
  error.value = ''
  const req: SubmitJobReq = {
    project_key: projectKey.value,
    agent: agentKey.value,
    runner: runnerName.value,
    title: title.value.trim() || undefined,
    prompt: prompt.value.trim() || undefined,
    model: model.value.trim() || undefined,
    cwd: cwd.value.trim() || '.',
    session: type.value === 'acp',
    interactive: type.value === 'pty',
  }
  const lockList = parseLockPaths(lockPaths.value)
  if (lockList.length > 0) req.lock_paths = lockList
  try {
    const result = await submitJob(req)
    emit('created', { jobID: result.job.id, sessionID: result.job.session_id ?? '', type: type.value })
    open.value = false
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

watch(projectKey, chooseDefaults)
watch(type, () => {
  agentKey.value = ''
  if (type.value === 'acp') runnerName.value = 'local'
  chooseDefaults()
})
watch(open, (value) => {
  if (!value) return
  type.value = props.type
  error.value = ''
  void loadMeta()
})
if (open.value) void loadMeta()
</script>

<template>
  <Sheet v-model="open" :label="t('sessions.new.title')" sheet-id="new-session-sheet">
    <form class="ns-form" data-test="new-session-form" @submit.prevent="create">
      <label class="ns-field">
        <span>{{ t('sessions.new.type') }}</span>
        <select v-model="type" data-test="session-type">
          <option value="pty">{{ t('sessions.new.typePty') }}</option>
          <option value="acp">{{ t('sessions.new.typeAcp') }}</option>
        </select>
      </label>
      <label class="ns-field">
        <span>{{ t('field.project') }}</span>
        <select v-model="projectKey" data-test="session-project">
          <option v-for="project in projects" :key="project.key" :value="project.key">{{ project.key }}</option>
        </select>
        <span v-if="projectCeilingHidden > 0" class="ns-hint">
          {{ t('sessions.new.ceilingHidden', { n: projectCeilingHidden, label: CEILING_LABEL }) }}
        </span>
      </label>
      <label class="ns-field">
        <span>{{ t('field.agent') }}</span>
        <select v-model="agentKey" :disabled="!agents.length" data-test="session-agent">
          <option v-for="agent in agents" :key="agent.key" :value="agent.key">{{ agent.key }}</option>
        </select>
      </label>
      <label class="ns-field">
        <span>runner</span>
        <select v-model="runnerName" data-test="session-runner">
          <option
            v-for="runner in runners"
            :key="runner.name"
            :value="runner.name"
            :disabled="!!runnerBlocks[runner.name]"
            :title="runnerBlocks[runner.name]?.full"
          >{{ runnerLabel(runner.name) }}<template v-if="runnerBlocks[runner.name]"> · {{ runnerBlocks[runner.name].short }}</template></option>
        </select>
        <span v-if="runnerBlocks[runnerName]" class="ns-hint">{{ runnerBlocks[runnerName].full }}</span>
      </label>
      <label class="ns-field">
        <span>{{ t('sessions.new.model') }}</span>
        <input v-model="model" type="text" :placeholder="t('sessions.new.modelPlaceholder')" />
      </label>
      <label class="ns-field">
        <span>{{ t('sessions.new.cwd') }}</span>
        <input v-model="cwd" type="text" placeholder="." />
      </label>
      <label class="ns-field ns-field--wide">
        <span>{{ t('sessions.new.lock') }}</span>
        <input v-model="lockPaths" type="text" :title="DIR_LOCK_HINT" :placeholder="t('sessions.new.lockPlaceholder')" />
      </label>
      <label class="ns-field ns-field--wide">
        <span>{{ t('sessions.new.titleField') }}</span>
        <input v-model="title" type="text" :placeholder="t('sessions.new.titlePlaceholder')" />
      </label>
      <label class="ns-field ns-field--wide">
        <span>{{ t('sessions.new.prompt') }}</span>
        <textarea v-model="prompt" rows="2" :placeholder="t('sessions.new.promptPlaceholder')"></textarea>
      </label>
      <p v-if="error" class="ns-error" role="alert">{{ error }}</p>
      <div class="ns-actions">
        <RouterLink class="ns-link" :to="fullConfigURL">{{ t('sessions.new.fullConfig') }}</RouterLink>
        <span class="ns-grow"></span>
        <button class="ns-cancel" type="button" @click="open = false">{{ t('act.cancel') }}</button>
        <button class="ns-submit" type="submit" :disabled="creating || !agentKey" data-test="session-create">
          {{ creating ? t('sessions.new.creating') : t('sessions.new.create') }}
        </button>
      </div>
    </form>
  </Sheet>
</template>

<style scoped>
.ns-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--s2) var(--s3);
  min-width: 0;
}
.ns-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.ns-field--wide {
  grid-column: span 2;
}
.ns-field input,
.ns-field select,
.ns-field textarea {
  min-width: 0;
  padding: 6px 7px;
  background: var(--surface-2);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
}
.ns-field textarea {
  resize: vertical;
}
.ns-field input:focus-visible,
.ns-field select:focus-visible,
.ns-field textarea:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 0;
  border-color: var(--accent);
}
.ns-hint {
  color: var(--need);
  font-size: var(--fs-xs);
  line-height: 1.3;
}
.ns-error {
  grid-column: span 2;
  margin: 0;
  padding: 6px var(--s2);
  color: var(--fail);
  border: 1px solid var(--fail);
  border-radius: var(--r-ctl);
  font-size: var(--fs-sm);
  overflow-wrap: anywhere;
}
.ns-actions {
  grid-column: span 2;
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
}
.ns-grow {
  flex: 1;
}
.ns-link {
  color: var(--accent);
  font-size: var(--fs-sm);
  text-decoration: none;
}
.ns-link:hover {
  text-decoration: underline;
}
.ns-cancel,
.ns-submit {
  min-height: 36px;
  padding: 0 var(--s3);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.ns-cancel {
  background: transparent;
  color: var(--ink-2);
  border: 1px solid var(--line);
}
.ns-submit {
  background: var(--accent);
  color: var(--accent-fg, var(--surface));
  border: 1px solid var(--accent);
  font-weight: 600;
}
.ns-submit:disabled {
  opacity: 0.45;
  cursor: default;
}
@media (max-width: 760px) {
  .ns-form {
    grid-template-columns: 1fr;
  }
  .ns-field--wide,
  .ns-error,
  .ns-actions {
    grid-column: span 1;
  }
  .ns-field select,
  .ns-field input,
  .ns-field textarea,
  .ns-cancel,
  .ns-submit {
    min-height: 40px;
    font-size: 16px;
  }
}
</style>
