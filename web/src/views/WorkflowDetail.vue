<script setup lang="ts">
// Workflow 详情：getWorkflow 填头部 + 步骤链 + 事件时间线；running 时轮询刷新（仿 Board 2.5s）。
//  - 步骤按 step_index 分组：fan-out 步改为对比视图（CompareColumns：每路一列，join=pick 时可「选这个并合并」），
//    普通步 / 重试 attempt 历史仍按行并列。
//  - subworkflow.started 事件携带 child_workflow_id，渲染为可链入子 wf 详情的入口。
//  - workflow_events 时间线（P1）展示 fan-out/retry/子 wf/终态等里程碑。
//  - 每个 step 链到对应 job 详情 /jobs/{job_id}（未起的 step 无链接）。
//  - running 显示 cancel 按钮（cancelWorkflow），终态停轮询。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { createLiveTopic } from '../utils/useLiveTopic'
import StatusBadge from '../components/StatusBadge.vue'
import CompareColumns from '../components/CompareColumns.vue'
import { cancelWorkflow, getWorkflow, getWorkflowEvents, rerunWorkflow, saveWorkflowAsTemplate } from '../api/client'
import { getMetaCached } from '../api/metaCache'
import { fmtDuration } from '../api/time'
import { workflowStepText } from '../utils/workflowStep'
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
import type { JobStatus, Workflow, WorkflowEvent, WorkflowStep } from '../api/types'

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
      ? '只重跑失败的步骤（扇出步只重跑失败的那几路）？会新建一个 workflow，原记录不动。'
      : `从第 ${step} 步起重跑？会新建一个 workflow，原记录不动。`
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
    tplError.value = '保存到项目需要选一个项目'
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
  <div class="detail">
    <div class="detail-head">
      <button class="back mono" type="button" @click="router.push('/workflows')">
        &larr; workflows
      </button>
      <div v-if="workflow" class="head-status">
        <StatusBadge :status="workflow.status" />
        <button
          v-if="isRunning"
          class="cancel-btn mono"
          type="button"
          :disabled="cancelling"
          @click="onCancel"
        >
          {{ cancelling ? '取消中…' : '取消工作流' }}
        </button>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div v-if="workflow" class="head-card">
      <h1 class="wf-title">{{ workflow.title || workflow.id }}</h1>
      <dl class="meta mono">
        <div class="meta-row">
          <dt>id</dt>
          <dd>{{ workflow.id }}</dd>
        </div>
        <div class="meta-row">
          <dt>step</dt>
          <dd>{{ workflowStepText(workflow.status, workflow.current_step, workflow.total_steps) }}</dd>
        </div>
        <div class="meta-row">
          <dt>耗时</dt>
          <dd>{{ duration }}</dd>
        </div>
        <div v-if="workflow.error" class="meta-row meta-row--err">
          <dt>error</dt>
          <dd>{{ workflow.error }}</dd>
        </div>
      </dl>
    </div>

    <section v-if="workflow" class="results" data-test="result-table">
      <div class="results-head">
        <h2 class="chain-title mono">结果</h2>
        <span v-if="failures" class="results-fail mono" data-test="result-fail-count">{{ failures }} 路失败</span>
        <span class="results-actions">
          <button
            v-if="!isRunning"
            class="mini-btn mono"
            type="button"
            :disabled="!canRerunFailed(workflow) || rerunning"
            :title="canRerunFailed(workflow) ? '只重跑失败的步骤 / 路' : '没有失败的步骤可重跑'"
            data-test="rerun-failed"
            @click="onRerun('failed')"
          >
            只重跑失败的
          </button>
          <button
            class="mini-btn mono"
            type="button"
            data-test="save-as-template-open"
            @click="openSaveAsTemplate"
          >
            另存为模板
          </button>
        </span>
      </div>
      <p v-if="rerunError" class="error mono" data-test="rerun-error">{{ rerunError }}</p>

      <p v-if="rows.length === 0" class="step-empty mono">尚无已起步骤</p>
      <ol v-else class="res-list">
        <li v-for="row in rows" :key="row.key" class="res-row" :class="{ 'res-row--fail': row.failed }" :data-test="`res-${row.stepIndex}-${row.tag || '0'}`">
          <div class="res-main">
            <span class="res-step mono">{{ row.stepLabel }}</span>
            <span v-if="row.tag" class="res-tag mono">{{ row.tag }}</span>
            <span class="res-badge"><StatusBadge v-if="row.status" :status="row.status as JobStatus" /><span v-else class="step-pending mono">待起</span></span>
            <span class="res-cell mono" data-test="res-runner"><em>runner</em>{{ row.runner }}</span>
            <span class="res-cell mono" data-test="res-started"><em>开始</em>{{ row.startedAt || '—' }}</span>
            <span class="res-cell mono"><em>退出码</em>{{ row.exitCode || '—' }}</span>
            <span class="res-cell mono"><em>耗时</em>{{ row.duration }}</span>
            <span class="res-acts">
              <button
                v-if="hasExcerpt(row)"
                class="mini-btn mono"
                type="button"
                data-test="res-excerpt-toggle"
                @click="toggleExcerpt(row.key)"
              >
                {{ expanded[row.key] ? '收起摘录' : '失败摘录' }}
              </button>
              <button
                v-if="row.jobId"
                class="mini-btn mono"
                type="button"
                :title="row.jobId"
                data-test="res-log"
                @click="openJob(row.jobId!)"
              >
                日志 &rarr;
              </button>
              <button
                v-if="!isRunning"
                class="mini-btn mono"
                type="button"
                :disabled="rerunning"
                :title="`从第 ${row.stepIndex} 步起重跑`"
                data-test="rerun-from"
                @click="onRerun('from', fromStepValue(row))"
              >
                从此步重跑
              </button>
            </span>
          </div>
          <pre v-if="expanded[row.key] && hasExcerpt(row)" class="res-excerpt mono" data-test="res-excerpt">{{ row.excerpt.join('\n') }}</pre>
          <!-- 工作目录：ref: 步跑在临时检出目录里，光看路径说不出是哪棵树，所以并排显示 ref。 -->
          <p v-if="row.cwd" class="res-cwd mono" data-test="res-cwd">
            <em>cwd</em>{{ row.cwd }}<span v-if="row.ref" class="res-cwd-ref"> (ref {{ row.ref.slice(0, 12) }})</span>
          </p>
        </li>
      </ol>

      <div v-if="tplOpen" class="save-tpl" data-test="save-as-template">
        <div class="field">
          <label class="label mono" for="sat-name">模板名（字母、数字、. _ -）</label>
          <input id="sat-name" v-model="tplName" class="control mono" type="text" data-test="sat-name" />
        </div>
        <div class="field">
          <span class="label mono">保存到哪一层</span>
          <label class="radio mono"><input v-model="tplScope" type="radio" value="global" data-test="sat-scope-global" /> 全局</label>
          <label class="radio mono"><input v-model="tplScope" type="radio" value="project" data-test="sat-scope-project" /> 项目</label>
          <select v-if="tplScope === 'project'" v-model="tplProject" class="control mono">
            <option v-for="p in projects" :key="p" :value="p">{{ p }}</option>
          </select>
        </div>
        <p v-if="tplError" class="error mono" data-test="sat-error">{{ tplError }}</p>
        <p v-if="tplNotice" class="notice mono" :class="`notice--${tplNotice.tone}`" data-test="sat-notice">
          {{ tplNotice.text }}
        </p>
        <code v-if="tplNotice?.command" class="sat-cmd mono" data-test="sat-commit-cmd">{{ tplNotice.command }}</code>
        <div class="res-acts">
          <button class="mini-btn mono" type="button" :disabled="tplSaving" data-test="sat-save" @click="onSaveAsTemplate">
            {{ tplSaving ? '保存中…' : '保存为模板' }}
          </button>
          <button class="mini-btn mono" type="button" @click="tplOpen = false">关闭</button>
        </div>
      </div>
    </section>

    <section v-if="workflow" class="chain">
      <h2 class="chain-title mono">步骤链</h2>
      <ol class="step-groups">
        <li
          v-for="group in stepGroups"
          :key="group.stepIndex"
          class="step-group"
        >
          <div class="group-head">
            <span class="step-idx mono">{{ group.stepIndex }}</span>
            <span class="step-name">{{ group.name }}</span>
            <span v-if="group.fanned" class="dim-tag mono dim-tag--fan">并行</span>
            <span v-if="group.retried" class="dim-tag mono dim-tag--retry">重试</span>
            <button
              v-if="childByStep.has(group.stepIndex)"
              class="sub-link mono"
              type="button"
              :title="childByStep.get(group.stepIndex)"
              @click="openWorkflow(childByStep.get(group.stepIndex)!)"
            >
              子工作流 &rarr;
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
                <StatusBadge v-if="row.status" :status="row.status" />
                <span v-else class="step-pending mono">待起</span>
              </span>
              <button
                v-if="row.job_id"
                class="step-link mono"
                type="button"
                :title="row.job_id"
                @click="openJob(row.job_id)"
              >
                job &rarr;
              </button>
              <span v-else class="step-nojob mono">—</span>
            </li>
          </ul>
        </li>
        <li v-if="stepGroups.length === 0" class="step-empty mono">
          尚无已起步骤
        </li>
      </ol>
    </section>

    <section v-if="workflow && events.length" class="timeline">
      <h2 class="chain-title mono">事件</h2>
      <ol class="events">
        <li v-for="ev in events" :key="ev.seq" class="event">
          <span class="ev-time mono">{{ eventTime(ev.at) }}</span>
          <span class="ev-type mono">{{ ev.type }}</span>
          <span v-if="ev.detail" class="ev-detail mono" :title="ev.detail">{{ ev.detail }}</span>
        </li>
      </ol>
    </section>

    <p v-else-if="!workflow && !error" class="loading mono">加载中…</p>
  </div>
</template>

<style scoped>
.detail {
  max-width: 1180px;
  margin: 0 auto;
}
.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.back {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.back:hover {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.head-status {
  display: inline-flex;
  align-items: center;
  gap: 12px;
}
.cancel-btn {
  background: transparent;
  color: var(--fail);
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.cancel-btn:hover:not(:disabled) {
  background: var(--fail);
  color: var(--ink);
}
.cancel-btn:disabled {
  opacity: 0.6;
  cursor: default;
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}

.head-card {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 16px;
  margin-bottom: 18px;
  background: var(--panel);
}
.wf-title {
  font-size: 18px;
  color: var(--paper);
  margin: 0 0 12px;
  word-break: break-word;
}
.meta {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 12px;
}
.meta-row {
  display: grid;
  grid-template-columns: 80px 1fr;
  gap: 10px;
}
.meta-row dt {
  color: var(--queue);
  letter-spacing: 0.04em;
}
.meta-row dd {
  margin: 0;
  color: var(--paper);
  word-break: break-all;
}
.meta-row--err dd {
  color: var(--fail);
}

.chain-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--queue);
  text-transform: uppercase;
  margin: 0 0 10px;
}
.steps {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.step {
  display: grid;
  grid-template-columns: 40px minmax(120px, 1fr) 140px 72px;
  align-items: center;
  gap: 12px;
  padding: 11px 14px;
  border-bottom: 1px solid var(--line);
}
.step:last-child {
  border-bottom: none;
}
.step-idx {
  color: var(--phosphor);
  font-size: 13px;
}
.step-name {
  color: var(--paper);
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.step-badge {
  display: inline-flex;
  align-items: center;
}
.step-pending {
  color: var(--queue);
  font-size: 12px;
  opacity: 0.7;
}
.step-link {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
}
.step-link:hover {
  border-color: var(--phosphor);
}
.step-nojob {
  color: var(--queue);
  font-size: 12px;
  text-align: center;
  opacity: 0.6;
}
.step-empty {
  padding: 18px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 12px;
}
.loading {
  color: var(--queue);
  font-size: 13px;
  padding: 18px 0;
}

/* P4/T4.3: grouped step rendering (fan-out / retry / sub-workflow). */
.step-groups {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.step-group {
  border-bottom: 1px solid var(--line);
  padding: 10px 14px;
}
.step-group:last-child {
  border-bottom: none;
}
.group-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}
.dim-tag {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: var(--radius);
  border: 1px solid var(--line);
  letter-spacing: 0.04em;
}
.dim-tag--fan {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.dim-tag--retry {
  color: var(--queue);
}
.sub-link {
  margin-left: auto;
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
}
.sub-link:hover {
  border-color: var(--phosphor);
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
  grid-template-columns: 56px 140px 72px;
  align-items: center;
  gap: 12px;
}
.row-tag {
  color: var(--queue);
  font-size: 12px;
}
.row-tag--none {
  opacity: 0.4;
}
.row-badge {
  display: inline-flex;
  align-items: center;
}

/* P1: workflow_events timeline. */
.timeline {
  margin-top: 18px;
}
.events {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.event {
  display: grid;
  grid-template-columns: 96px 180px 1fr;
  align-items: baseline;
  gap: 10px;
  padding: 7px 14px;
  border-bottom: 1px solid var(--line);
  font-size: 12px;
}
.event:last-child {
  border-bottom: none;
}
.ev-time {
  color: var(--queue);
}
.ev-type {
  color: var(--phosphor);
}
.ev-detail {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  opacity: 0.85;
}

/* 结果表（design §一·6）：每步 / 每路 fan 一行；窄屏改纵向卡片。 */
.results {
  margin-bottom: 18px;
}
.results-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
  flex-wrap: wrap;
}
.results-fail {
  color: var(--fail);
  font-size: 12px;
}
.results-actions {
  margin-left: auto;
  display: inline-flex;
  gap: 6px;
  flex-wrap: wrap;
}
.res-list {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.res-row {
  border-bottom: 1px solid var(--line);
  padding: 8px 12px;
}
.res-row:last-child {
  border-bottom: none;
}
.res-row--fail {
  border-left: 2px solid var(--fail);
}
.res-main {
  display: grid;
  grid-template-columns: minmax(120px, 1.6fr) 48px 96px 96px 72px 68px minmax(0, auto);
  align-items: center;
  gap: 10px;
  font-size: 12px;
}
.res-step {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.res-tag {
  color: var(--queue);
  font-size: 11px;
}
.res-cell {
  color: var(--paper);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.res-cell em {
  display: none;
  font-style: normal;
  color: var(--queue);
  margin-right: 6px;
}
.res-acts {
  display: inline-flex;
  gap: 6px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.mini-btn {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.mini-btn:disabled {
  opacity: 0.45;
  cursor: default;
}
.res-excerpt {
  margin: 8px 0 0;
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px;
  font-size: 11px;
  color: var(--fail);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 240px;
  overflow: auto;
}
.res-cwd {
  margin: 6px 0 0;
  font-size: 11px;
  color: var(--queue);
  word-break: break-all;
}
.res-cwd em {
  font-style: normal;
  color: var(--line);
  margin-right: 6px;
}
.res-cwd-ref {
  color: var(--phosphor);
}
.save-tpl {
  margin-top: 12px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.save-tpl .field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.save-tpl .label {
  font-size: 11px;
  letter-spacing: 0.08em;
  color: var(--queue);
}
.save-tpl .control {
  width: 100%;
  background: var(--ink);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px;
  font-size: 13px;
}
.radio {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--paper);
}
.notice {
  margin: 0;
  font-size: 12px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  word-break: break-word;
}
.notice--ok {
  color: var(--done);
  border-color: var(--done);
}
.notice--warn {
  color: var(--phosphor);
  border-color: var(--phosphor);
  background: var(--ink);
}
.sat-cmd {
  font-size: 11px;
  color: var(--paper);
  word-break: break-all;
}

@media (max-width: 720px) {
  .res-main {
    grid-template-columns: minmax(0, 1fr) auto;
    row-gap: 4px;
  }
  .res-step {
    grid-column: 1 / -1;
    white-space: normal;
  }
  .res-cell em {
    display: inline;
  }
  .res-acts {
    grid-column: 1 / -1;
    justify-content: flex-start;
  }
  .results-actions {
    margin-left: 0;
  }
}
</style>
