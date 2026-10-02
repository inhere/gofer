<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { getMeta, getPlan, listPlans, submitJob } from '../../api/client'
import type { MetaAgent, MetaProject, MetaResp, Plan, SubmitJobReq, Todo } from '../../api/types'

const emit = defineEmits<{ (e: 'submitted', jobID: string): void }>()
defineProps<{ sessionOpen: boolean }>()

type ComposerMode = 'conversation' | 'continuous' | 'terminal' | 'batch'

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
// 发起表单平时收起，由侧栏「＋ 新会话」、命令面板或手机右下角 ＋ 打开成弹窗。
const open = ref(false)

const selectedProject = computed<MetaProject | undefined>(() => meta.value.projects.find((p) => p.key === projectKey.value))
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
    .sort((a, b) => a.key.localeCompare(b.key))
})
const modeOptions = computed<Array<{ value: ComposerMode; label: string }>>(() => {
  const all: Array<{ value: ComposerMode; label: string }> = [{ value: 'batch', label: '批处理 · 日志' }]
  const project = selectedProject.value
  const projectAgents = meta.value.agents.filter((agent) => (project?.allowed_agents.length ?? 0) === 0 || project?.allowed_agents.includes(agent.key))
  if (projectAgents.some((agent) => agent.type === 'acp-agent')) all.unshift({ value: 'conversation', label: 'ACP 对话' })
  if (!project?.worker_only && projectAgents.some((agent) => agent.type === 'acp-agent') &&
      (project?.allowed_runners.length === 0 || project?.allowed_runners.includes('local') || project?.allowed_runners.includes('server'))) {
    all.unshift({ value: 'continuous', label: 'ACP 持续会话' })
  }
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
      runner: mode.value === 'continuous' ? 'local' : runnerFor(selectedProject.value),
      cwd: cwd.value.trim() || '.',
      channel: 'web',
    }
    if (selectedAgent.value.type === 'exec') req.cmd = commandArgs(prompt.value)
    else if (mode.value === 'terminal') req.system_prompt = prompt.value.trim()
    else req.prompt = prompt.value.trim()
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
    <button v-if="!open && !sessionOpen" class="mobile-launch mono" type="button" aria-label="发起新会话" @click="focusPrompt">＋</button>
    <div v-if="open" class="composer-backdrop" @click.self="close">
    <div class="composer-panel" role="dialog" aria-modal="true" aria-label="新会话" @keydown="onPanelKeydown">
      <div class="panel-head mono">
        <span>新会话</span>
        <button class="panel-close mono" type="button" aria-label="关闭" @click="close">×</button>
      </div>
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
    </div>
  </section>
</template>

<style scoped>
.composer { min-width: 0; }
.composer-backdrop { position: fixed; inset: 0; z-index: 90; display: flex; align-items: flex-start; justify-content: center; padding-top: 12vh; background: rgba(0,0,0,.45); }
.composer-panel { width: min(880px, 94vw); max-height: 80vh; overflow-y: auto; display: flex; flex-direction: column; gap: 8px; padding: 12px 14px 14px; background: var(--panel); border: 1px solid var(--line); border-radius: 10px; box-shadow: 0 14px 40px rgba(0,0,0,.55); }
.panel-head { display: flex; align-items: center; justify-content: space-between; color: var(--paper); font-size: 13px; font-weight: 600; }
.panel-close { color: var(--paper); background: transparent; border: 0; font-size: 20px; line-height: 1; cursor: pointer; }
.mobile-launch { display: none; }
.composer-selects { display: grid; grid-template-columns: repeat(3, minmax(0,1fr)); gap: 6px; }
.field, .prompt { min-width: 0; color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 7px; font-size: 12px; }
.field:focus, .prompt:focus { outline: 1px solid var(--phosphor); border-color: var(--phosphor); }
.prompt-row { display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 7px; }
.prompt { resize: vertical; min-height: 110px; max-height: 50vh; }
.submit { align-self: stretch; color: var(--ink); background: var(--phosphor); border: 1px solid var(--phosphor); border-radius: var(--radius); padding: 0 16px; font-weight: 700; }
.submit:disabled { opacity: .45; cursor: default; }
.error { color: var(--fail); margin: 0; font-size: 11px; }
@media (max-width: 620px) { .composer-selects { grid-template-columns: repeat(2, minmax(0,1fr)); } .prompt-row { grid-template-columns: 1fr; } .submit { min-height: 34px; } }
@media (max-width: 767px) {
  /* 手机：会话打开时由工作台顶栏提供 ＋，表单从底部弹出 */
  .mobile-launch { display: none; }
  .composer-backdrop { align-items: flex-end; padding: 8px; }
  .composer-panel { width: 100%; max-height: min(82vh, 680px); }
}
</style>
