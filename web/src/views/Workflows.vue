<script setup lang="ts">
// Workflows 列表（job 链）：轮询 listWorkflows（2.5s），Page Visibility 暂停/恢复，
// status 过滤，行点击进详情。仿 Board.vue 结构。
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import { useRouter } from 'vue-router'
import StatusBadge from '../components/StatusBadge.vue'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import { listWorkflows } from '../api/client'
import { fmtDuration } from '../api/time'
import { workflowStepText } from '../utils/workflowStep'
import { workflowStatusOptions } from '../utils/statusText'
import { COL, PAGE } from '../utils/uiText'
import { useWhoami } from '../store/whoami'
import type { Workflow, WorkflowStatus } from '../api/types'

const router = useRouter()
// 调用方上限只读时，创建入口一律不摆出来（服务端也会 403）。
const { readOnly } = useWhoami()

const workflows = ref<Workflow[]>([])
const loading = ref(false)
const error = ref('')
const statusFilter = ref<'' | WorkflowStatus>('')

const statusOptions: Array<{ value: '' | WorkflowStatus; label: string }> = workflowStatusOptions()


async function fetchWorkflows(): Promise<void> {
  loading.value = true
  try {
    const resp = await listWorkflows(statusFilter.value || undefined)
    workflows.value = resp.workflows ?? []
    error.value = ''
  } catch (e) {
    // 401 已由 client 处理（跳转登录）；其余显示错误条
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(statusFilter, () => {
  void fetchWorkflows()
})

function shortId(id: string): string {
  return id.length > 10 ? id.slice(-10) : id
}

// 工作流耗时：created_at -> updated_at（运行中按 now）。
function rowDuration(wf: Workflow): string {
  const end = wf.status === 'running' ? Math.floor(Date.now() / 1000) : wf.updated_at
  return fmtDuration(Math.max(0, end - wf.created_at))
}

function openWorkflow(wf: Workflow): void {
  void router.push(`/workflows/${encodeURIComponent(wf.id)}`)
}

// Q3：`workflows` 主题的失效通知触发重拉；WS 断开超过 15s 才由 30s 一次的兜底轮询接手（恢复后自动停）。
const liveWorkflows = createLiveTopic('workflows', { initial: false, fetch: fetchWorkflows })

onMounted(() => {
  void fetchWorkflows()
  liveWorkflows.start()
})

onUnmounted(() => {
  liveWorkflows.stop()
})
</script>

<template>
  <div class="board">
    <div class="board-head">
      <!-- 工作流 / 模板库在 docs/guide/stability.md 里属实验性，标题旁挂标注。 -->
      <h1 class="title mono">{{ PAGE.workflows }}<ExperimentalBadge /></h1>
      <div class="controls mono">
        <RouterLink v-if="!readOnly" to="/workflows/new" class="create-link">
          创建工作流
        </RouterLink>
        <label class="filter">
          <span class="filter-label">{{ COL.status }}</span>
          <select v-model="statusFilter" class="filter-select mono">
            <option v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </label>
        <span class="poll-hint" :class="{ 'poll-hint--on': loading }">●</span>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-status">{{ COL.status }}</span>
        <span class="col-wf">工作流 · 标题 / ID</span>
        <span class="col-step">步骤</span>
        <span class="col-dur">耗时</span>
      </div>

      <div
        v-for="wf in workflows"
        :key="wf.id"
        class="trow"
        role="button"
        tabindex="0"
        @click="openWorkflow(wf)"
        @keydown.enter="openWorkflow(wf)"
      >
        <span class="col-status"><StatusBadge :status="wf.status" /></span>
        <span class="col-wf" :class="{ 'col-wf--titled': wf.title }">
          <span v-if="wf.title" class="wf-title" :title="wf.title">{{ wf.title }}</span>
          <span class="wf-id mono" :title="wf.id">{{ shortId(wf.id) }}</span>
        </span>
        <span class="col-step mono">{{ workflowStepText(wf.status, wf.current_step, wf.total_steps) }}</span>
        <span class="col-dur mono">{{ rowDuration(wf) }}</span>
      </div>

      <div v-if="workflows.length === 0 && !error" class="empty mono">
        <p>暂无工作流</p>
        <RouterLink v-if="!readOnly" to="/workflows/new" class="empty-link">创建工作流</RouterLink>
      </div>
    </div>
  </div>
</template>

<style scoped>
.board {
  max-width: 1100px;
  margin: 0 auto;
}
.board-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.controls {
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 12px;
}
.create-link {
  background: var(--phosphor);
  color: var(--ink);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
  font-weight: 600;
}
.create-link:hover {
  text-decoration: none;
  opacity: 0.9;
}
.filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--queue);
}
.filter-label {
  font-size: 11px;
  letter-spacing: 0.06em;
}
.filter-select {
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.filter-select:focus {
  border-color: var(--phosphor);
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-hint--on {
  color: var(--phosphor);
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

.table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 124px minmax(200px, 1fr) 90px 120px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.thead {
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  cursor: pointer;
  font-size: 13px;
  outline: none;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--panel);
}
.trow:focus-visible {
  background: var(--panel);
  box-shadow: inset 2px 0 0 var(--phosphor);
}
.col-wf {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.wf-title {
  color: var(--paper);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-id {
  color: var(--phosphor);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-wf:not(.col-wf--titled) .wf-id {
  font-size: 13px;
}
.col-step {
  color: var(--queue);
}
.col-dur {
  color: var(--queue);
}
.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}
.empty p {
  margin: 0 0 10px;
}
.empty-link {
  display: inline-block;
  background: var(--phosphor);
  color: var(--ink);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 6px 12px;
  font-size: 12px;
  font-weight: 600;
}
.empty-link:hover {
  text-decoration: none;
  opacity: 0.9;
}

@media (max-width: 900px) {
  .board-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 10px;
  }
  .controls {
    flex-wrap: wrap;
  }
  /* 手机档（断点与 utils/mobileLayout.ts 的 MOBILE_MAX_WIDTH = 900 一致）：一行任务
     压成两行卡片——标题/id 一行，状态与步骤 / 耗时排列在下面，与「任务看板」手机版同一做法。 */
  .thead {
    display: none;
  }
  .trow {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 2px 0;
    padding: 8px 10px;
  }
  .col-wf {
    order: 1;
    flex: 1 0 calc(100% - 96px);
    min-width: 0;
  }
  .col-status {
    order: 2;
    flex: 0 0 96px;
    display: flex;
    justify-content: flex-end;
  }
  .col-step,
  .col-dur {
    order: 3;
    flex: none;
    font-size: 11px;
  }
  .col-dur::before {
    content: ' · ';
    white-space: pre;
    color: var(--queue);
  }
}

@media (max-width: 640px) {
  .filter-label {
    font-size: 10px;
  }
}
</style>
