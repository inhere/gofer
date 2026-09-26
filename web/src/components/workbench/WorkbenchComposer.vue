<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { getMeta, getPlan, listPlans, submitJob } from '../../api/client'
import type { MetaAgent, MetaProject, MetaResp, Plan, SubmitJobReq, Todo } from '../../api/types'

const emit = defineEmits<{ (e: 'submitted', jobID: string): void }>()

type ComposerMode = 'conversation' | 'terminal' | 'batch'

const meta = ref<MetaResp>({ projects: [], agents: [], runners: [], workers: [] })
const projectKey = ref('')
const agentKey = ref('')
const mode = ref<ComposerMode>('batch')
const planID = ref('')
const todoID = ref('')
const cwd = ref('.')
const prompt = ref('')
const plans = ref<Plan[]>([])
const todos = ref<Todo[]>([])
const loading = ref(false)
const submitting = ref(false)
const error = ref('')
const promptInput = ref<HTMLTextAreaElement | null>(null)
const mobileOpen = ref(false)

const selectedProject = computed<MetaProject | undefined>(() => meta.value.projects.find((p) => p.key === projectKey.value))
const selectedAgent = computed<MetaAgent | undefined>(() => meta.value.agents.find((a) => a.key === agentKey.value))
const agentOptions = computed(() => {
  const allowed = new Set(selectedProject.value?.allowed_agents ?? [])
  return meta.value.agents
    .filter((agent) => allowed.size === 0 || allowed.has(agent.key))
    .filter((agent) => {
      if (mode.value === 'terminal') return !!agent.interactive && selectedProject.value?.allow_interactive !== false
      if (mode.value === 'conversation') return agent.type === 'acp-agent'
      return agent.batch === true || agent.batch == null || agent.type === 'exec'
    })
    .sort((a, b) => a.key.localeCompare(b.key))
})
const modeOptions = computed<Array<{ value: ComposerMode; label: string }>>(() => {
  const all: Array<{ value: ComposerMode; label: string }> = [{ value: 'batch', label: '批处理 · 日志' }]
  const project = selectedProject.value
  const projectAgents = meta.value.agents.filter((agent) => (project?.allowed_agents.length ?? 0) === 0 || project?.allowed_agents.includes(agent.key))
  if (projectAgents.some((agent) => agent.type === 'acp-agent')) all.unshift({ value: 'conversation', label: 'ACP 对话' })
  if (project?.allow_interactive !== false && projectAgents.some((agent) => agent.interactive)) all.push({ value: 'terminal', label: '交互 PTY' })
  return all
})
const promptLabel = computed(() => selectedAgent.value?.type === 'exec' ? 'command' : 'prompt')
const canSubmit = computed(() => !!projectKey.value && !!agentKey.value && !!prompt.value.trim() && !submitting.value)

function agentCapability(agent: MetaAgent): string {
  const caps: string[] = []
  if (agent.interactive) caps.push('PTY')
  if (agent.type === 'acp-agent') caps.push('ACP')
  if (agent.batch || (!agent.interactive && agent.type !== 'acp-agent')) caps.push('批处理')
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

function runnerFor(project: MetaProject): string {
  return project.allowed_runners[0] ?? 'server'
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
      runner: runnerFor(selectedProject.value),
      cwd: cwd.value.trim() || '.',
      channel: 'web',
    }
    if (selectedAgent.value.type === 'exec') req.cmd = commandArgs(prompt.value)
    else if (mode.value === 'terminal') req.system_prompt = prompt.value.trim()
    else req.prompt = prompt.value.trim()
    if (mode.value === 'terminal') req.interactive = true
    if (planID.value) req.plan_id = planID.value
    if (todoID.value) req.todo_id = todoID.value
    const result = await submitJob(req)
    prompt.value = ''
    mobileOpen.value = false
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
  mobileOpen.value = true
  void nextTick(() => promptInput.value?.focus())
}

defineExpose({ focusPrompt })

watch(projectKey, () => {
  chooseDefaultAgent()
  void refreshPlans()
})
watch(mode, chooseDefaultAgent)
watch(planID, () => void loadTodos())

onMounted(async () => {
  loading.value = true
  try {
    meta.value = await getMeta()
    projectKey.value = meta.value.projects[0]?.key ?? ''
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
  <section class="composer" aria-label="发起新会话">
    <button v-if="!mobileOpen" class="mobile-launch mono" type="button" aria-label="发起新会话" @click="mobileOpen = true">＋</button>
    <div class="composer-panel" :class="{ 'mobile-open': mobileOpen }">
      <button class="mobile-close mono" type="button" aria-label="收起发起面板" @click="mobileOpen = false">×</button>
      <div class="composer-selects">
      <select v-model="projectKey" class="field mono" aria-label="项目" :disabled="loading">
        <option value="" disabled>项目</option>
        <option v-for="project in meta.projects" :key="project.key" :value="project.key">{{ project.key }}</option>
      </select>
      <select v-model="agentKey" class="field mono" aria-label="Agent" :disabled="loading">
        <option value="" disabled>agent</option>
        <option v-for="agent in agentOptions" :key="agent.key" :value="agent.key">{{ agent.key }} · {{ agentCapability(agent) }}</option>
      </select>
      <select v-model="mode" class="field mono" aria-label="模式">
        <option v-for="item in modeOptions" :key="item.value" :value="item.value">{{ item.label }}</option>
      </select>
      <select v-model="planID" class="field mono" aria-label="计划">
        <option value="">不关联 plan</option>
        <option v-for="plan in plans" :key="plan.plan_id" :value="plan.plan_id">{{ plan.title || plan.plan_id }}</option>
      </select>
      <select v-model="todoID" class="field mono" aria-label="计划待办" :disabled="!planID">
        <option value="">不关联 todo</option>
        <option v-for="todo in todos" :key="todo.todo_id" :value="todo.todo_id">{{ todo.title }}</option>
      </select>
      <input v-model="cwd" class="field cwd mono" aria-label="工作目录" placeholder="cwd" />
      </div>
      <div class="prompt-row">
      <textarea
        ref="promptInput"
        v-model="prompt"
        class="prompt mono"
        rows="2"
        :placeholder="`${promptLabel}…（Ctrl/Cmd+Enter 提交）`"
        @keydown="onKeydown"
      ></textarea>
      <button class="submit mono" type="button" :disabled="!canSubmit" @click="submit">
        {{ submitting ? '提交中…' : '开始' }}
      </button>
      </div>
      <p v-if="error" class="error mono">{{ error }}</p>
    </div>
  </section>
</template>

<style scoped>
.composer { min-width: 0; }
.composer-panel { min-width: 0; display: flex; flex-direction: column; gap: 7px; }
.mobile-launch, .mobile-close { display: none; }
.composer-selects { display: grid; grid-template-columns: 1.1fr 1.1fr .8fr 1fr 1fr .8fr; gap: 6px; }
.field, .prompt { min-width: 0; color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 7px; font-size: 11px; }
.field:focus, .prompt:focus { outline: 1px solid var(--phosphor); border-color: var(--phosphor); }
.prompt-row { display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 7px; }
.prompt { resize: vertical; max-height: 160px; }
.submit { align-self: stretch; color: var(--ink); background: var(--phosphor); border: 1px solid var(--phosphor); border-radius: var(--radius); padding: 0 16px; font-weight: 700; }
.submit:disabled { opacity: .45; cursor: default; }
.error { color: var(--fail); margin: 0; font-size: 11px; }
@media (max-width: 980px) { .composer-selects { grid-template-columns: repeat(3, minmax(0,1fr)); } }
@media (max-width: 620px) { .composer-selects { grid-template-columns: repeat(2, minmax(0,1fr)); } .prompt-row { grid-template-columns: 1fr; } .submit { min-height: 34px; } }
@media (max-width: 767px) {
  .composer { position: fixed; right: 12px; bottom: 12px; z-index: 80; }
  .mobile-launch { display: grid; place-items: center; width: 46px; height: 46px; color: var(--ink); background: var(--phosphor); border: 0; border-radius: 50%; font-size: 26px; box-shadow: 0 8px 24px rgba(0,0,0,.4); }
  .composer-panel { display: none; position: fixed; left: 8px; right: 8px; bottom: 8px; max-height: min(78vh, 660px); overflow-y: auto; padding: 36px 10px 10px; background: var(--panel); border: 1px solid var(--line); border-radius: 10px; box-shadow: 0 14px 40px rgba(0,0,0,.55); }
  .composer-panel.mobile-open { display: flex; }
  .mobile-close { display: block; position: absolute; top: 7px; right: 9px; color: var(--paper); background: transparent; border: 0; font-size: 20px; }
}
</style>
