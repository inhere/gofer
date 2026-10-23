<script setup lang="ts">
// Workflows 列表（job 链）：轮询 listWorkflows（2.5s），Page Visibility 暂停/恢复，
// 状态 chip 过滤（客户端收窄 + 计数，接口没有按项目过滤的参数），行点击进详情。
// UI 重设计 P5：ui/FilterChips + ui/StatusPill + 分段 ProgressBar（设计稿 §7.2）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import { useRouter } from 'vue-router'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import { listWorkflows } from '../api/client'
import { fmtDateTime, fmtDuration } from '../api/time'
import { workflowStatusOptions, statusText } from '../utils/statusText'
import { t, te } from '../i18n'
import FilterChips from '../ui/FilterChips.vue'
import StatusPill from '../ui/StatusPill.vue'
import ProgressBar from '../ui/ProgressBar.vue'
import { useListReturn } from '../ui/useListReturn'
import { useWhoami } from '../store/whoami'
import type { FilterChipItem } from '../ui/filterChips'
import type { Workflow, WorkflowStatus } from '../api/types'

const router = useRouter()
// 调用方上限只读时，创建入口一律不摆出来（服务端也会 403）。
const { readOnly } = useWhoami()

// 详情页的「← 工作流」回到这里（带筛选与滚动位置）。
useListReturn('workflows', '/workflows')

const workflows = ref<Workflow[]>([])
const loading = ref(false)
const error = ref('')
const statusFilter = ref<'' | WorkflowStatus>('')

// 状态 chip 的文案走 i18n；没有 status.<值> 时回退到旧的中文映射（P5 之后只剩 i18n）。
const statusOptions = workflowStatusOptions().filter((o) => o.value !== '')

const statusItems = computed<FilterChipItem[]>(() => {
  const counts = new Map<string, number>()
  for (const wf of workflows.value) {
    counts.set(wf.status, (counts.get(wf.status) ?? 0) + 1)
  }
  const all: FilterChipItem = {
    value: '',
    label: t('workflows.filterAll'),
    count: workflows.value.length,
  }
  return [
    all,
    ...statusOptions.map((o) => ({
      value: o.value,
      label: te(`status.${o.value}`) ? t(`status.${o.value}`) : statusText(o.value),
      count: counts.get(o.value) ?? 0,
    })),
  ]
})

const visible = computed(() =>
  statusFilter.value ? workflows.value.filter((wf) => wf.status === statusFilter.value) : workflows.value,
)

const filtered = computed(() => statusFilter.value !== '')

async function fetchWorkflows(): Promise<void> {
  loading.value = true
  try {
    const resp = await listWorkflows()
    workflows.value = resp.workflows ?? []
    error.value = ''
  } catch (e) {
    // 401 已由 client 处理（跳转登录）；其余显示错误条
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 换 chip 立即重拉一次（与之前换 select 的行为一致；WS 失效通知也会触发）。
watch(statusFilter, () => {
  void fetchWorkflows()
})

// 工作流耗时：created_at -> updated_at（运行中按 now）。
function rowDuration(wf: Workflow): string {
  const end = wf.status === 'running' ? Math.floor(Date.now() / 1000) : wf.updated_at
  return fmtDuration(Math.max(0, end - wf.created_at))
}

// 步骤进度：done 之后后端的 current_step 会越过 total_steps，显示层夹到 total。
function stepsDone(wf: Workflow): number {
  return wf.total_steps > 0 && wf.current_step > wf.total_steps ? wf.total_steps : wf.current_step
}

function stepsText(wf: Workflow): string {
  return t('workflows.stepsOf', { cur: stepsDone(wf), total: wf.total_steps })
}

// 列表里的分段进度条只用「已完成 / 未开始」两段（列表拿不到每步状态）。
function stepParts(wf: Workflow) {
  const done = stepsDone(wf)
  return [
    { family: 'done' as const, count: done },
    { family: 'todo' as const, count: Math.max(0, wf.total_steps - done) },
  ]
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
  <div class="page" data-test="workflows-page">
    <header class="page-head">
      <!-- 工作流 / 模板库在 docs/guide/stability.md 里属实验性，标题旁挂标注。 -->
      <div class="head-main">
        <h1 class="title">{{ t('workflows.title') }}<ExperimentalBadge /></h1>
        <p class="sub">{{ visible.length ? t('workflows.subtitle', { n: visible.length }) : t('workflows.subtitleEmpty') }}</p>
      </div>
      <div class="head-actions">
        <span class="poll-dot" :class="{ 'poll-dot--on': loading }" aria-hidden="true">●</span>
        <RouterLink v-if="!readOnly" to="/workflows/new" class="btn btn--primary" data-test="new-workflow">
          {{ t('workflows.new') }}
        </RouterLink>
      </div>
    </header>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="list-bar">
      <FilterChips v-model="statusFilter" :items="statusItems" :label="t('workflows.col.status')" />
      <button v-if="filtered" class="link-btn" type="button" data-test="clear-filters" @click="statusFilter = ''">
        {{ t('workflows.clearFilters') }}
      </button>
    </div>

    <div class="table" role="table">
      <div class="thead" role="row">
        <span class="col-status">{{ t('workflows.col.status') }}</span>
        <span class="col-wf">{{ t('workflows.col.name') }}</span>
        <span class="col-step">{{ t('workflows.col.steps') }}</span>
        <span class="col-started">{{ t('workflows.col.started') }}</span>
        <span class="col-dur">{{ t('workflows.col.duration') }}</span>
      </div>

      <div
        v-for="wf in visible"
        :key="wf.id"
        class="trow"
        role="row"
        tabindex="0"
        @click="openWorkflow(wf)"
        @keydown.enter="openWorkflow(wf)"
      >
        <span class="col-status"><StatusPill :status="wf.status" /></span>
        <span class="col-wf" :class="{ 'col-wf--titled': wf.title }">
          <span v-if="wf.title" class="wf-title" :title="wf.title">{{ wf.title }}</span>
          <span class="wf-id mono" :title="wf.id">{{ wf.id }}</span>
        </span>
        <span class="col-step">
          <template v-if="wf.total_steps > 0">
            <ProgressBar
              :parts="stepParts(wf)"
              :total="wf.total_steps"
              :done="stepsDone(wf)"
              :label="stepsText(wf)"
              :legend="false"
              size="sm"
            />
          </template>
          <span v-else class="dim">{{ stepsText(wf) }}</span>
        </span>
        <span class="col-started" :title="fmtDateTime(wf.created_at)">{{ fmtDateTime(wf.created_at) }}</span>
        <span class="col-dur mono">{{ rowDuration(wf) }}</span>
      </div>

      <div v-if="visible.length === 0 && !error" class="empty">
        <p>{{ t('workflows.empty') }}</p>
        <p class="empty-hint">{{ t('workflows.emptyHint') }}</p>
        <RouterLink v-if="!readOnly" to="/workflows/new" class="btn btn--primary">{{ t('workflows.new') }}</RouterLink>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page {
  max-width: 1100px;
  margin: 0 auto;
}
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
.link-btn {
  background: none;
  border: 0;
  padding: 0 2px;
  color: var(--ink-3);
  font: inherit;
  font-size: var(--fs-sm);
  text-decoration: underline;
  cursor: pointer;
}
.link-btn:hover {
  color: var(--ink);
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
  gap: var(--s3);
  margin-bottom: var(--s3);
  min-width: 0;
}
.table {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  overflow: hidden;
  background: var(--surface);
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 124px minmax(200px, 1fr) 150px 150px 110px;
  align-items: center;
  gap: var(--s3);
  padding: 10px var(--s3);
}
.thead {
  background: var(--surface-2);
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  color: var(--ink-3);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  cursor: pointer;
  font-size: var(--fs-sm);
  outline: none;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--surface-2);
}
.trow:focus-visible {
  box-shadow: inset 2px 0 0 var(--accent);
}
.col-wf {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.wf-title {
  color: var(--ink);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-id {
  color: var(--accent);
  font-size: var(--fs-xs);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-wf:not(.col-wf--titled) .wf-id {
  font-size: var(--fs-sm);
}
.col-started {
  color: var(--ink-3);
  white-space: nowrap;
}
.col-dur {
  color: var(--ink-3);
}
.col-step {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-width: 0;
}
.dim {
  color: var(--ink-3);
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
.empty-hint {
  color: var(--ink-3);
}
.empty .btn {
  margin-top: var(--s1);
}
@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
    gap: var(--s2);
  }
  .btn,
  .link-btn {
    min-height: 40px;
  }
  /* 手机：一行压成两行卡片——标题/id 一行，状态与步骤 / 耗时排列在下面。 */
  .thead {
    display: none;
  }
  .trow {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 0;
    padding: var(--s2) var(--s3);
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
  .col-step {
    order: 3;
    flex: 1 1 120px;
  }
  .col-started,
  .col-dur {
    order: 4;
    font-size: var(--fs-xs);
    white-space: nowrap;
  }
  .col-dur::before {
    content: ' · ';
    white-space: pre;
  }
}
</style>
