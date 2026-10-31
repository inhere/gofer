<script setup lang="ts">
// 工作流列表（/workflows）：列出内置 / 全局 / 项目三类工作流模板（GET /v1/workflow-templates），
// 每项显示来源徽标、说明、变量、最近一次执行与累计次数。操作：运行（进模板向导）、
// 查看 YAML（行内展开，只读；编辑 / 删除在详情页）、执行记录（/workflows/runs?template=）。
// 单次执行的列表在 /workflows/runs，详情仍是 /workflows/:id。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import { getWorkflowTemplateSource, listWorkflowTemplates } from '../api/client'
import { fmtDateTime } from '../api/time'
import { t } from '../i18n'
import StatusPill from '../ui/StatusPill.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import { useListReturn } from '../ui/useListReturn'
import { useProjectFilter } from '../ui/projectFilter'
import { createLiveTopic } from '../utils/useLiveTopic'
import { sourceLabel } from '../utils/workflowTemplate'
import { groupTemplates, runCount, runLink, runsLink, templateLink, varNames } from '../utils/workflowList'
import { useWhoami } from '../store/whoami'
import type { WorkflowTemplateInfo, WorkflowTemplateSource } from '../api/types'

// 调用方上限只读时，运行入口不摆出来（服务端也会 403）。
const { readOnly } = useWhoami()

// 项目是筛选条件：值在 URL 的 ?project=，项目目录里的模板只有选了项目才列得出。
const { project, setProject, options: projectOptions, loadOptions: loadProjectOptions, loadError: projectLoadError } =
  useProjectFilter('/workflows')

// 模板详情页的「← 工作流」回到这里（带项目筛选与滚动位置）。
useListReturn('workflow-templates', '/workflows')

const templates = ref<WorkflowTemplateInfo[]>([])
const loading = ref(false)
const error = ref('')
const groups = computed(() => groupTemplates(templates.value))

async function fetchTemplates(): Promise<void> {
  loading.value = true
  try {
    const resp = await listWorkflowTemplates(project.value || undefined)
    templates.value = resp.templates ?? []
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(project, () => {
  opened.value = ''
  void fetchTemplates()
})

// ---- 行内查看 YAML（只读）----
// 一次只展开一个；展开时才拉 /source（带路径与盖住的层）。
const opened = ref('')
const source = ref<WorkflowTemplateSource | null>(null)
const sourceLoading = ref(false)
const sourceError = ref('')

async function toggleYaml(tpl: WorkflowTemplateInfo): Promise<void> {
  if (opened.value === tpl.name) {
    opened.value = ''
    return
  }
  opened.value = tpl.name
  source.value = null
  sourceError.value = ''
  sourceLoading.value = true
  try {
    const got = await getWorkflowTemplateSource(tpl.name, project.value || undefined)
    // 展开期间用户切到了别的行：丢掉迟到的响应
    if (opened.value === tpl.name) source.value = got
  } catch (e) {
    if (opened.value === tpl.name) sourceError.value = e instanceof Error ? e.message : String(e)
  } finally {
    sourceLoading.value = false
  }
}

function projectOf(tpl: WorkflowTemplateInfo): string {
  return tpl.source === 'project' ? project.value : ''
}

// 工作流的 WS 主题：有新执行 / 状态变化时刷新「最近一次」与次数。
const liveWorkflows = createLiveTopic('workflows', { initial: false, fetch: fetchTemplates })

onMounted(() => {
  void fetchTemplates()
  void loadProjectOptions()
  liveWorkflows.start()
})

onUnmounted(() => {
  liveWorkflows.stop()
})
</script>

<template>
  <div class="page" data-test="workflows-page">
    <header class="page-head">
      <div class="head-main">
        <h1 class="title">{{ t('workflows.title') }}<ExperimentalBadge /></h1>
        <p class="sub">
          {{ templates.length ? t('workflows.list.subtitle', { n: templates.length }) : t('workflows.list.subtitleEmpty') }}
        </p>
      </div>
      <div class="head-actions">
        <span class="poll-dot" :class="{ 'poll-dot--on': loading }" aria-hidden="true">●</span>
        <RouterLink to="/workflows/runs" class="btn" data-test="all-runs">{{ t('workflows.list.allRuns') }}</RouterLink>
        <RouterLink v-if="!readOnly" to="/workflows/new" class="btn btn--primary" data-test="new-workflow">
          {{ t('workflows.new') }}
        </RouterLink>
      </div>
    </header>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="list-bar">
      <ProjectFilter
        :model-value="project"
        :options="projectOptions"
        :loading="false"
        :error="projectLoadError"
        @update:model-value="setProject"
      />
      <span v-if="!project" class="hint" data-test="project-hint">{{ t('workflows.list.projectHint') }}</span>
    </div>

    <section v-for="g in groups" :key="g.layer" class="group" :data-test="`group-${g.layer}`">
      <h2 class="group-title">{{ sourceLabel(g.layer) }}<span class="group-n">{{ g.items.length }}</span></h2>

      <article v-for="tpl in g.items" :key="tpl.name" class="card" :data-test="`tpl-${tpl.name}`">
        <div class="card-head">
          <RouterLink class="card-name mono" :to="templateLink(tpl.name, projectOf(tpl))" data-test="tpl-open">
            {{ tpl.name }}
          </RouterLink>
          <span class="badge" :class="`badge--${g.layer}`" data-test="source-badge">{{ sourceLabel(tpl.source) }}</span>
        </div>
        <p v-if="tpl.desc" class="card-desc">{{ tpl.desc }}</p>

        <div v-if="varNames(tpl).length" class="vars" data-test="tpl-vars">
          <span class="vars-label">{{ t('workflows.list.vars') }}</span>
          <span v-for="v in varNames(tpl)" :key="v.name" class="var mono">{{ v.name }}<b v-if="v.required">*</b></span>
        </div>

        <div class="runinfo" data-test="tpl-lastrun">
          <template v-if="tpl.last_run">
            <span class="runinfo-label">{{ t('workflows.list.lastRun') }}</span>
            <RouterLink :to="`/workflows/${encodeURIComponent(tpl.last_run.id)}`" class="runinfo-link">
              <StatusPill :status="tpl.last_run.status" />
            </RouterLink>
            <span class="dim">{{ fmtDateTime(tpl.last_run.started_at) }}</span>
          </template>
          <span v-else class="dim" data-test="tpl-never">{{ t('workflows.list.neverRun') }}</span>
          <span class="dim runinfo-count" data-test="tpl-runs">{{ t('workflows.list.runCount', { n: runCount(tpl) }) }}</span>
        </div>

        <div class="actions">
          <RouterLink v-if="!readOnly" class="btn btn--primary" :to="runLink(tpl.name, projectOf(tpl))" data-test="tpl-run">
            {{ t('workflows.list.run') }}
          </RouterLink>
          <button class="btn" type="button" :aria-expanded="opened === tpl.name" data-test="tpl-yaml" @click="toggleYaml(tpl)">
            {{ opened === tpl.name ? t('workflows.list.hideYaml') : t('workflows.list.viewYaml') }}
          </button>
          <RouterLink class="btn" :to="runsLink(tpl.name)" data-test="tpl-runs-link">{{ t('workflows.list.runs') }}</RouterLink>
          <RouterLink class="btn" :to="templateLink(tpl.name, projectOf(tpl))" data-test="tpl-detail">
            {{ t('workflows.list.detail') }}
          </RouterLink>
        </div>

        <div v-if="opened === tpl.name" class="yaml" data-test="tpl-yaml-panel">
          <p v-if="sourceLoading" class="dim">{{ t('workflows.new.readingSource') }}</p>
          <p v-else-if="sourceError" class="error" data-test="tpl-yaml-error">{{ sourceError }}</p>
          <template v-else-if="source">
            <p class="yaml-path mono" data-test="tpl-path">{{ source.path || t('workflows.list.builtinPath') }}</p>
            <pre class="yaml-body mono" data-test="tpl-yaml-body">{{ source.content }}</pre>
          </template>
        </div>
      </article>
    </section>

    <div v-if="groups.length === 0 && !error && !loading" class="empty" data-test="workflows-empty">
      <p>{{ t('workflows.list.empty') }}</p>
      <p class="empty-hint">{{ t('workflows.emptyHint') }}</p>
      <RouterLink v-if="!readOnly" to="/workflows/new" class="btn btn--primary">{{ t('workflows.new') }}</RouterLink>
    </div>
  </div>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--s3);
  margin-bottom: var(--s3);
}
.title {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--ink);
}
.sub {
  margin: 2px 0 0;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.head-actions {
  display: flex;
  align-items: center;
  gap: var(--s2);
}
.poll-dot {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-dot--on {
  color: var(--accent);
}
.btn {
  display: inline-flex;
  align-items: center;
  min-height: 32px;
  padding: 0 var(--s3);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
  white-space: nowrap;
}
.btn:hover {
  text-decoration: none;
  border-color: var(--accent);
}
.btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn--primary:hover {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.error {
  color: var(--fail);
  font-size: var(--fs-sm);
  border: 1px solid var(--fail);
  border-radius: var(--r-ctl);
  padding: var(--s2);
  margin: 0 0 var(--s3);
  word-break: break-word;
}
.list-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s3);
  margin-bottom: var(--s3);
}
.hint,
.dim {
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.group {
  margin-bottom: var(--s4);
}
.group-title {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin: 0 0 var(--s2);
  font-size: var(--fs-sm);
  letter-spacing: 0.06em;
  color: var(--ink-3);
  text-transform: uppercase;
}
.group-n {
  font-size: var(--fs-xs);
}
.card {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  background: var(--surface);
  padding: var(--s3);
  margin-bottom: var(--s2);
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  min-width: 0;
}
.card-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-width: 0;
}
.card-name {
  color: var(--accent);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.badge {
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  padding: 0 6px;
  font-size: var(--fs-xs);
  color: var(--ink-3);
  white-space: nowrap;
}
.badge--project {
  border-color: var(--accent);
  color: var(--accent);
}
.card-desc {
  margin: 0;
  color: var(--ink);
  font-size: var(--fs-sm);
}
.vars {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1) var(--s2);
  font-size: var(--fs-xs);
}
.vars-label {
  color: var(--ink-3);
}
.var {
  background: var(--surface-2);
  border-radius: var(--r-ctl);
  padding: 0 6px;
}
.var b {
  color: var(--fail);
  margin-left: 2px;
}
.runinfo {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s2);
  font-size: var(--fs-sm);
}
.runinfo-label {
  color: var(--ink-3);
}
.runinfo-count {
  margin-left: auto;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s2);
}
.yaml {
  border-top: 1px solid var(--line);
  padding-top: var(--s2);
  min-width: 0;
}
.yaml-path {
  margin: 0 0 var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  word-break: break-all;
}
.yaml-body {
  margin: 0;
  max-height: 360px;
  overflow: auto;
  background: var(--surface-2);
  border-radius: var(--r-ctl);
  padding: var(--s2);
  font-size: var(--fs-xs);
  white-space: pre;
}
.empty {
  padding: var(--s6) var(--s3);
  text-align: center;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.empty p {
  margin: 0 0 var(--s2);
}
@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
    gap: var(--s2);
  }
  .btn {
    min-height: 40px;
  }
  .runinfo-count {
    margin-left: 0;
  }
}
</style>
