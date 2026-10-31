<script setup lang="ts">
// 工作流（模板）详情（/workflows/templates/:name）：来源与文件路径、变量、YAML（查看 / 编辑 / 删除）、
// 内嵌该工作流的执行记录。编辑 / 删除只对全局 / 项目文件开放，且要 config.write（服务端也拒）；
// 内置模板只读（同名保存是「覆盖」，在向导里做）。删除要二次确认，删除后按接口的
// falls_back_to 说明回落到哪一层。?project=<p> 指明项目模板所在的项目，?tab=runs 切到执行记录。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import {
  deleteWorkflowTemplate,
  getWorkflowTemplateSource,
  listWorkflowTemplates,
  saveWorkflowTemplate,
} from '../api/client'
import { fmtDateTime } from '../api/time'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import TemplateRuns from '../components/TemplateRuns.vue'
import { t } from '../i18n'
import BackLink from '../ui/BackLink.vue'
import StatusPill from '../ui/StatusPill.vue'
import Tabs from '../ui/Tabs.vue'
import Toast from '../ui/Toast.vue'
import { toastDuration } from '../ui/toast'
import { useWhoami } from '../store/whoami'
import {
  shadowsNote,
  templateDeleteNotice,
  templateEditGate,
  templateReadOnlyNote,
  templateSaveNotice,
  type SaveNotice,
} from '../utils/workflowResult'
import { sourceLabel } from '../utils/workflowTemplate'
import { isEditableLayer, queryString, runCount, runLink, runsLink, varNames } from '../utils/workflowList'
import type { WorkflowTemplateInfo, WorkflowTemplateSource } from '../api/types'

const route = useRoute()
const { readOnly, whoami, can } = useWhoami()

const name = computed(() => queryString(route.params.name))
const project = computed(() => queryString(route.query.project))

const tpl = ref<WorkflowTemplateInfo | null>(null)
const src = ref<WorkflowTemplateSource | null>(null)
const loading = ref(true)
const error = ref('')
// 名字已没有任何一层（删到头了）：页面只剩提示与返回列表。
const gone = ref(false)

const gate = computed(() => templateEditGate(can('config.write'), whoami.value != null))
const editable = computed(() => gate.value.editable && !readOnly.value && isEditableLayer(src.value?.source))
const readOnlyNote = computed(() => templateReadOnlyNote(src.value?.source))
const shadows = computed(() => shadowsNote(src.value?.shadows))
const tabs = computed(() => [
  { value: 'yaml', label: t('workflows.tpl.tabYaml') },
  { value: 'runs', label: t('workflows.tpl.tabRuns'), count: tpl.value ? runCount(tpl.value) : undefined },
])

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  gone.value = false
  try {
    const [list, source] = await Promise.all([
      listWorkflowTemplates(project.value || undefined),
      getWorkflowTemplateSource(name.value, project.value || undefined),
    ])
    tpl.value = (list.templates ?? []).find((x) => x.name === name.value) ?? null
    src.value = source
  } catch (e) {
    tpl.value = null
    src.value = null
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(() => [name.value, project.value], () => void load())
onMounted(() => void load())

// ---- 编辑 ----
const editing = ref(false)
const editText = ref('')
const saving = ref(false)
const saveError = ref('')
const notice = ref<SaveNotice | null>(null)

function startEdit(): void {
  if (!editable.value) return
  editText.value = src.value?.content ?? ''
  saveError.value = ''
  notice.value = null
  editing.value = true
}

function cancelEdit(): void {
  editing.value = false
  saveError.value = ''
}

async function onSave(): Promise<void> {
  const layer = src.value?.source
  if (saving.value || !editable.value || !isEditableLayer(layer)) return
  saving.value = true
  saveError.value = ''
  try {
    const r = await saveWorkflowTemplate(name.value, layer, editText.value, project.value || undefined)
    editing.value = false
    await load()
    notice.value = templateSaveNotice(r)
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

// ---- 删除（二次确认）----
const confirmingDelete = ref(false)
const removing = ref(false)
const toast = ref<{ title: string; text: string } | null>(null)

async function onDelete(): Promise<void> {
  const layer = src.value?.source
  if (removing.value || !editable.value || !isEditableLayer(layer)) return
  if (!confirmingDelete.value) {
    confirmingDelete.value = true
    return
  }
  removing.value = true
  saveError.value = ''
  try {
    const r = await deleteWorkflowTemplate(name.value, layer, project.value || undefined)
    confirmingDelete.value = false
    editing.value = false
    // 回落到哪一层由接口的 falls_back_to 决定；没有下一层时名字就没了。
    toast.value = { title: name.value, text: templateDeleteNotice(r) }
    if (r.falls_back_to) await load()
    else gone.value = true
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    removing.value = false
  }
}

// 手机上提示自己收掉；宽屏等用户关闭。
const dwell = toastDuration()
</script>

<template>
  <div class="page" data-test="workflow-template">
    <div class="detail-head">
      <BackLink list="workflow-templates" to="/workflows" :label="t('workflows.title')" />
    </div>

    <p v-if="error" class="error" data-test="tpl-load-error">{{ error }}</p>
    <p v-if="loading" class="dim">{{ t('workflows.loading') }}</p>

    <div v-if="gone" class="empty" data-test="tpl-gone">
      <p>{{ t('workflows.tpl.gone', { name }) }}</p>
      <RouterLink to="/workflows" class="btn">{{ t('workflows.title') }}</RouterLink>
    </div>

    <template v-else-if="src">
      <header class="head">
        <h1 class="title mono">{{ name }}<ExperimentalBadge /></h1>
        <span class="badge" data-test="source-badge">{{ sourceLabel(src.source) }}</span>
      </header>
      <p v-if="tpl?.desc" class="desc">{{ tpl.desc }}</p>
      <p class="path mono" data-test="tpl-path">{{ src.path || t('workflows.list.builtinPath') }}</p>
      <p v-if="shadows" class="hint" data-test="tpl-shadows">{{ shadows }}</p>
      <p v-if="readOnlyNote" class="hint" data-test="tpl-readonly">{{ readOnlyNote }}</p>

      <div v-if="tpl && varNames(tpl).length" class="vars" data-test="tpl-vars">
        <span class="dim">{{ t('workflows.list.vars') }}</span>
        <span v-for="v in varNames(tpl)" :key="v.name" class="var mono">{{ v.name }}<b v-if="v.required">*</b></span>
      </div>

      <div v-if="tpl" class="runinfo" data-test="tpl-lastrun">
        <template v-if="tpl.last_run">
          <span class="dim">{{ t('workflows.list.lastRun') }}</span>
          <RouterLink :to="`/workflows/${encodeURIComponent(tpl.last_run.id)}`"><StatusPill :status="tpl.last_run.status" /></RouterLink>
          <span class="dim">{{ fmtDateTime(tpl.last_run.started_at) }}</span>
        </template>
        <span v-else class="dim">{{ t('workflows.list.neverRun') }}</span>
        <span class="dim">{{ t('workflows.list.runCount', { n: runCount(tpl) }) }}</span>
      </div>

      <div class="actions">
        <RouterLink v-if="!readOnly" class="btn btn--primary" :to="runLink(name, src.source === 'project' ? project : '')" data-test="tpl-run">
          {{ t('workflows.list.run') }}
        </RouterLink>
        <RouterLink class="btn" :to="runsLink(name)" data-test="tpl-runs-link">{{ t('workflows.list.runs') }}</RouterLink>
      </div>

      <Tabs :tabs="tabs" :label="name" fallback="yaml">
        <template #default="{ active }">
          <section v-if="active === 'yaml'" class="pane" data-test="pane-yaml">
            <template v-if="editing">
              <textarea v-model="editText" class="editor mono" rows="18" spellcheck="false" data-test="tpl-editor"></textarea>
              <p v-if="saveError" class="error mono" data-test="tpl-save-error">{{ saveError }}</p>
              <div class="actions">
                <button class="btn btn--primary" type="button" :disabled="saving" data-test="tpl-save" @click="onSave">
                  {{ saving ? t('workflows.new.saving') : t('workflows.new.saveTemplate') }}
                </button>
                <button class="btn" type="button" :disabled="saving" data-test="tpl-cancel" @click="cancelEdit">
                  {{ t('workflows.new.cancel') }}
                </button>
              </div>
            </template>
            <template v-else>
              <pre class="yaml mono" data-test="tpl-yaml-body">{{ src.content }}</pre>
              <p v-if="!editable && gate.reason && isEditableLayer(src.source)" class="hint" data-test="tpl-edit-gate">{{ gate.reason }}</p>
              <p v-if="saveError" class="error mono" data-test="tpl-save-error">{{ saveError }}</p>
              <div v-if="editable" class="actions">
                <button class="btn" type="button" data-test="tpl-edit" @click="startEdit">{{ t('workflows.new.edit') }}</button>
                <button class="btn btn--danger" type="button" :disabled="removing" data-test="tpl-delete" @click="onDelete">
                  {{ confirmingDelete ? t('workflows.new.confirmDelete') : t('workflows.new.delete') }}
                </button>
                <button v-if="confirmingDelete" class="btn" type="button" data-test="tpl-delete-cancel" @click="confirmingDelete = false">
                  {{ t('workflows.new.cancelDelete') }}
                </button>
              </div>
            </template>

            <p v-if="notice" class="notice mono" :class="`notice--${notice.tone}`" data-test="tpl-save-notice">{{ notice.text }}</p>
            <code v-if="notice?.command" class="mono cmd" data-test="tpl-commit-cmd">{{ notice.command }}</code>
          </section>

          <TemplateRuns v-else :template="name" />
        </template>
      </Tabs>
    </template>

    <Toast
      v-if="toast"
      placement="corner"
      closable
      :title="toast.title"
      :text="toast.text"
      :duration="dwell"
      data-test="tpl-toast"
      @close="toast = null"
    />
  </div>
</template>

<style scoped>
.detail-head {
  margin-bottom: var(--s3);
}
.head {
  display: flex;
  align-items: center;
  gap: var(--s2);
}
.title {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--ink);
}
.badge {
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  padding: 0 6px;
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.desc {
  margin: var(--s2) 0 0;
  font-size: var(--fs-sm);
}
.path {
  margin: var(--s1) 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  word-break: break-all;
}
.hint,
.dim {
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.vars,
.runinfo,
.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s2);
  margin: var(--s2) 0;
}
.var {
  background: var(--surface-2);
  border-radius: var(--r-ctl);
  padding: 0 6px;
  font-size: var(--fs-xs);
}
.var b {
  color: var(--fail);
  margin-left: 2px;
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
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn--danger {
  color: var(--fail);
  border-color: var(--fail);
}
.pane {
  margin-top: var(--s3);
}
.yaml {
  margin: 0;
  max-height: 480px;
  overflow: auto;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  padding: var(--s3);
  font-size: var(--fs-xs);
  white-space: pre;
}
.editor {
  width: 100%;
  box-sizing: border-box;
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-panel);
  padding: var(--s2);
  font-size: var(--fs-xs);
}
.error {
  color: var(--fail);
  font-size: var(--fs-sm);
  word-break: break-word;
}
.notice {
  font-size: var(--fs-sm);
}
.notice--warn {
  color: var(--need);
}
.cmd {
  display: block;
  word-break: break-all;
  font-size: var(--fs-xs);
}
.empty {
  padding: var(--s6) var(--s3);
  text-align: center;
  color: var(--ink-3);
}
@media (max-width: 760px) {
  .btn {
    min-height: 40px;
  }
}
</style>
