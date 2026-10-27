<script setup lang="ts">
// Workflows 列表（job 链）：轮询 listWorkflows（2.5s），Page Visibility 暂停/恢复，
// 状态 chip 过滤 + 服务端分页（每页 50 条，页码写 ?page=），行点击进详情。
// UI 重设计 P5：ui/FilterChips + ui/StatusPill + 分段 ProgressBar（设计稿 §7.2）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import { useRoute, useRouter } from 'vue-router'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import { listWorkflows } from '../api/client'
import { fmtDateTime, fmtDuration } from '../api/time'
import { workflowStatusOptions, statusText } from '../utils/statusText'
import { clampPage, pageCountOf, pageQueryUpdate, pageWindow, readPageQuery } from '../utils/issuePaging'
import { t, te } from '../i18n'
import FilterChips from '../ui/FilterChips.vue'
import StatusPill from '../ui/StatusPill.vue'
import ProgressBar from '../ui/ProgressBar.vue'
import { useListReturn } from '../ui/useListReturn'
import { useWhoami } from '../store/whoami'
import type { FilterChipItem } from '../ui/filterChips'
import type { Workflow, WorkflowStatus } from '../api/types'

const route = useRoute()
const router = useRouter()
// 调用方上限只读时，创建入口一律不摆出来（服务端也会 403）。
const { readOnly } = useWhoami()

// 详情页的「← 工作流」回到这里（带筛选、页码与滚动位置）。
useListReturn('workflows', '/workflows')

// 每页 50 条：limit / offset 交给服务端切页，total 是同一过滤条件下的总条数。
const PAGE_SIZE = 50

const workflows = ref<Workflow[]>([])
const loading = ref(false)
const error = ref('')
const statusFilter = ref<'' | WorkflowStatus>('')
const total = ref(0)
const page = ref(readPageQuery(route.query))
const pageCount = computed(() => pageCountOf(total.value, PAGE_SIZE))
const currentPage = computed(() => clampPage(page.value, pageCount.value))
const tableEl = ref<HTMLElement | null>(null)

// 状态 chip 的文案走 i18n；没有 status.<值> 时回退到旧的中文映射（P5 之后只剩 i18n）。
const statusOptions = workflowStatusOptions().filter((o) => o.value !== '')

// 服务端分页后拿到的是当前页，按状态计数要扫全量才能给出，所以 chip 只做筛选、不再带计数
// （全部条数在页头副标题与页脚的「共 N 条」上）。
const statusItems = computed<FilterChipItem[]>(() => [
  { value: '', label: t('workflows.filterAll') },
  ...statusOptions.map((o) => ({
    value: o.value,
    label: te(`status.${o.value}`) ? t(`status.${o.value}`) : statusText(o.value),
  })),
])

const filtered = computed(() => statusFilter.value !== '')

async function fetchWorkflows(): Promise<void> {
  loading.value = true
  try {
    const resp = await listWorkflows({
      status: statusFilter.value || undefined,
      limit: PAGE_SIZE,
      offset: (currentPage.value - 1) * PAGE_SIZE,
    })
    workflows.value = resp.workflows ?? []
    total.value = resp.total ?? workflows.value.length
    error.value = ''
    // 当前页已越界（行被删、或别人改小了这个过滤下的总数）：退回最后一页重拉，URL 跟着修。
    if (page.value > pageCount.value) {
      page.value = pageCount.value
      writeQuery(pageQueryUpdate(route.query, page.value))
      void fetchWorkflows()
    }
  } catch (e) {
    // 401 已由 client 处理（跳转登录）；其余显示错误条
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 页码写进 ?page=（第 1 页不写），可分享、后退可复原。
function writeQuery(next: Record<string, string>): void {
  void router.replace({ query: next })
}

function gotoPage(p: number): void {
  page.value = clampPage(p, pageCount.value)
  writeQuery(pageQueryUpdate(route.query, page.value))
  tableEl.value?.scrollIntoView?.({ block: 'start' })
  void fetchWorkflows()
}

// 换 chip 立即回到第 1 页并重拉（WS 失效通知也会触发）。
watch(statusFilter, () => {
  page.value = 1
  writeQuery(pageQueryUpdate(route.query, 1))
  void fetchWorkflows()
})

// 后退 / 前进改 URL 时页码跟着走（自己换页时 page 已是新值，不重复拉一次）。
watch(
  () => route.query.page,
  (value) => {
    const next = readPageQuery({ page: value })
    if (next === page.value) return
    page.value = next
    void fetchWorkflows()
  },
)

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
        <p class="sub">{{ total ? t('workflows.subtitle', { n: total }) : t('workflows.subtitleEmpty') }}</p>
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

    <div ref="tableEl" class="table" role="table">
      <div class="thead" role="row">
        <span class="col-status">{{ t('workflows.col.status') }}</span>
        <span class="col-wf">{{ t('workflows.col.name') }}</span>
        <span class="col-step">{{ t('workflows.col.steps') }}</span>
        <span class="col-started">{{ t('workflows.col.started') }}</span>
        <span class="col-dur">{{ t('workflows.col.duration') }}</span>
      </div>

      <div
        v-for="wf in workflows"
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

      <div v-if="workflows.length === 0 && !error" class="empty">
        <p>{{ t('workflows.empty') }}</p>
        <p class="empty-hint">{{ t('workflows.emptyHint') }}</p>
        <RouterLink v-if="!readOnly" to="/workflows/new" class="btn btn--primary">{{ t('workflows.new') }}</RouterLink>
      </div>
    </div>

    <!-- 服务端分页（每页 50 条）：页码进 ?page=，切筛选回第 1 页。 -->
    <nav v-if="total > 0" class="pager" data-test="pager" :aria-label="t('workflows.pager.label')">
      <span class="pager-count" data-test="page-summary">{{ t('workflows.pageSummary', { total, page: currentPage, pages: pageCount }) }}</span>
      <button type="button" class="page-btn" :disabled="currentPage <= 1" data-test="page-prev" @click="gotoPage(currentPage - 1)">{{ t('workflows.pager.prev') }}</button>
      <template v-for="(p, i) in pageWindow(currentPage, pageCount)" :key="i"><span v-if="p === null" class="pager-gap">…</span><button v-else type="button" class="page-btn" :class="{ 'page-btn--on': p === currentPage }" :data-test="`page-${p}`" :aria-label="t('workflows.pager.page', { page: p })" @click="gotoPage(p)">{{ p }}</button></template>
      <button type="button" class="page-btn" :disabled="currentPage >= pageCount" data-test="page-next" @click="gotoPage(currentPage + 1)">{{ t('workflows.pager.next') }}</button>
    </nav>
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
/* 分页条（与 Issues / 记忆页同一套写法）：可换行，手机上按钮够大。 */
.pager {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s1);
  margin-top: var(--s3);
  font-size: var(--fs-sm);
}
.pager-count {
  color: var(--ink-3);
  margin-right: var(--s2);
  font-size: var(--fs-xs);
}
.page-btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: transparent;
  color: var(--ink);
  padding: 3px 9px;
  font: inherit;
  font-size: var(--fs-xs);
  min-width: 30px;
  min-height: 30px;
}
.page-btn--on {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.page-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.pager-gap {
  color: var(--ink-3);
}
@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
    gap: var(--s2);
  }
  .btn,
  .page-btn,
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
