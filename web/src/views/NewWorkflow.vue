<script setup lang="ts">
// 新建 workflow 两种方式（页签）：
//  - 从模板新建（默认，Z4）：选项目 → 选模板（内置 / 全局 / 项目 .gofer/workflows）→ 按模板 vars 生成表单
//    （agent / runner / project 类变量用下拉，按项目 allowed_* 过滤）→ 预览渲染后的步骤 → 提交（POST /v1/workflows {template, vars}）。
//  - YAML：inline spec，前端解析成 workflow.Spec JSON 再 POST /v1/workflows。
import { load as loadYaml } from 'js-yaml'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getMetaCached } from '../api/metaCache'
import {
  deleteWorkflowTemplate,
  getWorkflowTemplateSource,
  listWorkflowTemplates,
  renderWorkflowTemplate,
  saveWorkflowTemplate,
  submitWorkflow,
  submitWorkflowFromTemplate,
} from '../api/client'
import type {
  MetaAgent,
  MetaProject,
  MetaRunner,
  MetaWorker,
  WorkflowSpec,
  WorkflowTemplateInfo,
  WorkflowTemplateSource,
} from '../api/types'
import { computeRunnerBlocks } from '../utils/runnerChoice'
import { runnerOptionText } from '../utils/runnerDisplay'
import { t } from '../i18n'
import Tabs from '../ui/Tabs.vue'
import BackLink from '../ui/BackLink.vue'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import { useWhoami } from '../store/whoami'
import { ceilingAllowsAgent, ceilingAllowsExec, ceilingAllowsProject, ceilingAllowsRunner } from '../utils/ceiling'
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
import {
  overridesBuiltinNotice,
  shadowsNote,
  templateDeleteNotice,
  templateEditGate,
  templateReadOnlyNote,
  templateSaveNotice,
  type SaveNotice,
  type TemplateScope,
} from '../utils/workflowResult'

const router = useRouter()
const route = useRoute()
// 调用方上限（ceiling）：本人 token 的范围上界（utils/ceiling.ts），用于收窄向导里的候选。
const { ceiling: ceil, whoami, can } = useWhoami()

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
// 页签的当前值以 URL 的 ?tab= 为准（ui/Tabs 负责读写），可深链。
const mode = computed<'template' | 'yaml'>(() => (route.query.tab === 'yaml' ? 'yaml' : 'template'))
const modeTabs = computed(() => [
  { value: 'template', label: t('workflows.new.tabTemplate') },
  { value: 'yaml', label: t('workflows.new.tabYaml') },
])
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

// ---- 模板源文件 / 编辑（D7）----
// 查看：GET …/{name}/source（来源、路径、原文、盖住了哪些层）；编辑 / 新建 / 删除需要
// config.write。项目范围的保存只是工作区里的未提交改动，页面必须说清楚并给出提交命令。
const showSource = ref(false)
const src = ref<WorkflowTemplateSource | null>(null)
const srcLoading = ref(false)
const srcError = ref('')
const editing = ref(false)
const editText = ref('')
const saveScope = ref<TemplateScope>('project')
const saving = ref(false)
const saveError = ref('')
const notice = ref<SaveNotice | null>(null)
const deleteNotice = ref('')
const confirmingDelete = ref(false)
const removing = ref(false)
const creating = ref(false)
const newName = ref('')

const gated = computed(() => templateEditGate(can('config.write'), whoami.value != null))
const editingName = computed(() => (creating.value ? newName.value.trim() : tplName.value))
// 删除针对模板所在那一层：内置没有文件，只能做全局 / 项目（无文件时 404）。
const deleteScope = computed<TemplateScope>(() => {
  const s = src.value?.source
  return s === 'global' || s === 'project' ? s : saveScope.value
})
const readOnlyNote = computed(() => templateReadOnlyNote(src.value?.source))
const shadowText = computed(() => shadowsNote(src.value?.shadows))
const overrideNote = computed(() =>
  overridesBuiltinNotice(editingName.value, selectedTpl.value?.source, saveScope.value),
)
const canDelete = computed(() => {
  const s = src.value?.source
  return !!tplName.value && (s === 'global' || s === 'project')
})

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
  // 源文件面板跟着选项重置：换了模板就不该再显示上一份的文本或编辑区
  showSource.value = false
  src.value = null
  srcError.value = ''
  editing.value = false
  creating.value = false
  saveError.value = ''
  notice.value = null
  deleteNotice.value = ''
  confirmingDelete.value = false
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
  saveScope.value = key ? 'project' : 'global'
  adaptAgents()
  void loadTemplates()
})

// 模板源文件：打开时拉一次（内置模板取它的 spec 序列化文本）。
async function loadSource(): Promise<void> {
  if (!tplName.value) {
    src.value = null
    return
  }
  srcLoading.value = true
  srcError.value = ''
  try {
    src.value = await getWorkflowTemplateSource(tplName.value, tplProject.value || undefined)
  } catch (e) {
    src.value = null
    srcError.value = e instanceof Error ? e.message : String(e)
  } finally {
    srcLoading.value = false
  }
}

function toggleSource(): void {
  showSource.value = !showSource.value
  editing.value = false
  creating.value = false
  saveError.value = ''
  if (showSource.value && !src.value) void loadSource()
}

function startEdit(): void {
  if (!tplName.value || !gated.value.editable) return
  creating.value = false
  showSource.value = true
  editText.value = src.value?.content ?? ''
  saveScope.value = src.value?.source === 'global' ? 'global' : tplProject.value ? 'project' : 'global'
  editing.value = true
  saveError.value = ''
  notice.value = null
  deleteNotice.value = ''
  confirmingDelete.value = false
}

function newTemplateText(): string {
  const project = tplProject.value || 'your-project'
  return [
    t('workflows.newTemplate.comment'),
    t('workflows.newTemplate.title'),
    'vars:',
    '  project: {default: ' + project + '}',
    'steps:',
    '  - name: prepare',
    '    project_key: ${vars.project}',
    '    agent: exec',
    '    runner: local',
    '    cmd: ["echo", "hello"]',
    '',
  ].join('\n')
}

function startCreate(): void {
  if (!gated.value.editable) return
  creating.value = true
  editing.value = true
  showSource.value = true
  src.value = null
  newName.value = ''
  editText.value = newTemplateText()
  saveScope.value = tplProject.value ? 'project' : 'global'
  saveError.value = ''
  notice.value = null
  deleteNotice.value = ''
  confirmingDelete.value = false
}

function cancelEdit(): void {
  const wasCreating = creating.value
  editing.value = false
  creating.value = false
  saveError.value = ''
  if (!wasCreating && !src.value) void loadSource()
}

async function onSaveTemplate(): Promise<void> {
  const name = editingName.value
  if (!name || saving.value || !gated.value.editable) return
  if (!/^[A-Za-z0-9._-]+$/.test(name)) {
    saveError.value = t('workflows.new.templateNameRule')
    return
  }
  saving.value = true
  saveError.value = ''
  notice.value = null
  try {
    const r = await saveWorkflowTemplate(name, saveScope.value, editText.value, tplProject.value || undefined)
    const wasCreating = creating.value
    editing.value = false
    creating.value = false
    await loadTemplates()
    if (wasCreating) pickTemplate(name)
    showSource.value = true
    await loadSource()
    // 提示放在重新拉模板之后：pickTemplate 会清空上一份面板状态
    notice.value = templateSaveNotice(r)
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

async function onDeleteTemplate(): Promise<void> {
  if (!tplName.value || removing.value || !gated.value.editable) return
  if (!confirmingDelete.value) {
    confirmingDelete.value = true
    return
  }
  removing.value = true
  saveError.value = ''
  deleteNotice.value = ''
  try {
    const r = await deleteWorkflowTemplate(tplName.value, deleteScope.value, tplProject.value || undefined)
    confirmingDelete.value = false
    editing.value = false
    src.value = null
    await loadTemplates()
    if (templates.value.some((t) => t.name === tplName.value) && showSource.value) await loadSource()
    // 回落到哪一层要说清楚；同样放在重拉列表之后
    deleteNotice.value = templateDeleteNotice(r)
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    removing.value = false
  }
}

// 提交命令一键复制（写不进去也不算错：用户还能手选）。
const copiedCommand = ref(false)
async function copyCommitCommand(cmd: string): Promise<void> {
  if (!cmd) return
  try {
    await navigator.clipboard.writeText(cmd)
    copiedCommand.value = true
  } catch {
    copiedCommand.value = false
  }
}

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
      parts.push(t('workflows.new.yamlLineCol', { line, col: column }))
    } else if (line != null) {
      parts.push(t('workflows.new.yamlLine', { line }))
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
      ${t('workflows.newTemplate.stepBody')}`
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
# run: 多行脚本（与 cmd 互斥）；shell: bash（默认）/sh/pwsh/python。
# if: always 表示前面步骤失败后这一步仍然执行（不改变最终结论）。
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
    const m = await getMetaCached()
    // workflows 不支持 worker-only project（无 host 配置、不能本地/串行编排跑）→ 过滤掉，
    // 使默认 project 选取 / firstAgentFor / firstRunnerFor 行为与之前保持一致。
    projects.value = (m.projects ?? []).filter((p) => !p.worker_only)
    agents.value = m.agents ?? []
    runners.value = m.runners ?? []
    workers.value = m.workers ?? []
    // 调用方上限（ceiling）：范围外的 project / agent / runner 不进向导（模板变量下拉与步骤预览
    // 都从这里取候选），否则会摆出一批提交必被拒的选项；exec 类还要 allow_exec。
    projects.value = projects.value.filter((p) => ceilingAllowsProject(ceil.value, p.key))
    agents.value = agents.value.filter(
      (a) => ceilingAllowsAgent(ceil.value, a.key) && (ceilingAllowsExec(ceil.value) || a.type !== 'exec'),
    )
    runners.value = runners.value.filter((r) => ceilingAllowsRunner(ceil.value, r.name))
    tplProject.value = projects.value[0]?.key ?? ''
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    yamlText.value = exampleTemplate()
    loading.value = false
    saveScope.value = tplProject.value ? 'project' : 'global'
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
    submitError.value = t('workflows.new.yamlParseFailed', { err: yamlErrorMessage(e) })
    return null
  }
  if (!isRecord(parsed)) {
    submitError.value = t('workflows.new.yamlRootMustBeObject')
    return null
  }
  if (!Array.isArray(parsed.steps) || parsed.steps.length === 0) {
    submitError.value = t('workflows.new.yamlStepsRequired')
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

// YAML 编辑器的行号栏：与 textarea 同字号、同滚动位置（设计稿 §7.2「等宽字体加行号」）。
const yamlArea = ref<HTMLTextAreaElement | null>(null)
const gutter = ref<HTMLElement | null>(null)
const lineNumbers = computed(() =>
  yamlText.value.split('\n').map((_, i) => i + 1).join('\n'),
)

function syncScroll(): void {
  if (gutter.value && yamlArea.value) {
    gutter.value.scrollTop = yamlArea.value.scrollTop
  }
}

onMounted(() => {
  void loadMeta()
})
</script>

<template>
  <div class="newwf">
    <div class="newwf-head">
      <BackLink list="workflows" to="/workflows" :label="t('workflows.detail.back')" />
      <h1 class="title mono">{{ t('workflows.new.title') }}<ExperimentalBadge /></h1>
    </div>

    <p v-if="loadError" class="error mono">{{ t('workflows.new.metaFailed', { err: loadError }) }}</p>
    <p v-else-if="loading" class="hint mono">{{ t('workflows.new.loading') }}</p>

    <Tabs v-if="!loading" :tabs="modeTabs" :label="t('workflows.new.title')" />

    <div v-if="!loading && mode === 'template'" class="card" data-test="template-pane">
      <div class="field">
        <label class="label mono" for="nw-project">{{ t('workflows.new.project') }}</label>
        <select id="nw-project" v-model="tplProject" class="control mono" data-test="tpl-project">
          <option v-for="p in projects" :key="p.key" :value="p.key">{{ p.key }}</option>
        </select>
        <p class="field-hint mono">{{ t('workflows.new.projectHint') }}</p>
      </div>

      <div class="field">
        <span class="label mono">{{ t('workflows.new.template') }}</span>
        <p v-if="tplLoading" class="hint mono">{{ t('workflows.new.loadingTemplates') }}</p>
        <p v-else-if="tplError" class="error mono">{{ tplError }}</p>
        <p v-else-if="templates.length === 0" class="hint mono">{{ t('workflows.new.noTemplates') }}</p>
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
        <div v-if="!tplLoading && templates.length" class="toolbar mono tpl-tools">
          <button v-if="selectedTpl" class="plain-btn" type="button" data-test="tpl-view-source" @click="toggleSource">
            {{ showSource ? t('workflows.new.hideSource') : t('workflows.new.viewSource') }}
          </button>
          <button
            v-if="gated.editable"
            class="plain-btn"
            type="button"
            data-test="tpl-new"
            @click="startCreate"
          >
            {{ t('workflows.newTemplate.create') }}
          </button>
        </div>
        <p v-if="!gated.editable && templates.length" class="hint mono" data-test="tpl-readonly-reason">
          {{ gated.reason }}
        </p>
      </div>

      <section v-if="showSource" class="src-pane" data-test="tpl-source">
        <div class="src-head mono">
          <span class="src-name">{{ editingName || t('workflows.new.unnamed') }}</span>
          <span v-if="creating" class="tpl-src tpl-src--new">{{ t('workflows.new.newBadge') }}</span>
          <span v-else-if="src" class="tpl-src" :class="`tpl-src--${src.source}`">{{ sourceLabel(src.source) }}</span>
          <span v-if="src?.path" class="src-path" :title="src.path">{{ src.path }}</span>
        </div>
        <p v-if="srcLoading" class="hint mono">{{ t('workflows.new.readingSource') }}</p>
        <p v-if="srcError" class="error mono" data-test="tpl-source-error">{{ srcError }}</p>
        <p v-if="shadowText" class="hint mono" data-test="tpl-shadows">{{ shadowText }}</p>
        <p v-if="readOnlyNote && !editing" class="hint mono" data-test="tpl-builtin-note">{{ readOnlyNote }}</p>

        <div v-if="creating" class="field">
          <label class="label mono" for="tpl-new-name">{{ t('workflows.new.sourceName') }}（{{ t('workflows.new.sourceNameHint') }}）</label>
          <input id="tpl-new-name" v-model="newName" class="control mono" type="text" data-test="tpl-new-name" />
        </div>

        <textarea
          v-if="editing"
          v-model="editText"
          class="control mono area-src"
          spellcheck="false"
          data-test="tpl-source-editor"
        ></textarea>
        <pre v-else-if="src" class="src-text mono" data-test="tpl-source-text">{{ src.content }}</pre>

        <div v-if="editing" class="field">
          <span class="label mono">{{ t('workflows.new.saveScope') }}</span>
          <label class="radio mono"
            ><input v-model="saveScope" type="radio" value="global" data-test="tpl-scope-global" />
            {{ t('workflows.new.scopeGlobal') }}</label
          >
          <label class="radio mono" :class="{ 'radio--off': !tplProject }"
            ><input
              v-model="saveScope"
              type="radio"
              value="project"
              :disabled="!tplProject"
              data-test="tpl-scope-project"
            />
            {{ t('workflows.newTemplate.scopeProject') }}<template v-if="tplProject">（{{ tplProject }}）</template></label
          >
          <p v-if="overrideNote" class="hint mono" data-test="tpl-override-note">{{ overrideNote }}</p>
        </div>

        <p v-if="saveError" class="error mono" data-test="tpl-save-error">{{ saveError }}</p>

        <div v-if="editing" class="toolbar mono">
          <button class="plain-btn" type="button" :disabled="saving || !editingName" data-test="tpl-save" @click="onSaveTemplate">
            {{ saving ? t('workflows.new.saving') : t('workflows.new.saveTemplate') }}
          </button>
          <button class="plain-btn" type="button" :disabled="saving" data-test="tpl-cancel" @click="cancelEdit">
            {{ t('workflows.new.cancel') }}
          </button>
        </div>
        <div v-else-if="gated.editable && src" class="toolbar mono">
          <button class="plain-btn" type="button" data-test="tpl-edit" @click="startEdit">{{ t('workflows.new.edit') }}</button>
          <button
            v-if="canDelete"
            class="plain-btn plain-btn--danger"
            type="button"
            :disabled="removing"
            data-test="tpl-delete"
            @click="onDeleteTemplate"
          >
            {{ confirmingDelete ? t('workflows.new.confirmDelete') : t('workflows.new.delete') }}
          </button>
          <button v-if="confirmingDelete" class="plain-btn" type="button" @click="confirmingDelete = false">
            {{ t('workflows.new.cancelDelete') }}
          </button>
        </div>

        <p v-if="notice" class="notice mono" :class="`notice--${notice.tone}`" data-test="tpl-save-notice">
          {{ notice.text }}
        </p>
        <div v-if="notice?.command" class="notice-cmd" data-test="tpl-commit-cmd">
          <code class="mono">{{ notice.command }}</code>
          <button class="plain-btn" type="button" data-test="tpl-commit-copy" @click="copyCommitCommand(notice.command)">
            {{ copiedCommand ? t('workflows.new.copied') : t('workflows.new.copy') }}
          </button>
        </div>
        <p v-if="deleteNotice" class="notice mono notice--warn" data-test="tpl-delete-notice">{{ deleteNotice }}</p>
      </section>

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
            {{ previewing ? t('workflows.new.previewing') : t('workflows.new.preview') }}
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
          {{ submitting ? t('workflows.new.submitting') : t('workflows.new.submit') }}
        </button>
      </template>
    </div>

    <form v-else-if="!loading && mode === 'yaml'" class="card" @submit.prevent="onSubmit">
      <div class="toolbar mono">
        <span class="template-note">{{ t('workflows.new.inlineYaml') }}</span>
        <button class="plain-btn" type="button" @click="resetTemplate">
          {{ t('workflows.new.resetTemplate') }}
        </button>
      </div>

      <div class="field">
        <label class="label mono" for="nw-yaml">{{ t('workflows.new.yaml') }}</label>
        <div class="yaml-editor mono">
          <pre ref="gutter" class="yaml-gutter" aria-hidden="true">{{ lineNumbers }}</pre>
          <textarea
            id="nw-yaml"
            ref="yamlArea"
            v-model="yamlText"
            class="control mono area"
            spellcheck="false"
            @scroll="syncScroll"
          ></textarea>
        </div>
      </div>

      <p v-if="submitError" class="error mono">{{ submitError }}</p>

      <button class="submit" type="submit" :disabled="submitting">
        {{ submitting ? t('workflows.new.submitting') : t('workflows.new.submit') }}
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
  gap: var(--s3);
  margin-bottom: var(--s3);
}
/* YAML 编辑器：行号栏在左侧，与 textarea 同字号同滚（设计稿 §7.2）。 */
.yaml-editor {
  display: flex;
  align-items: stretch;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface-2);
  overflow: hidden;
}
.yaml-gutter {
  margin: 0;
  padding: 8px 6px 8px 8px;
  min-width: 44px;
  max-height: 360px;
  overflow: hidden;
  text-align: right;
  color: var(--ink-3);
  border-right: 1px solid var(--line);
  user-select: none;
  font-size: 12px;
  line-height: 1.5;
}
.yaml-editor .area {
  border: 0;
  border-radius: 0;
  background: transparent;
  max-height: 360px;
  line-height: 1.5;
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

.tabs {
  display: flex;
  gap: 6px;
  margin-bottom: 12px;
}
.tab {
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 6px 14px;
  font-size: 13px;
}
.tab--on {
  color: var(--accent);
  border-color: var(--accent);
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
  background: var(--bg);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 10px;
}
.tpl-card--on {
  border-color: var(--accent);
}
.tpl-name {
  font-size: 13px;
  color: var(--accent);
}
.tpl-src {
  align-self: flex-start;
  font-size: 10px;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 0 5px;
}
.tpl-desc {
  font-size: 12px;
  color: var(--ink-3);
  line-height: 1.4;
}

/* D7：模板源文件查看 / 编辑 */
.tpl-tools {
  margin-top: 8px;
  justify-content: flex-start;
  gap: 8px;
}
.src-pane {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.src-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 12px;
}
.src-name {
  color: var(--ink);
  font-weight: 600;
}
.src-path {
  color: var(--ink-3);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}
.tpl-src--new {
  color: var(--accent);
  border-color: var(--accent);
}
.src-text {
  margin: 0;
  max-height: 420px;
  overflow: auto;
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 10px;
  font-size: 12px;
  color: var(--ink);
  white-space: pre-wrap;
  word-break: break-word;
}
.area-src {
  min-height: 320px;
  resize: vertical;
  tab-size: 2;
}
.radio {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--ink);
  margin-bottom: 4px;
}
.radio--off {
  color: var(--ink-3);
}
.plain-btn--danger {
  color: var(--fail);
  border-color: var(--fail);
}
.plain-btn--danger:hover {
  background: var(--fail);
  color: var(--bg);
  border-color: var(--fail);
}
.notice {
  margin: 0;
  font-size: 12px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  word-break: break-word;
}
.notice--ok {
  color: var(--done);
  border-color: var(--done);
}
.notice--warn {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--bg);
}
.notice-cmd {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  border: 1px dashed var(--line);
  border-radius: var(--r-chip);
  padding: 6px 8px;
}
.notice-cmd code {
  font-size: 11px;
  color: var(--ink);
  word-break: break-all;
  flex: 1 1 200px;
}
.req {
  color: var(--fail);
}
.vdesc {
  color: var(--ink-3);
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
  border-radius: var(--r-chip);
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
  color: var(--accent);
}
.pv-name {
  color: var(--ink);
  font-weight: 600;
}
.pv-who,
.pv-runner {
  color: var(--ink-3);
}
.pv-badge {
  font-size: 10px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 0 5px;
  color: var(--accent);
}
.pv-sum {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--ink-3);
  word-break: break-word;
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

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.template-note {
  color: var(--ink-3);
  font-size: 11px;
  letter-spacing: 0.08em;
}
.plain-btn {
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 5px 10px;
  font-size: 12px;
}
.plain-btn:hover {
  border-color: var(--accent);
  color: var(--accent);
}

.field {
  display: flex;
  flex-direction: column;
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
  padding: 10px;
  font-size: 13px;
  line-height: 1.45;
  outline: none;
}
.control:focus {
  border-color: var(--accent);
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

@media (max-width: 760px) {
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
}</style>
