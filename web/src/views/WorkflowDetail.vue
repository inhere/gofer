<script setup lang="ts">
// Workflow 详情：getWorkflow 填头部 + 步骤链 + 事件时间线；running 时轮询刷新（仿 Board 2.5s）。
//  - 步骤按 step_index 分组：fan-out 同 step 多个并行 job 横向展示、重试 attempt 历史并列。
//  - subworkflow.started 事件携带 child_workflow_id，渲染为可链入子 wf 详情的入口。
//  - workflow_events 时间线（P1）展示 fan-out/retry/子 wf/终态等里程碑。
//  - 每个 step 链到对应 job 详情 /jobs/{job_id}（未起的 step 无链接）。
//  - running 显示 cancel 按钮（cancelWorkflow），终态停轮询。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import StatusBadge from '../components/StatusBadge.vue'
import { cancelWorkflow, getWorkflow, getWorkflowEvents } from '../api/client'
import { fmtDuration } from '../api/time'
import type { Workflow, WorkflowEvent, WorkflowStep } from '../api/types'

const props = defineProps<{ id: string }>()
const router = useRouter()

const POLL_MS = 2500

const workflow = ref<Workflow | null>(null)
const events = ref<WorkflowEvent[]>([])
const error = ref('')
const cancelling = ref(false)

let timer: number | null = null

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
    // 进入终态则停轮询
    if (!isRunning.value) {
      stopPolling()
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  timer = window.setInterval(() => {
    if (isRunning.value) {
      void fetchWorkflow()
    } else {
      stopPolling()
    }
  }, POLL_MS)
}

function stopPolling(): void {
  if (timer != null) {
    window.clearInterval(timer)
    timer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else if (isRunning.value) {
    void fetchWorkflow()
    startPolling()
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
watch(
  () => props.id,
  () => {
    stopPolling()
    workflow.value = null
    events.value = []
    void fetchWorkflow().then(() => {
      if (isRunning.value) {
        startPolling()
      }
    })
  },
)

onMounted(() => {
  void fetchWorkflow().then(() => {
    if (isRunning.value) {
      startPolling()
    }
  })
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
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
          <dd>{{ workflow.current_step }} / {{ workflow.total_steps }}</dd>
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

    <section v-if="workflow" class="chain">
      <h2 class="chain-title mono">STEP CHAIN</h2>
      <ol class="step-groups">
        <li
          v-for="group in stepGroups"
          :key="group.stepIndex"
          class="step-group"
        >
          <div class="group-head">
            <span class="step-idx mono">{{ group.stepIndex }}</span>
            <span class="step-name">{{ group.name }}</span>
            <span v-if="group.fanned" class="dim-tag mono dim-tag--fan">fan-out</span>
            <span v-if="group.retried" class="dim-tag mono dim-tag--retry">retry</span>
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
          <ul class="group-rows">
            <li v-for="row in group.rows" :key="row.job_id || rowTag(row)" class="group-row">
              <span v-if="rowTag(row)" class="row-tag mono">{{ rowTag(row) }}</span>
              <span v-else class="row-tag mono row-tag--none">·</span>
              <span class="row-badge">
                <StatusBadge v-if="row.status" :status="row.status" />
                <span v-else class="step-pending mono">pending</span>
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
      <h2 class="chain-title mono">EVENTS</h2>
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
  max-width: 880px;
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
</style>
