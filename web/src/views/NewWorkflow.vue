<script setup lang="ts">
// 新建 workflow：Web 只支持 inline YAML spec。
// YAML 先在前端解析成 workflow.Spec JSON，再 POST /v1/workflows。
import { load as loadYaml } from 'js-yaml'
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getMeta, submitWorkflow } from '../api/client'
import type { MetaAgent, MetaProject, MetaRunner, WorkflowSpec } from '../api/types'

const router = useRouter()

const loading = ref(true)
const loadError = ref('')
const submitting = ref(false)
const submitError = ref('')
const yamlText = ref('')

const projects = ref<MetaProject[]>([])
const agents = ref<MetaAgent[]>([])
const runners = ref<MetaRunner[]>([])

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
    projects.value = m.projects ?? []
    agents.value = m.agents ?? []
    runners.value = m.runners ?? []
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    yamlText.value = exampleTemplate()
    loading.value = false
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
    <p v-else-if="loading" class="hint mono">加载示例模板中…</p>

    <form v-else class="card" @submit.prevent="onSubmit">
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
