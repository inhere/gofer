<script setup lang="ts">
// 新建 workflow 两种方式（页签）：
//  - 从模板新建（默认，Z4）：选项目 → 选模板（内置 / 全局 / 项目 .gofer/workflows）→ 按模板 vars 生成表单
//    （agent / runner / project 类变量用下拉，按项目 allowed_* 过滤）→ 预览渲染后的步骤 → 提交（POST /v1/workflows {template, vars}）。
//  - YAML：inline spec，前端解析成 workflow.Spec JSON 再 POST /v1/workflows。
import { load as loadYaml } from 'js-yaml'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { getMeta, listWorkflowTemplates, renderWorkflowTemplate, submitWorkflow, submitWorkflowFromTemplate } from '../api/client'
import type { MetaAgent, MetaProject, MetaRunner, MetaWorker, WorkflowSpec, WorkflowTemplateInfo } from '../api/types'
import { computeRunnerBlocks } from '../utils/runnerChoice'
import { runnerOptionText } from '../utils/runnerDisplay'
import {
  adaptAgentVars,
  agentOptions,
  cleanVars,
  initialValues,
  previewSteps,
  runnerPickOptions,
  sourceLabel,
  validateVars,
  varFields,
  type PreviewStep,
  type VarField,
} from '../utils/workflowTemplate'

const router = useRouter()

const loading = ref(true)
const loadError = ref('')
const submitting = ref(false)
const submitError = ref('')
const yamlText = ref('')

const projects = ref<MetaProject[]>([])
const agents = ref<MetaAgent[]>([])
const runners = ref<MetaRunner[]>([])
const workers = ref<MetaWorker[]>([])

// ---- 从模板新建 ----
const mode = ref<'template' | 'yaml'>('template')
const tplProject = ref('')
const templates = ref<WorkflowTemplateInfo[]>([])
const tplLoading = ref(false)
const tplError = ref('')
const tplName = ref('')
const tplValues = reactive<Record<string, string>>({})
const tplTouched = ref(false)
const tplNotes = ref<string[]>([])
const preview = ref<PreviewStep[] | null>(null)
const previewError = ref('')
const previewing = ref(false)

const selectedProject = computed(() => projects.value.find((p) => p.key === tplProject.value))
const selectedTpl = computed(() => templates.value.find((t) => t.name === tplName.value))
const fields = computed<VarField[]>(() => (selectedTpl.value ? varFields(selectedTpl.value.spec) : []))
const formFields = computed(() => fields.value.filter((f) => f.kind !== 'project'))
const errors = computed(() => validateVars(fields.value, tplValues))
const runnerBlocks = computed(() =>
  computeRunnerBlocks(selectedProject.value, runners.value, workers.value),
)
const hasErrors = computed(() => Object.keys(errors.value).length > 0)

function optionsFor(f: VarField) {
  if (f.kind === 'agent') return agentOptions(selectedProject.value, agents.value, tplValues[f.name] ?? '')
  return runnerPickOptions(selectedProject.value, runners.value, runnerBlocks.value, tplValues[f.name] ?? '', (r) => runnerOptionText(r))
}

async function loadTemplates(): Promise<void> {
  tplLoading.value = true
  tplError.value = ''
  try {
    const resp = await listWorkflowTemplates(tplProject.value || undefined)
    templates.value = resp.templates ?? []
    if (!templates.value.some((t) => t.name === tplName.value)) {
      pickTemplate(templates.value[0]?.name ?? '')
    }
  } catch (e) {
    templates.value = []
    tplError.value = e instanceof Error ? e.message : String(e)
  } finally {
    tplLoading.value = false
  }
}

function pickTemplate(name: string): void {
  tplName.value = name
  tplTouched.value = false
  preview.value = null
  previewError.value = ''
  for (const k of Object.keys(tplValues)) delete tplValues[k]
  const tpl = templates.value.find((t) => t.name === name)
  if (tpl) Object.assign(tplValues, initialValues(varFields(tpl.spec), tplProject.value))
  adaptAgents()
}

// 模板默认 agent 在所选项目未开放时，自动换成该项目第一个可用的同类 agent，并给出提示。
function adaptAgents(): void {
  const r = adaptAgentVars(fields.value, { ...tplValues }, selectedProject.value, agents.value)
  Object.assign(tplValues, r.values)
  tplNotes.value = r.notes
}

// 项目变了：重拉模板（项目目录里可能有自己的模板），project 变量同步，agent/runner 当前值若不再可用保持可见。
watch(tplProject, (key) => {
  for (const f of fields.value) if (f.kind === 'project') tplValues[f.name] = key
  preview.value = null
  adaptAgents()
  void loadTemplates()
})

async function onPreview(): Promise<void> {
  tplTouched.value = true
  previewError.value = ''
  preview.value = null
  if (hasErrors.value) return
  previewing.value = true
  try {
    const spec = await renderWorkflowTemplate(tplName.value, cleanVars(fields.value, tplValues))
    preview.value = previewSteps(spec)
  } catch (e) {
    previewError.value = e instanceof Error ? e.message : String(e)
  } finally {
    previewing.value = false
  }
}

async function onSubmitTemplate(): Promise<void> {
  tplTouched.value = true
  if (submitting.value || hasErrors.value || !tplName.value) return
  submitting.value = true
  submitError.value = ''
  try {
    const wf = await submitWorkflowFromTemplate(tplName.value, cleanVars(fields.value, tplValues))
    void router.push(`/workflows/${encodeURIComponent(wf.id)}`)
  } catch (e) {
    submitError.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}

interface YamlMark {
  line?: number
  column?: number
}

interface YamlErrorLike {
  message?: string
  reason?: string
  mark?: YamlMark
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function yamlErrorMessage(e: unknown): string {
  const err = e as YamlErrorLike
  const parts: string[] = []
  if (err.reason) {
    parts.push(err.reason)
  } else if (err.message) {
    parts.push(err.message)
  } else {
    parts.push(String(e))
  }
  if (err.mark) {
    const line = err.mark.line != null ? err.mark.line + 1 : undefined
    const column = err.mark.column != null ? err.mark.column + 1 : undefined
    if (line != null && column != null) {
      parts.push(`行 ${line}，列 ${column}`)
    } else if (line != null) {
      parts.push(`行 ${line}`)
    }
  }
  return parts.join('；')
}

function firstAgentFor(project?: MetaProject): MetaAgent | undefined {
  if (!project) {
    return agents.value[0]
  }
  const allowed = project.allowed_agents ?? []
  if (allowed.length === 0) {
    return agents.value.find((a) => a.key === project.default_agent) ?? agents.value[0]
  }
  const def = project.default_agent
  if (def && allowed.includes(def)) {
    return agents.value.find((a) => a.key === def)
  }
  return agents.value.find((a) => allowed.includes(a.key))
}

function firstRunnerFor(project?: MetaProject): MetaRunner | undefined {
  if (!project) {
    return runners.value[0]
  }
  const allowed = project.allowed_runners ?? []
  if (allowed.length === 0) {
    return runners.value[0]
  }
  return runners.value.find((r) => allowed.includes(r.name))
}

function stepBody(agent?: MetaAgent): string {
  if (agent?.type === 'exec') {
    return `    cmd: ["echo", "step output"]`
  }
  return `    prompt: |
      说明当前步骤要完成的工作。`
}

function exampleTemplate(): string {
  const project = projects.value[0]
  const agent = firstAgentFor(project)
  const runner = firstRunnerFor(project)
  const projectKey = project?.key ?? 'your-project'
  const agentKey = agent?.key ?? 'your-agent'
  const runnerName = runner?.name ?? 'local'
  const body = stepBody(agent)

  return `# 纯 inline workflow spec；字段名使用后端 JSON/YAML tag。
# title: 工作流标题，可选。
# steps: 必填，非空数组；每一步都会按 project_key/agent/runner 准入校验。
# name: 步骤名，可选。
# project_key/agent/runner: 必填，和单个 job 提交一致。
# prompt: cli-agent 类 agent 的输入正文；exec 类 agent 通常改用 cmd。
# cmd: exec 命令 argv 数组，例如 ["go", "test", "./..."]。
# cwd: 相对项目执行目录，可选，默认由后端处理。
# timeout_sec/tags: 可选。
# on_failure: 可选，fail/continue/retry；retry 需要 retry.max_attempts。
title: web-inline-workflow
steps:
  - name: prepare
    project_key: ${projectKey}
    agent: ${agentKey}
    runner: ${runnerName}
    cwd: .
    timeout_sec: 300
    tags: [web, workflow]
${body}

  - name: verify
    project_key: ${projectKey}
    agent: ${agentKey}
    runner: ${runnerName}
    cwd: .
    timeout_sec: 300
${body}

  - name: summarize
    project_key: ${projectKey}
    agent: ${agentKey}
    runner: ${runnerName}
    cwd: .
    timeout_sec: 300
    on_failure: fail
${body}
`
}

async function loadMeta() {
  loading.value = true
  loadError.value = ''
  try {
    const m = await getMeta()
    // workflows 不支持 worker-only project（无 host 配置、不能本地/串行编排跑）→ 过滤掉，
    // 使默认 project 选取 / firstAgentFor / firstRunnerFor 行为与之前保持一致。
    projects.value = (m.projects ?? []).filter((p) => !p.worker_only)
    agents.value = m.agents ?? []
    runners.value = m.runners ?? []
    workers.value = m.workers ?? []
    tplProject.value = projects.value[0]?.key ?? ''
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    yamlText.value = exampleTemplate()
    loading.value = false
    // 有项目时由 watch(tplProject) 触发拉取；没有项目只能看内置 / 全局模板
    if (!tplProject.value) void loadTemplates()
  }
}

function parseSpec(): WorkflowSpec | null {
  submitError.value = ''
  let parsed: unknown
  try {
    parsed = loadYaml(yamlText.value)
  } catch (e) {
    submitError.value = `YAML 解析失败：${yamlErrorMessage(e)}`
    return null
  }
  if (!isRecord(parsed)) {
    submitError.value = 'YAML 根节点必须是对象，至少包含 steps 数组'
    return null
  }
  if (!Array.isArray(parsed.steps) || parsed.steps.length === 0) {
    submitError.value = 'steps 必须是非空数组'
    return null
  }
  return parsed as unknown as WorkflowSpec
}

async function onSubmit() {
  if (submitting.value) {
    return
  }
  const spec = parseSpec()
  if (!spec) {
    return
  }
  submitting.value = true
  submitError.value = ''
  try {
    const wf = await submitWorkflow(spec)
    void router.push(`/workflows/${encodeURIComponent(wf.id)}`)
  } catch (e) {
    submitError.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}

function resetTemplate() {
  yamlText.value = exampleTemplate()
  submitError.value = ''
}

onMounted(() => {
  void loadMeta()
})
</script>

<template>
  <div class="newwf">
    <div class="newwf-head">
      <RouterLink to="/workflows" class="back mono">← workflows</RouterLink>
      <h1 class="title mono">新建 workflow</h1>
    </div>

    <p v-if="loadError" class="error mono">示例模板未能读取 meta，已使用占位值：{{ loadError }}</p>
    <p v-else-if="loading" class="hint mono">加载中…</p>

    <div v-if="!loading" class="tabs mono" role="tablist">
      <button class="tab" :class="{ 'tab--on': mode === 'template' }" type="button" role="tab" data-test="tab-template" @click="mode = 'template'">从模板新建</button>
      <button class="tab" :class="{ 'tab--on': mode === 'yaml' }" type="button" role="tab" data-test="tab-yaml" @click="mode = 'yaml'">YAML</button>
    </div>

    <div v-if="!loading && mode === 'template'" class="card" data-test="template-pane">
      <div class="field">
        <label class="label mono" for="nw-project">项目（决定可用的 agent / runner 与项目内模板）</label>
        <select id="nw-project" v-model="tplProject" class="control mono" data-test="tpl-project">
          <option v-for="p in projects" :key="p.key" :value="p.key">{{ p.key }}</option>
        </select>
      </div>

      <div class="field">
        <span class="label mono">模板</span>
        <p v-if="tplLoading" class="hint mono">加载模板…</p>
        <p v-else-if="tplError" class="error mono">{{ tplError }}</p>
        <p v-else-if="templates.length === 0" class="hint mono">没有可用模板</p>
        <ul v-else class="tpl-list" data-test="tpl-list">
          <li v-for="t in templates" :key="t.name">
            <button
              class="tpl-card"
              :class="{ 'tpl-card--on': t.name === tplName }"
              type="button"
              :data-test="`tpl-${t.name}`"
              @click="pickTemplate(t.name)"
            >
              <span class="tpl-name mono">{{ t.name }}</span>
              <span class="tpl-src mono" :class="`tpl-src--${t.source}`">{{ sourceLabel(t.source) }}</span>
              <span class="tpl-desc">{{ t.desc || t.spec.title || '' }}</span>
            </button>
          </li>
        </ul>
      </div>

      <template v-if="selectedTpl">
        <p v-for="n in tplNotes" :key="n" class="hint mono" data-test="agent-adapted">{{ n }}</p>
        <div v-for="f in formFields" :key="f.name" class="field" :data-test="`var-${f.name}`">
          <label class="label mono" :for="`nw-var-${f.name}`">
            {{ f.name }}<span v-if="f.required" class="req"> *</span>
            <span v-if="f.desc" class="vdesc"> — {{ f.desc }}</span>
          </label>
          <select
            v-if="f.kind === 'agent' || f.kind === 'runner'"
            :id="`nw-var-${f.name}`"
            v-model="tplValues[f.name]"
            class="control mono"
          >
            <option v-for="o in optionsFor(f)" :key="o.value" :value="o.value" :disabled="o.disabled">{{ o.label }}</option>
          </select>
          <textarea v-else-if="f.kind === 'textarea'" :id="`nw-var-${f.name}`" v-model="tplValues[f.name]" class="control mono small-area" rows="4"></textarea>
          <input v-else :id="`nw-var-${f.name}`" v-model="tplValues[f.name]" class="control mono" type="text" />
          <span v-if="tplTouched && errors[f.name]" class="field-err mono">{{ errors[f.name] }}</span>
        </div>

        <div class="toolbar mono">
          <button class="plain-btn" type="button" :disabled="previewing" data-test="tpl-preview" @click="onPreview">
            {{ previewing ? '渲染中…' : '预览步骤' }}
          </button>
        </div>
        <p v-if="previewError" class="error mono" data-test="preview-error">{{ previewError }}</p>
        <ol v-if="preview" class="preview" data-test="preview">
          <li v-for="st in preview" :key="st.index" class="pv-step">
            <div class="pv-head mono">
              <span class="pv-idx">{{ st.index }}</span>
              <span class="pv-name">{{ st.name }}</span>
              <span class="pv-who">{{ st.who }}</span>
              <span v-if="st.runner" class="pv-runner">@ {{ st.runner }}</span>
              <span v-for="b in st.badges" :key="b" class="pv-badge">{{ b }}</span>
            </div>
            <p v-if="st.summary" class="pv-sum">{{ st.summary }}</p>
          </li>
        </ol>

        <p v-if="submitError" class="error mono">{{ submitError }}</p>
        <button class="submit" type="button" :disabled="submitting || !tplName" data-test="tpl-submit" @click="onSubmitTemplate">
          {{ submitting ? '提交中…' : '提交 workflow' }}
        </button>
      </template>
    </div>

    <form v-else-if="!loading && mode === 'yaml'" class="card" @submit.prevent="onSubmit">
      <div class="toolbar mono">
        <span class="template-note">INLINE YAML SPEC</span>
        <button class="plain-btn" type="button" @click="resetTemplate">
          重置模板
        </button>
      </div>

      <div class="field">
        <label class="label mono" for="nw-yaml">WORKFLOW YAML</label>
        <textarea
          id="nw-yaml"
          v-model="yamlText"
          class="control mono area"
          spellcheck="false"
        ></textarea>
      </div>

      <p v-if="submitError" class="error mono">{{ submitError }}</p>

      <button class="submit" type="submit" :disabled="submitting">
        {{ submitting ? '提交中…' : '提交 workflow' }}
      </button>
    </form>
  </div>
</template>

<style scoped>
.newwf {
  max-width: 900px;
  margin: 0 auto;
}
.newwf-head {
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

.tabs {
  display: flex;
  gap: 6px;
  margin-bottom: 12px;
}
.tab {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 6px 14px;
  font-size: 13px;
}
.tab--on {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.tpl-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 8px;
}
.tpl-card {
  width: 100%;
  height: 100%;
  text-align: left;
  display: flex;
  flex-direction: column;
  gap: 4px;
  background: var(--ink);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px;
}
.tpl-card--on {
  border-color: var(--phosphor);
}
.tpl-name {
  font-size: 13px;
  color: var(--phosphor);
}
.tpl-src {
  align-self: flex-start;
  font-size: 10px;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 5px;
}
.tpl-desc {
  font-size: 12px;
  color: var(--queue);
  line-height: 1.4;
}
.req {
  color: var(--fail);
}
.vdesc {
  color: var(--queue);
  letter-spacing: 0;
  text-transform: none;
}
.field-err {
  color: var(--fail);
  font-size: 11px;
  margin-top: 4px;
}
.small-area {
  min-height: 90px;
  resize: vertical;
}
.preview {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.pv-step {
  padding: 8px 10px;
  border-bottom: 1px solid var(--line);
}
.pv-step:last-child {
  border-bottom: none;
}
.pv-head {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: baseline;
  font-size: 12px;
}
.pv-idx {
  color: var(--phosphor);
}
.pv-name {
  color: var(--paper);
  font-weight: 600;
}
.pv-who,
.pv-runner {
  color: var(--queue);
}
.pv-badge {
  font-size: 10px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 5px;
  color: var(--phosphor);
}
.pv-sum {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--queue);
  word-break: break-word;
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

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.template-note {
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.08em;
}
.plain-btn {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px 10px;
  font-size: 12px;
}
.plain-btn:hover {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

.field {
  display: flex;
  flex-direction: column;
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
  padding: 10px;
  font-size: 13px;
  line-height: 1.45;
  outline: none;
}
.control:focus {
  border-color: var(--phosphor);
}
.area {
  min-height: 560px;
  resize: vertical;
  tab-size: 2;
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

@media (max-width: 640px) {
  .card {
    padding: 14px;
  }
  .toolbar {
    align-items: flex-start;
    flex-direction: column;
  }
  .area {
    min-height: 460px;
  }
}
</style>
