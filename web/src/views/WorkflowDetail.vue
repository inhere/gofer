<script setup lang="ts">
// Workflow 详情：getWorkflow 填头部 + 步骤链 + 事件时间线；running 时轮询刷新（仿 Board 2.5s）。
//  - 步骤按 step_index 分组：fan-out 步改为对比视图（CompareColumns：每路一列，join=pick 时可「选这个并合并」），
//    普通步 / 重试 attempt 历史仍按行并列。
//  - subworkflow.started 事件携带 child_workflow_id，渲染为可链入子 wf 详情的入口。
//  - workflow_events 时间线（P1）展示 fan-out/retry/子 wf/终态等里程碑。
//  - 每个 step 链到对应 job 详情 /jobs/{job_id}（未起的 step 无链接）。
//  - running 显示 cancel 按钮（cancelWorkflow），终态停轮询。
// UI 重设计 P5：ui/BackLink 回列表、ui/StatusPill、结果表按 §7.2 的列、另存为模板进 ui/Drawer。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { createLiveTopic } from '../utils/useLiveTopic'
import CompareColumns from '../components/CompareColumns.vue'
import { cancelWorkflow, getWorkflow, getWorkflowEvents, rerunWorkflow, saveWorkflowAsTemplate } from '../api/client'
import { getMetaCached } from '../api/metaCache'
import { fmtDuration } from '../api/time'
import {
  canRerunFailed,
  failedCount,
  fromStepValue,
  hasExcerpt,
  resultRows,
  templateSaveNotice,
  type ResultRow,
  type SaveNotice,
} from '../utils/workflowResult'
import { t } from '../i18n'
import BackLink from '../ui/BackLink.vue'
import StatusPill from '../ui/StatusPill.vue'
import Drawer from '../ui/Drawer.vue'
import type { Workflow, WorkflowEvent, WorkflowStep } from '../api/types'

const props = defineProps<{ id: string }>()
const router = useRouter()

const workflow = ref<Workflow | null>(null)
const events = ref<WorkflowEvent[]>([])
const error = ref('')
const cancelling = ref(false)

// ---- 结果表（design §一·6）----
const rows = computed<ResultRow[]>(() => resultRows(workflow.value))
const failures = computed(() => failedCount(rows.value))
const expanded = ref<Record<string, boolean>>({})
const rerunning = ref(false)
const rerunError = ref('')

function toggleExcerpt(key: string): void {
  expanded.value = { ...expanded.value, [key]: !expanded.value[key] }
}

// 续跑（design §一·7）：基于原 spec 裁剪出新 workflow，原记录不动，完成后跳到新的。
async function onRerun(mode: 'failed' | 'from', step?: string): Promise<void> {
  if (rerunning.value) return
  const msg =
    mode === 'failed'
      ? t('workflows.detail.rerunFailedConfirm')
      : t('workflows.detail.rerunFromConfirm', { step: step ?? '' })
  if (!window.confirm(msg)) return
  rerunning.value = true
  rerunError.value = ''
  try {
    const nw = await rerunWorkflow(props.id, mode === 'failed' ? { mode } : { mode, step })
    void router.push(`/workflows/${encodeURIComponent(nw.id)}`)
  } catch (e) {
    rerunError.value = e instanceof Error ? e.message : String(e)
  } finally {
    rerunning.value = false
  }
}

// ---- 另存为模板（D7）----
const tplOpen = ref(false)
const tplName = ref('')
const tplScope = ref<'global' | 'project'>('project')
const tplProject = ref('')
const projects = ref<string[]>([])
const tplSaving = ref(false)
const tplError = ref('')
const tplNotice = ref<SaveNotice | null>(null)

async function openSaveAsTemplate(): Promise<void> {
  tplOpen.value = true
  tplNotice.value = null
  tplError.value = ''
  if (!tplName.value) {
    tplName.value = (workflow.value?.title || workflow.value?.id || 'workflow')
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 40)
  }
  if (projects.value.length === 0) {
    try {
      const m = await getMetaCached()
      projects.value = (m.projects ?? []).filter((p) => !p.worker_only).map((p) => p.key)
      tplProject.value = projects.value[0] ?? ''
    } catch {
      projects.value = []
    }
  }
}

async function onSaveAsTemplate(): Promise<void> {
  if (!tplName.value.trim() || tplSaving.value) return
  if (tplScope.value === 'project' && !tplProject.value) {
    tplError.value = t('workflows.detail.needProject')
    return
  }
  tplSaving.value = true
  tplError.value = ''
  tplNotice.value = null
  try {
    const r = await saveWorkflowAsTemplate(props.id, {
      name: tplName.value.trim(),
      scope: tplScope.value,
      project: tplScope.value === 'project' ? tplProject.value : undefined,
    })
    tplNotice.value = templateSaveNotice(r)
  } catch (e) {
    tplError.value = e instanceof Error ? e.message : String(e)
  } finally {
    tplSaving.value = false
  }
}

const isRunning = computed(() => workflow.value?.status === 'running')

const duration = computed(() => {
  const wf = workflow.value
  if (!wf) {
    return ''
  }
  const end = wf.status === 'running' ? Math.floor(Date.now() / 1000) : wf.updated_at
  return fmtDuration(Math.max(0, end - wf.created_at))
})

// 一个 step_index 的一组行（fan-out 并行 + 重试 attempt）。grouped 视图据此横向/纵向展示。
interface StepGroup {
  stepIndex: number
  name: string
  rows: WorkflowStep[]
  fanned: boolean
  retried: boolean
}

// stepGroups 把扁平 steps 按 step_index 聚合，识别 fan-out（同 step 多 fan_index）与
// 重试（同 step 多 attempt）。空 attempt/fan_index 归一为 v1 单 job（不显示徽标）。
const stepGroups = computed<StepGroup[]>(() => {
  const wf = workflow.value
  if (!wf?.steps) {
    return []
  }
  const byStep = new Map<number, WorkflowStep[]>()
  for (const st of wf.steps) {
    const arr = byStep.get(st.step_index) ?? []
    arr.push(st)
    byStep.set(st.step_index, arr)
  }
  const out: StepGroup[] = []
  for (const [stepIndex, rows] of [...byStep.entries()].sort((a, b) => a[0] - b[0])) {
    const fans = new Set(rows.map((r) => r.fan_index ?? 0))
    const attempts = new Set(rows.map((r) => r.attempt ?? 1))
    out.push({
      stepIndex,
      name: rows.find((r) => r.name)?.name || '(unnamed)',
      rows: rows.slice().sort((a, b) => (a.attempt ?? 1) - (b.attempt ?? 1) || (a.fan_index ?? 0) - (b.fan_index ?? 0)),
      fanned: fans.size > 1 || [...fans].some((f) => f >= 1),
      retried: attempts.size > 1 || [...attempts].some((a) => a >= 2),
    })
  }
  return out
})

// 从 subworkflow.started 事件解出 step -> child_workflow_id 映射，供 step 组渲染子 wf 链接。
const childByStep = computed<Map<number, string>>(() => {
  const m = new Map<number, string>()
  // 主源：step 链里 workflow 型步直接带 child_workflow_id（P3 UI 修复，事件被 prune 也在）。
  for (const st of workflow.value?.steps ?? []) {
    if (st.child_workflow_id) {
      m.set(st.step_index, st.child_workflow_id)
    }
  }
  // 兜底：subworkflow.started 事件（detail 携带 step + child_workflow_id）。
  for (const ev of events.value) {
    if (ev.type !== 'subworkflow.started' || !ev.detail) {
      continue
    }
    try {
      const d = JSON.parse(ev.detail) as { step?: number; child_workflow_id?: string }
      if (typeof d.step === 'number' && d.child_workflow_id && !m.has(d.step)) {
        m.set(d.step, d.child_workflow_id)
      }
    } catch {
      // detail 非 JSON：跳过（事件时间线仍原样展示）
    }
  }
  return m
})

async function fetchWorkflow(): Promise<void> {
  try {
    const [wf, evResp] = await Promise.all([
      getWorkflow(props.id),
      getWorkflowEvents(props.id).catch(() => ({ events: events.value })),
    ])
    workflow.value = wf
    events.value = evResp.events ?? []
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function onCancel(): Promise<void> {
  if (!workflow.value || cancelling.value) {
    return
  }
  cancelling.value = true
  try {
    workflow.value = await cancelWorkflow(props.id)
    error.value = ''
    // 取消后立即重拉一次拿最新步骤链
    await fetchWorkflow()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    cancelling.value = false
  }
}

// 对比视图里选定 / 合并后：立即重拉，工作流可能已推进到下一步（重新进入 running 则恢复轮询）。
async function onCompareChanged(): Promise<void> {
  await fetchWorkflow()
}

function openJob(jobId: string): void {
  void router.push(`/jobs/${encodeURIComponent(jobId)}`)
}

// 链入子工作流详情（subworkflow.started 事件给出的 child_workflow_id）。
function openWorkflow(wfId: string): void {
  void router.push(`/workflows/${encodeURIComponent(wfId)}`)
}

// 事件行的徽标文案：attempt>=2 显示 a{n}，fan_index>=1 显示 f{n}，否则空。
function rowTag(st: WorkflowStep): string {
  const parts: string[] = []
  if ((st.attempt ?? 1) >= 2) {
    parts.push(`a${st.attempt}`)
  }
  if ((st.fan_index ?? 0) >= 1) {
    parts.push(`f${st.fan_index}`)
  }
  return parts.join(' ')
}

// 事件时间戳 -> 本地时间字符串（Unix 秒）。
function eventTime(at: number): string {
  return new Date(at * 1000).toLocaleTimeString()
}

// 步骤进度：done 之后后端的 current_step 会越过 total_steps，显示层夹到 total。
function stepProgressText(wf: Workflow): string {
  const cur = wf.total_steps > 0 && wf.current_step > wf.total_steps ? wf.total_steps : wf.current_step
  return `${cur}/${wf.total_steps}`
}

// 路由 param 从一个 workflow 跳到另一个（如点"子工作流 →"）时，/workflows/:id
// 复用同一组件实例、onMounted 不再触发，必须 watch props.id 重新拉取，否则停留在旧
// workflow。重置数据避免旧详情闪现，再按新 workflow 是否 running 决定轮询。
// Q3：`workflows` 主题的失效通知触发重拉；WS 断开超过 15s 才由 30s 兜底轮询接手（恢复后自动停）。
const liveWorkflow = createLiveTopic('workflows', { initial: false, fetch: fetchWorkflow })

watch(
  () => props.id,
  () => {
    workflow.value = null
    events.value = []
    expanded.value = {}
    rerunError.value = ''
    tplOpen.value = false
    tplNotice.value = null
    void fetchWorkflow()
  },
)

onMounted(() => {
  void fetchWorkflow()
  liveWorkflow.start()
})

onUnmounted(() => {
  liveWorkflow.stop()
})
</script>

<template>
  <div class="page" data-test="workflow-detail">
    <div class="detail-head">
      <BackLink list="workflows" to="/workflows" :label="t('workflows.detail.back')" />
      <div v-if="workflow" class="head-status">
        <StatusPill :status="workflow.status" size="md" />
        <button
          v-if="isRunning"
          class="btn btn--danger"
          type="button"
          :disabled="cancelling"
          data-test="cancel-workflow"
          @click="onCancel"
        >
          {{ cancelling ? t('workflows.detail.cancelling') : t('workflows.detail.cancel') }}
        </button>
      </div>
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <div v-if="workflow" class="head-card">
      <h1 class="wf-title">{{ workflow.title || workflow.id }}</h1>
      <dl class="meta">
        <div class="meta-row">
          <dt>id</dt>
          <dd class="mono">{{ workflow.id }}</dd>
        </div>
        <div class="meta-row">
          <dt>{{ t('workflows.detail.step') }}</dt>
          <dd class="mono">{{ stepProgressText(workflow) }}</dd>
        </div>
        <div class="meta-row">
          <dt>{{ t('workflows.detail.duration') }}</dt>
          <dd class="mono">{{ duration }}</dd>
        </div>
        <div v-if="workflow.error" class="meta-row meta-row--err">
          <dt>{{ t('workflows.detail.error') }}</dt>
          <dd>{{ workflow.error }}</dd>
        </div>
      </dl>
    </div>

    <section v-if="workflow" class="results" data-test="result-table">
      <div class="section-head">
        <h2 class="section-title">{{ t('workflows.detail.results') }}</h2>
        <span v-if="failures" class="results-fail" data-test="result-fail-count">
          {{ t('workflows.detail.failedRoutes', { n: failures }) }}
        </span>
        <span class="section-actions">
          <button
            v-if="!isRunning"
            class="btn"
            type="button"
            :disabled="!canRerunFailed(workflow) || rerunning"
            :title="canRerunFailed(workflow) ? t('workflows.detail.rerunFailedTitle') : t('workflows.detail.rerunFailedDisabled')"
            data-test="rerun-failed"
            @click="onRerun('failed')"
          >
            {{ t('workflows.detail.rerunFailed') }}
          </button>
          <button class="btn" type="button" data-test="save-as-template-open" @click="openSaveAsTemplate">
            {{ t('workflows.detail.saveAsTemplate') }}
          </button>
        </span>
      </div>
      <p v-if="rerunError" class="error" data-test="rerun-error">{{ rerunError }}</p>

      <p v-if="rows.length === 0" class="empty-line">{{ t('workflows.detail.noSteps') }}</p>
      <ol v-else class="res-list">
        <li
          v-for="row in rows"
          :key="row.key"
          class="res-row"
          :class="{ 'res-row--fail': row.failed }"
          :data-test="`res-${row.stepIndex}-${row.tag || '0'}`"
        >
          <div class="res-head">
            <span class="res-step" :title="row.stepLabel">{{ row.stepLabel }}</span>
            <span v-if="row.tag" class="res-tag mono">{{ row.tag }}</span>
            <span class="res-badge">
              <StatusPill v-if="row.status" :status="row.status" />
              <span v-else class="step-pending">{{ t('workflows.detail.stepPending') }}</span>
            </span>
          </div>
          <dl class="res-grid">
            <div class="res-cell" data-test="res-runner">
              <dt>{{ t('workflows.detail.runner') }}</dt>
              <dd class="mono">{{ row.runner }}</dd>
            </div>
            <div class="res-cell" data-test="res-started">
              <dt>{{ t('workflows.detail.started') }}</dt>
              <dd class="mono">{{ row.startedAt || '—' }}</dd>
            </div>
            <div class="res-cell">
              <dt>{{ t('workflows.detail.exitCode') }}</dt>
              <dd class="mono">{{ row.exitCode || '—' }}</dd>
            </div>
            <div class="res-cell">
              <dt>{{ t('workflows.detail.duration') }}</dt>
              <dd class="mono">{{ row.duration }}</dd>
            </div>
          </dl>
          <!-- 工作目录：ref: 步跑在临时检出目录里，光看路径说不出是哪棵树，所以并排显示 ref。 -->
          <p v-if="row.cwd" class="res-cwd mono" data-test="res-cwd">
            <em>{{ t('workflows.detail.cwd') }}</em>{{ row.cwd
            }}<span v-if="row.ref" class="res-cwd-ref"> (ref {{ row.ref.slice(0, 12) }})</span>
          </p>
          <div class="res-acts">
            <button
              v-if="hasExcerpt(row)"
              class="btn btn--sm"
              type="button"
              data-test="res-excerpt-toggle"
              @click="toggleExcerpt(row.key)"
            >
              {{ expanded[row.key] ? t('workflows.detail.hideExcerpt') : t('workflows.detail.excerpt') }}
            </button>
            <button
              v-if="row.jobId"
              class="btn btn--sm"
              type="button"
              :title="row.jobId"
              data-test="res-log"
              @click="openJob(row.jobId!)"
            >
              {{ t('workflows.detail.openJob') }} →
            </button>
            <button
              v-if="!isRunning"
              class="btn btn--sm"
              type="button"
              :disabled="rerunning"
              :title="t('workflows.detail.rerunFromTitle', { step: row.stepIndex })"
              data-test="rerun-from"
              @click="onRerun('from', fromStepValue(row))"
            >
              {{ t('workflows.detail.rerunFrom') }}
            </button>
          </div>
          <pre v-if="expanded[row.key] && hasExcerpt(row)" class="res-excerpt mono" data-test="res-excerpt">{{
            row.excerpt.join('\n')
          }}</pre>
        </li>
      </ol>
    </section>

    <section v-if="workflow" class="chain">
      <h2 class="section-title">{{ t('workflows.detail.chain') }}</h2>
      <ol class="step-groups">
        <li v-for="group in stepGroups" :key="group.stepIndex" class="step-group">
          <div class="group-head">
            <span class="step-idx mono">{{ group.stepIndex }}</span>
            <span class="step-name">{{ group.name }}</span>
            <span v-if="group.fanned" class="dim-tag dim-tag--fan">{{ t('workflows.detail.fanned') }}</span>
            <span v-if="group.retried" class="dim-tag dim-tag--retry">{{ t('workflows.detail.retried') }}</span>
            <button
              v-if="childByStep.has(group.stepIndex)"
              class="btn btn--sm sub-link"
              type="button"
              :title="childByStep.get(group.stepIndex)"
              @click="openWorkflow(childByStep.get(group.stepIndex)!)"
            >
              {{ t('workflows.detail.childWorkflow') }}
            </button>
          </div>
          <CompareColumns
            v-if="group.fanned"
            :workflow-id="workflow.id"
            :workflow-status="workflow.status"
            :current-step="workflow.current_step"
            :step-index="group.stepIndex"
            :rows="group.rows"
            @open-job="openJob"
            @changed="onCompareChanged"
          />
          <ul v-else class="group-rows">
            <li v-for="row in group.rows" :key="row.job_id || rowTag(row)" class="group-row">
              <span v-if="rowTag(row)" class="row-tag mono">{{ rowTag(row) }}</span>
              <span v-else class="row-tag mono row-tag--none">·</span>
              <span class="row-badge">
                <StatusPill v-if="row.status" :status="row.status" />
                <span v-else class="step-pending">{{ t('workflows.detail.stepPending') }}</span>
              </span>
              <button
                v-if="row.job_id"
                class="btn btn--sm"
                type="button"
                :title="row.job_id"
                @click="openJob(row.job_id)"
              >
                {{ t('workflows.detail.openJob') }} →
              </button>
              <span v-else class="step-nojob">—</span>
            </li>
          </ul>
        </li>
        <li v-if="stepGroups.length === 0" class="empty-line">
          {{ t('workflows.detail.noSteps') }}
        </li>
      </ol>
    </section>

    <section v-if="workflow && events.length" class="timeline">
      <h2 class="section-title">{{ t('workflows.detail.events') }}</h2>
      <ol class="events">
        <li v-for="ev in events" :key="ev.seq" class="event">
          <span class="ev-time mono">{{ eventTime(ev.at) }}</span>
          <span class="ev-type mono">{{ ev.type }}</span>
          <span v-if="ev.detail" class="ev-detail mono" :title="ev.detail">{{ ev.detail }}</span>
        </li>
      </ol>
    </section>

    <p v-else-if="!workflow && !error" class="loading">{{ t('workflows.loading') }}</p>

    <!-- 另存为模板（D7）：模板名 + 保存层级 + 项目，结果带提交命令 -->
    <Drawer v-model="tplOpen" :label="t('workflows.detail.saveAsTemplate')" width="clamp(420px, 32vw, 560px)">
      <div data-test="save-as-template">
        <div class="field">
          <label class="label" for="sat-name">{{ t('workflows.detail.templateName') }}</label>
          <input id="sat-name" v-model="tplName" class="control mono" type="text" data-test="sat-name" />
          <span class="hint">{{ t('workflows.detail.templateNameHint') }}</span>
        </div>
        <div class="field">
          <span class="label">{{ t('workflows.detail.templateScope') }}</span>
          <label class="radio"
            ><input v-model="tplScope" type="radio" value="global" data-test="sat-scope-global" />
            {{ t('workflows.detail.templateGlobal') }}</label
          >
          <label class="radio"
            ><input v-model="tplScope" type="radio" value="project" data-test="sat-scope-project" />
            {{ t('workflows.detail.templateProject') }}</label
          >
          <select v-if="tplScope === 'project'" v-model="tplProject" class="control">
            <option v-for="p in projects" :key="p" :value="p">{{ p }}</option>
          </select>
        </div>
        <p v-if="tplError" class="error" data-test="sat-error">{{ tplError }}</p>
        <p v-if="tplNotice" class="notice" :class="`notice--${tplNotice.tone}`" data-test="sat-notice">
          {{ tplNotice.text }}
        </p>
        <code v-if="tplNotice?.command" class="sat-cmd mono" data-test="sat-commit-cmd">{{ tplNotice.command }}</code>
        <div class="modal-act">
          <button class="btn" type="button" :disabled="tplSaving" data-test="sat-save" @click="onSaveAsTemplate">
            {{ tplSaving ? t('workflows.detail.templateSaving') : t('workflows.detail.templateSave') }}
          </button>
          <button class="btn" type="button" data-test="sat-close" @click="tplOpen = false">
            {{ t('workflows.detail.close') }}
          </button>
        </div>
      </div>
    </Drawer>
  </div>
</template>

<style scoped>
.page {
  max-width: 1180px;
  margin: 0 auto;
}
.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--s3);
  margin-bottom: var(--s3);
}
.head-status {
  display: inline-flex;
  align-items: center;
  gap: var(--s3);
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
.btn:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.btn:disabled {
  opacity: 0.45;
  cursor: default;
}
.btn--sm {
  min-height: 26px;
  padding: 0 var(--s2);
  font-size: var(--fs-xs);
}
.btn--danger {
  color: var(--fail);
  border-color: var(--fail);
}
.btn--danger:hover:not(:disabled) {
  background: var(--fail);
  color: var(--accent-ink);
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
.head-card {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  padding: var(--s4);
  margin-bottom: var(--s4);
  background: var(--surface);
}
.wf-title {
  font-size: var(--fs-lg);
  color: var(--ink);
  margin: 0 0 var(--s3);
  word-break: break-word;
}
.meta {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: var(--fs-sm);
}
.meta-row {
  display: grid;
  grid-template-columns: 80px 1fr;
  gap: var(--s3);
}
.meta-row dt {
  color: var(--ink-3);
  letter-spacing: 0.04em;
}
.meta-row dd {
  margin: 0;
  color: var(--ink);
  word-break: break-all;
}
.meta-row--err dd {
  color: var(--fail);
}
.section-title {
  font-size: var(--fs-xs);
  letter-spacing: 0.08em;
  color: var(--ink-3);
  text-transform: uppercase;
  margin: 0 0 var(--s2);
}
.section-head {
  display: flex;
  align-items: center;
  gap: var(--s3);
  margin-bottom: var(--s2);
  flex-wrap: wrap;
}
.results-fail {
  color: var(--fail);
  font-size: var(--fs-sm);
}
.section-actions {
  margin-left: auto;
  display: inline-flex;
  gap: var(--s1);
  flex-wrap: wrap;
}
.results {
  margin-bottom: var(--s4);
}
.res-list {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  overflow: hidden;
  background: var(--surface);
}
.res-row {
  border-bottom: 1px solid var(--line);
  padding: var(--s2) var(--s3);
}
.res-row:last-child {
  border-bottom: none;
}
.res-row--fail {
  border-left: 2px solid var(--fail);
}
.res-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-width: 0;
}
.res-step {
  color: var(--ink);
  font-weight: 600;
  font-size: var(--fs-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.res-tag {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.res-badge {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
}
/* 结果行的字段格：桌面横排、窄屏换行（§9.6 英文更宽）。 */
.res-grid {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1) var(--s4);
  margin: var(--s1) 0 0;
  font-size: var(--fs-sm);
}
.res-cell {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}
.res-cell dt {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.res-cell dd {
  margin: 0;
  color: var(--ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.res-acts {
  display: inline-flex;
  gap: var(--s1);
  flex-wrap: wrap;
  margin-top: var(--s1);
}
.res-excerpt {
  margin: var(--s2) 0 0;
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: var(--s2);
  font-size: var(--fs-xs);
  color: var(--fail);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 240px;
  overflow: auto;
}
.res-cwd {
  margin: 6px 0 0;
  font-size: var(--fs-xs);
  color: var(--ink-3);
  word-break: break-all;
}
.res-cwd em {
  font-style: normal;
  color: var(--ink-3);
  opacity: 0.7;
  margin-right: 6px;
}
.res-cwd-ref {
  color: var(--accent);
}
.chain {
  margin-bottom: var(--s4);
}
.step-groups {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  overflow: hidden;
  background: var(--surface);
}
.step-group {
  border-bottom: 1px solid var(--line);
  padding: var(--s2) var(--s3);
}
.step-group:last-child {
  border-bottom: none;
}
.group-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin-bottom: 6px;
}
.step-idx {
  color: var(--accent);
  font-size: var(--fs-sm);
}
.step-name {
  color: var(--ink);
  font-size: var(--fs-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dim-tag {
  font-size: var(--fs-xs);
  padding: 1px 6px;
  border-radius: var(--r-chip);
  border: 1px solid var(--line);
  letter-spacing: 0.04em;
}
.dim-tag--fan {
  color: var(--accent);
  border-color: var(--accent);
}
.dim-tag--retry {
  color: var(--ink-3);
}
.sub-link {
  margin-left: auto;
}
.step-pending {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  opacity: 0.8;
}
.step-nojob {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  text-align: center;
  opacity: 0.6;
}
.group-rows {
  list-style: none;
  margin: 0;
  padding: 0 0 0 28px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.group-row {
  display: grid;
  grid-template-columns: 56px 140px 96px;
  align-items: center;
  gap: var(--s3);
}
.row-tag {
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.row-tag--none {
  opacity: 0.4;
}
.row-badge {
  display: inline-flex;
  align-items: center;
}
.empty-line {
  padding: var(--s5) var(--s3);
  text-align: center;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.loading {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  padding: var(--s5) 0;
}
.timeline {
  margin-top: var(--s4);
}
.events {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  overflow: hidden;
  background: var(--surface);
}
.event {
  display: grid;
  grid-template-columns: 96px 180px 1fr;
  align-items: baseline;
  gap: var(--s3);
  padding: 7px var(--s3);
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-sm);
}
.event:last-child {
  border-bottom: none;
}
.ev-time {
  color: var(--ink-3);
}
.ev-type {
  color: var(--accent);
}
.ev-detail {
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  opacity: 0.85;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: var(--s3);
}
.label {
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  color: var(--ink-3);
}
.control {
  width: 100%;
  background: var(--surface-2);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  padding: 8px;
  font: inherit;
  font-size: var(--fs-sm);
}
.hint {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.radio {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: var(--fs-sm);
  color: var(--ink);
}
.notice {
  margin: 0 0 var(--s2);
  font-size: var(--fs-sm);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: var(--s2);
  word-break: break-word;
}
.notice--ok {
  color: var(--done);
  border-color: var(--done);
}
.notice--warn {
  color: var(--need);
  border-color: var(--need);
}
.sat-cmd {
  display: block;
  font-size: var(--fs-xs);
  color: var(--ink);
  word-break: break-all;
}
.modal-act {
  display: flex;
  justify-content: flex-end;
  gap: var(--s2);
  margin-top: var(--s4);
}
@media (max-width: 760px) {
  .btn {
    min-height: 40px;
  }
  .btn--sm {
    min-height: 32px;
  }
  .res-step {
    white-space: normal;
  }
  .event {
    grid-template-columns: 1fr;
    gap: 2px;
  }
  .ev-detail {
    white-space: normal;
  }
  .group-row {
    grid-template-columns: 44px 1fr 96px;
  }
}
</style>
