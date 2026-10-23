<script setup lang="ts">
import { runnerOptionText } from '../../utils/runnerDisplay'
import { useWhoami } from '../../store/whoami'
import { t } from '../../i18n'
import {
  ceilingLabel,
  ceilingReadOnlyNotice,
  ceilingAllowsAgent,
  ceilingAllowsExec,
  ceilingAllowsProject,
  ceilingAllowsRunner,
  ceilingDeniedText,
} from '../../utils/ceiling'
import { DIR_LOCK_HINT, parseLockPaths } from '../../utils/lockPaths'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { getPlan, listPlans, submitJob } from '../../api/client'
import { getMetaCached } from '../../api/metaCache'
import type { MetaAgent, MetaProject, MetaResp, Plan, SubmitJobReq, Todo } from '../../api/types'
import {
  computeRunnerBlocks,
  effectiveRunnerBlocks,
  pickRunner,
  projectRunnerOptions,
  sessionRunnerBlock,
} from '../../utils/runnerChoice'

const emit = defineEmits<{ (e: 'submitted', jobID: string): void }>()
defineProps<{ sessionOpen: boolean }>()

type ComposerMode = 'conversation' | 'continuous' | 'terminal' | 'batch'

const meta = ref<MetaResp>({ projects: [], agents: [], runners: [], workers: [] })
const projectKey = ref('')
const agentKey = ref('')
const mode = ref<ComposerMode>('batch')
const runnerName = ref('')
// 选了 ACP agent 而模式还是一次性批处理时自动切到持续会话，并在表单里说明。
const modeNote = ref('')
const planID = ref('')
const todoID = ref('')
const cwd = ref('.')
// 目录锁（gofer-jkd2）：与 CLI 的 `--lock` 对齐；持续会话整个会话期间持有锁，尽量锁子目录。
const lockPaths = ref('')
// N1 §B：可选模型，空 = agent 自身默认。
const model = ref('')
const prompt = ref('')
const plans = ref<Plan[]>([])
const todos = ref<Todo[]>([])
const loading = ref(false)
const submitting = ref(false)
const error = ref('')
const promptInput = ref<HTMLTextAreaElement | null>(null)
// 发起表单平时收起，由侧栏「＋ 新会话」、命令面板或手机右下角 ＋ 打开成弹窗。
const open = ref(false)

// 调用方上限（ceiling）：本人 token 的范围上界（utils/ceiling.ts）。候选按它收窄，只读的不给提交。
const { ceiling: ceil, readOnly: ceilingReadOnly } = useWhoami()

const selectedProject = computed<MetaProject | undefined>(() => meta.value.projects.find((p) => p.key === projectKey.value))
// 调用方上限：范围外的 project 不列出（下拉没有灰显原因的位置，选到就必被拒）。
const projectOptions = computed(() => meta.value.projects.filter((p) => ceilingAllowsProject(ceil.value, p.key)))
const projectCeilingHidden = computed(() => meta.value.projects.length - projectOptions.value.length)
const selectedAgent = computed<MetaAgent | undefined>(() => meta.value.agents.find((a) => a.key === agentKey.value))
const agentOptions = computed(() => {
  const allowed = new Set(selectedProject.value?.allowed_agents ?? [])
  return meta.value.agents
    .filter((agent) => allowed.size === 0 || allowed.has(agent.key))
    .filter((agent) => {
      if (mode.value === 'terminal') return !!agent.interactive && selectedProject.value?.allow_interactive !== false
      if (mode.value === 'conversation' || mode.value === 'continuous') return agent.type === 'acp-agent'
      return agent.batch === true || agent.batch == null || agent.type === 'exec'
    })
    // 调用方上限：范围外的 agent 不出现；exec 类还要 allow_exec（与后端严格默认一致）。
    .filter((agent) => ceilingAllowsAgent(ceil.value, agent.key))
    .filter((agent) => ceilingAllowsExec(ceil.value) || agent.type !== 'exec')
    .sort((a, b) => a.key.localeCompare(b.key))
})
// runner：按项目 allowed_runners 过滤；不可用项灰显并写原因（逻辑与 NewJob 共用 utils/runnerChoice）。
const runnerOptions = computed(() => projectRunnerOptions(selectedProject.value, meta.value.runners))
const baseRunnerBlocks = computed(() => computeRunnerBlocks(selectedProject.value, runnerOptions.value, meta.value.workers))
// 持续会话模式下，只有 local 与协议 >= v13 的 worker 可用；调用方上限范围外的 runner 也灰显。
const runnerBlocks = computed(() => {
  const base = effectiveRunnerBlocks(runnerOptions.value, baseRunnerBlocks.value, meta.value.workers, mode.value === 'continuous')
  for (const r of runnerOptions.value) {
    if (!ceilingAllowsRunner(ceil.value, r.name)) {
      base[r.name] = { short: ceilingLabel(), full: ceilingDeniedText('runner', r.name) }
    }
  }
  return base
})
const selectedRunnerBlock = computed(() => runnerBlocks.value[runnerName.value]?.full ?? '')
// 持续会话可用 = 存在可用的 local 或 worker runner（不再硬写 local），且在本人的上限范围内。
const sessionRunnerAvailable = computed(() =>
  runnerOptions.value.some(
    (r) =>
      ceilingAllowsRunner(ceil.value, r.name) &&
      (r.type === 'local' || r.type === 'worker') &&
      !baseRunnerBlocks.value[r.name] &&
      !sessionRunnerBlock(r, meta.value.workers),
  ),
)
const modeOptions = computed<Array<{ value: ComposerMode; label: string }>>(() => {
  const all: Array<{ value: ComposerMode; label: string }> = [{ value: 'batch', label: t('workbench.new.mode.batch') }]
  const project = selectedProject.value
  const projectAgents = meta.value.agents.filter((agent) => (project?.allowed_agents.length ?? 0) === 0 || project?.allowed_agents.includes(agent.key))
  if (projectAgents.some((agent) => agent.type === 'acp-agent')) all.unshift({ value: 'conversation', label: t('workbench.new.mode.conversation') })
  if (projectAgents.some((agent) => agent.type === 'acp-agent') && sessionRunnerAvailable.value) {
    all.unshift({ value: 'continuous', label: t('workbench.new.mode.continuous') })
  }
  if (project?.allow_interactive !== false && projectAgents.some((agent) => agent.interactive)) all.push({ value: 'terminal', label: t('workbench.new.mode.terminal') })
  return all
})
const promptLabel = computed(() => selectedAgent.value?.type === 'exec'
  ? t('workbench.new.commandLabel')
  : t('workbench.new.promptLabel'))
const canSubmit = computed(() => !ceilingReadOnly.value && !!projectKey.value && !!agentKey.value && !!prompt.value.trim() && !submitting.value && !selectedRunnerBlock.value)

function agentCapability(agent: MetaAgent): string {
  const caps: string[] = []
  if (agent.interactive) caps.push('PTY')
  if (agent.type === 'acp-agent') caps.push('ACP')
  if (agent.batch || (!agent.interactive && agent.type !== 'acp-agent')) caps.push(t('workbench.new.capability.batch'))
  return caps.join('/') || agent.type
}

function chooseDefaultAgent(): void {
  if (!agentOptions.value.some((agent) => agent.key === agentKey.value)) {
    agentKey.value = agentOptions.value[0]?.key ?? ''
  }
}

async function refreshPlans(): Promise<void> {
  planID.value = ''
  todoID.value = ''
  todos.value = []
  if (!projectKey.value) {
    plans.value = []
    return
  }
  try {
    plans.value = (await listPlans({ project: projectKey.value, limit: 100 })).plans ?? []
  } catch {
    plans.value = []
  }
}

async function loadTodos(): Promise<void> {
  todoID.value = ''
  todos.value = []
  if (!planID.value) return
  try {
    todos.value = (await getPlan(planID.value)).todos ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

// 默认 runner 沿用原规则（项目 allowed_runners[0]，否则内置 local），但必须可见可改。
function chooseRunner(): void {
  runnerName.value = pickRunner(runnerName.value, selectedProject.value, runnerOptions.value, runnerBlocks.value)
}

function commandArgs(raw: string): string[] {
  return raw.trim().split(/\s+/).filter(Boolean)
}

async function submit(): Promise<void> {
  if (!canSubmit.value || !selectedProject.value || !selectedAgent.value) return
  submitting.value = true
  error.value = ''
  try {
    const req: SubmitJobReq = {
      project_key: projectKey.value,
      agent: agentKey.value,
      runner: runnerName.value || 'local',
      cwd: cwd.value.trim() || '.',
      channel: 'web',
    }
    if (selectedAgent.value.type === 'exec') req.cmd = commandArgs(prompt.value)
    else if (mode.value === 'terminal') req.system_prompt = prompt.value.trim()
    else req.prompt = prompt.value.trim()
    if (selectedAgent.value.type !== 'exec' && model.value.trim()) req.model = model.value.trim()
    const lockList = parseLockPaths(lockPaths.value)
    if (lockList.length > 0) req.lock_paths = lockList
    if (mode.value === 'terminal') req.interactive = true
    if (mode.value === 'continuous') req.session = true
    if (planID.value) req.plan_id = planID.value
    if (todoID.value) req.todo_id = todoID.value
    const result = await submitJob(req)
    prompt.value = ''
    open.value = false
    emit('submitted', result.job.id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
    event.preventDefault()
    void submit()
  }
}

function focusPrompt(): void {
  open.value = true
  void nextTick(() => promptInput.value?.focus())
}

function close(): void {
  open.value = false
}

function onPanelKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    close()
  }
}

defineExpose({ focusPrompt, close })

watch(projectKey, () => {
  chooseRunner()
  chooseDefaultAgent()
  void refreshPlans()
})
watch(mode, () => {
  chooseRunner()
  chooseDefaultAgent()
})
watch(agentKey, (key) => {
  const agent = meta.value.agents.find((a) => a.key === key)
  if (agent?.type === 'acp-agent' && mode.value === 'batch' && modeOptions.value.some((m) => m.value === 'continuous')) {
    mode.value = 'continuous'
    modeNote.value = t('workbench.new.acpNote', { key })
  }
})
watch(planID, () => void loadTodos())

onMounted(async () => {
  loading.value = true
  try {
    meta.value = await getMetaCached()
    projectKey.value = projectOptions.value[0]?.key ?? ''
    chooseRunner()
    chooseDefaultAgent()
    await refreshPlans()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <section class="composer" :aria-label="t('workbench.list.new')">
    <button v-if="!open && !sessionOpen" class="mobile-launch mono" type="button" :aria-label="t('workbench.new.launch')" @click="focusPrompt">＋</button>
    <div v-if="open" class="composer-backdrop" @click.self="close">
    <div class="composer-panel" role="dialog" aria-modal="true" :aria-label="t('workbench.list.new')" @keydown="onPanelKeydown">
      <div class="panel-head mono">
        <span>{{ t('workbench.list.new') }}</span>
        <button class="panel-close mono" type="button" :aria-label="t('act.close')" @click="close">×</button>
      </div>
      <div class="composer-selects">
      <select v-model="projectKey" class="field mono" :aria-label="t('field.project')" :disabled="loading">
        <option value="" disabled>{{ t('field.project') }}</option>
        <option v-for="project in projectOptions" :key="project.key" :value="project.key">{{ project.key }}</option>
      </select>
      <select v-model="agentKey" class="field mono" :aria-label="t('field.agent')" :disabled="loading">
        <option value="" disabled>agent</option>
        <option v-for="agent in agentOptions" :key="agent.key" :value="agent.key">{{ agent.key }} · {{ agentCapability(agent) }}</option>
      </select>
      <select v-model="runnerName" class="field mono" :aria-label="t('workbench.field.runner')" :disabled="loading">
        <option v-for="r in runnerOptions" :key="r.name" :value="r.name" :disabled="!!runnerBlocks[r.name]">
          {{ runnerOptionText(r) }}<template v-if="runnerBlocks[r.name]"> · {{ runnerBlocks[r.name].short }}</template>
        </option>
      </select>
      <select v-model="mode" class="field mono" :aria-label="t('workbench.new.mode')" @change="modeNote = ''">
        <option v-for="item in modeOptions" :key="item.value" :value="item.value">{{ item.label }}</option>
      </select>
      <select v-model="planID" class="field mono" :aria-label="t('workbench.new.plan')">
        <option value="">{{ t('workbench.new.noPlan') }}</option>
        <option v-for="plan in plans" :key="plan.plan_id" :value="plan.plan_id">{{ plan.title || plan.plan_id }}</option>
      </select>
      <select v-model="todoID" class="field mono" :aria-label="t('workbench.new.todo')" :disabled="!planID">
        <option value="">{{ t('workbench.new.noTodo') }}</option>
        <option v-for="todo in todos" :key="todo.todo_id" :value="todo.todo_id">{{ todo.title }}</option>
      </select>
      <input v-model="cwd" class="field cwd mono" :aria-label="t('workbench.new.cwd')" placeholder="cwd" />
      <input v-model="lockPaths" class="field cwd mono" :aria-label="t('workbench.new.lock')" :title="DIR_LOCK_HINT" :placeholder="t('workbench.new.lockPlaceholder')" />
      <input v-if="selectedAgent && selectedAgent.type !== 'exec'" v-model="model" class="field cwd mono" :aria-label="t('workbench.field.model')" :placeholder="t('workbench.new.modelPlaceholder')" />
      </div>
      <div class="prompt-row">
      <textarea
        ref="promptInput"
        v-model="prompt"
        class="prompt mono"
        rows="2"
        :placeholder="t('workbench.new.promptPlaceholder', { label: promptLabel })"
        @keydown="onKeydown"
      ></textarea>
      <button class="submit mono" type="button" :disabled="!canSubmit" @click="submit">
        {{ submitting ? t('workbench.new.submitting') : t('workbench.new.start') }}
      </button>
      </div>
      <p v-if="projectCeilingHidden > 0" class="hint mono">{{ t('workbench.new.ceilingHidden', { n: projectCeilingHidden, label: ceilingLabel() }) }}</p>
      <p v-if="ceilingReadOnly" class="hint mono" data-test="composer-readonly">{{ ceilingReadOnlyNotice() }}</p>
      <p v-if="modeNote" class="hint mono">{{ modeNote }}</p>
      <p v-if="mode === 'continuous' || mode === 'conversation'" class="hint mono">
        {{ t('workbench.new.sessionHint') }}
      </p>
      <p v-if="selectedRunnerBlock" class="error mono">{{ selectedRunnerBlock }}</p>
      <p v-if="error" class="error mono">{{ error }}</p>
    </div>
    </div>
  </section>
</template>

<style scoped>
.composer { min-width: 0; }
.composer-backdrop { position: fixed; inset: 0; z-index: 90; display: flex; align-items: flex-start; justify-content: center; padding-top: 12vh; background: rgba(0,0,0,.45); }
.composer-panel { width: min(880px, 94vw); max-height: 80vh; overflow-y: auto; display: flex; flex-direction: column; gap: 8px; padding: 12px 14px 14px; background: var(--surface); border: 1px solid var(--line); border-radius: 10px; box-shadow: 0 14px 40px rgba(0,0,0,.55); }
.panel-head { display: flex; align-items: center; justify-content: space-between; color: var(--ink); font-size: 13px; font-weight: 600; }
.panel-close { color: var(--ink); background: transparent; border: 0; font-size: 20px; line-height: 1; cursor: pointer; }
.mobile-launch { display: none; }
.composer-selects { display: grid; grid-template-columns: repeat(3, minmax(0,1fr)); gap: 6px; }
.field, .prompt { min-width: 0; color: var(--ink); background: var(--bg); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 6px 7px; font-size: 12px; }
.field:focus, .prompt:focus { outline: 1px solid var(--accent); border-color: var(--accent); }
.prompt-row { display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 7px; }
.prompt { resize: vertical; min-height: 110px; max-height: 50vh; }
.submit { align-self: stretch; color: var(--bg); background: var(--accent); border: 1px solid var(--accent); border-radius: var(--r-chip); padding: 0 16px; font-weight: 700; }
.submit:disabled { opacity: .45; cursor: default; }
.error { color: var(--fail); margin: 0; font-size: 11px; }
.hint { color: var(--ink-3); margin: 0; font-size: 11px; }
@media (max-width: 760px) { .composer-selects { grid-template-columns: repeat(2, minmax(0,1fr)); } .prompt-row { grid-template-columns: 1fr; } .submit { min-height: 34px; } }
@media (max-width: 760px) {
  /* 手机：会话打开时由工作台顶栏提供 ＋，表单从底部弹出 */
  .mobile-launch { display: none; }
  .composer-backdrop { align-items: flex-end; padding: 8px; }
  .composer-panel { width: 100%; max-height: min(82vh, 680px); }
}
</style>
