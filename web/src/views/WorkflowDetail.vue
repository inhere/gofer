<script setup lang="ts">
// Workflow 详情：getWorkflow 填头部 + 步骤链；running 时轮询刷新（仿 Board 2.5s）。
//  - 每个 step 链到对应 job 详情 /jobs/{job_id}（未起的 step 无链接）。
//  - running 显示 cancel 按钮（cancelWorkflow），终态停轮询。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import StatusBadge from '../components/StatusBadge.vue'
import { cancelWorkflow, getWorkflow } from '../api/client'
import { fmtDuration } from '../api/time'
import type { Workflow } from '../api/types'

const props = defineProps<{ id: string }>()
const router = useRouter()

const POLL_MS = 2500

const workflow = ref<Workflow | null>(null)
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

async function fetchWorkflow(): Promise<void> {
  try {
    workflow.value = await getWorkflow(props.id)
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
      <ol class="steps">
        <li v-for="step in workflow.steps ?? []" :key="step.step_index" class="step">
          <span class="step-idx mono">{{ step.step_index }}</span>
          <span class="step-name">{{ step.name || '(unnamed)' }}</span>
          <span class="step-badge">
            <StatusBadge v-if="step.status" :status="step.status" />
            <span v-else class="step-pending mono">pending</span>
          </span>
          <button
            v-if="step.job_id"
            class="step-link mono"
            type="button"
            :title="step.job_id"
            @click="openJob(step.job_id)"
          >
            job &rarr;
          </button>
          <span v-else class="step-nojob mono">—</span>
        </li>
        <li v-if="(workflow.steps ?? []).length === 0" class="step-empty mono">
          尚无已起步骤
        </li>
      </ol>
    </section>

    <p v-else-if="!error" class="loading mono">加载中…</p>
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
</style>
