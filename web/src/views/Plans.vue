<script setup lang="ts">
// Plans 列表：轮询 listPlans（2.5s，只刷当前页），Page Visibility 暂停/恢复，status/project/tag/q
// 过滤 + limit/offset 分页（F-d），行点击进详情；顶部内联「新建计划」表单。
// 过滤与翻页全部落在 URL query（status/project/q/offset），刷新/分享都保持同一视图。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { createPlan, listPlans, listProjects } from '../api/client'
import { t } from '../i18n'
import type { Plan, PlanStatus } from '../api/types'
import FilterChips from '../ui/FilterChips.vue'
import FilterDrawer from '../ui/FilterDrawer.vue'
import PlanOverviewRow from '../ui/PlanOverviewRow.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import { useListReturn } from '../ui/useListReturn'
import { chipItems, type ChipValue } from '../ui/filterChips'
import { planStatusOptions } from '../utils/statusText'
import { COL } from '../utils/uiText'
import { createLiveTopic } from '../utils/useLiveTopic'
import { useWhoami } from '../store/whoami'

const route = useRoute()
const router = useRouter()
// 调用方上限只读时，新建入口一律不摆出来（服务端也会 403）。
const { readOnly } = useWhoami()
// 与后端默认页大小一致（GET /v1/plans 缺省 20、上限 100）。
const PAGE_SIZE = 20

const plans = ref<Plan[]>([])
// total 是同条件下的总条数（服务端给的，不是本页条数）：翻页按钮据此判断有没有下一页。
const total = ref(0)
const loading = ref(false)
const error = ref('')

// URL query 是过滤条件的唯一来源（get 读 URL、set 写 URL），所以浏览器前进/后退、分享链接
// 都能复原同一页；q 是输入框的即时值，回车/失焦才写回 URL。
// 默认看「进行中 + 受阻」：计划一受阻就从默认列表里消失，正是最需要人看到的时候。
const DEFAULT_PLAN_STATUSES: PlanStatus[] = ['open', 'blocked']
const statusFilter = computed<PlanStatus[]>(() => {
  const raw = typeof route.query.status === 'string' ? route.query.status : ''
  if (raw === 'all') return []
  return raw ? raw.split(',').filter((s): s is PlanStatus => ['open', 'done', 'archived', 'blocked'].includes(s)) : [...DEFAULT_PLAN_STATUSES]
})
const isDefaultStatusFilter = computed(
  () => statusFilter.value.length === DEFAULT_PLAN_STATUSES.length && DEFAULT_PLAN_STATUSES.every((s) => statusFilter.value.includes(s)),
)
const projectFilter = computed({
  get: () => {
    const p = route.query.project
    return typeof p === 'string' ? p : ''
  },
  set: (value: string) => setFilter('project', value),
})
const tagFilter = computed({
  get: () => typeof route.query.tag === 'string' ? route.query.tag : '',
  set: (value: string) => setFilter('tag', value),
})
const qFilter = computed(() => {
  const q = route.query.q
  return typeof q === 'string' ? q : ''
})
const offset = computed(() => {
  const o = Number(route.query.offset)
  return Number.isFinite(o) && o > 0 ? Math.floor(o) : 0
})

const qInput = ref(qFilter.value)
watch(qFilter, (v) => {
  qInput.value = v
})

// pushQuery 合并写回 URL：空值删除该键（不留下 ?status= 这种空参数）。
function pushQuery(patch: Record<string, string | undefined>): void {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries({ ...route.query, ...patch })) {
    if (typeof v === 'string' && v) {
      next[k] = v
    }
  }
  void router.push({ path: '/plans', query: next })
}

// 过滤条件变化一律回到第 1 页（否则换了过滤还在 offset=40 会看到空页）。
function setFilter(key: 'status' | 'project' | 'tag', value: string): void {
  pushQuery({ [key]: value || undefined, offset: undefined })
}

function applyQueryInput(): void {
  const v = qInput.value.trim()
  if (v !== qFilter.value) {
    pushQuery({ q: v || undefined, offset: undefined })
  }
}

const projectKeys = ref<string[]>([])
const projectOptions = computed(() => {
  const keys = [...projectKeys.value]
  if (projectFilter.value && !keys.includes(projectFilter.value)) {
    keys.unshift(projectFilter.value)
  }
  return keys
})

// 内联新建
// 窄屏时新建表单默认收起，只留「＋ 新建计划」按钮；桌面宽度下 CSS 始终展开。
const createOpen = ref(false)
const newTitle = ref('')
const newDesc = ref('')
const newTags = ref('')
const creating = ref(false)
const createError = ref('')

const statusOptions: Array<{ value: PlanStatus | ''; label: string }> = planStatusOptions()
// 状态 chip（设计稿 §7）：多选累积，一个不选 = 全部。计数不在列表接口里，不显示徽标。
const statusChipItems = computed(() => chipItems(statusOptions))

// 「筛选」抽屉：关键字与 tag（项目与状态已经在列表头）。
const filtersOpen = ref(false)
const drawerFilterCount = computed(() => [qFilter.value, tagFilter.value].filter(Boolean).length)

const pageFrom = computed(() => (plans.value.length === 0 ? 0 : offset.value + 1))
const pageTo = computed(() => offset.value + plans.value.length)
const hasPrev = computed(() => offset.value > 0)
const hasNext = computed(() => pageTo.value < total.value)
const hasFilters = computed(() => Boolean(projectFilter.value || tagFilter.value || qFilter.value || (statusFilter.value.length > 0 && !isDefaultStatusFilter.value)))

// FilterChips 的多选结果直接换算成 URL 上的 status=open,blocked（空 = all）。
function onStatusChips(value: ChipValue): void {
  const list = (Array.isArray(value) ? value : value ? [value] : []).filter(Boolean) as PlanStatus[]
  setFilter('status', list.length ? list.join(',') : 'all')
}

function clearFilters(): void {
  pushQuery({ status: 'all', project: undefined, tag: undefined, q: undefined, offset: undefined })
}

function prevPage(): void {
  const prev = offset.value - PAGE_SIZE
  pushQuery({ offset: prev > 0 ? String(prev) : undefined })
}

function nextPage(): void {
  if (hasNext.value) {
    pushQuery({ offset: String(offset.value + PAGE_SIZE) })
  }
}


async function fetchPlans(): Promise<void> {
  loading.value = true
  try {
    const resp = await listPlans({
      statuses: statusFilter.value,
      project: projectFilter.value || undefined,
      tags: tagFilter.value.split(',').map((tag) => tag.trim()).filter(Boolean),
      q: qFilter.value || undefined,
      limit: PAGE_SIZE,
      offset: offset.value,
    })
    plans.value = resp.plans ?? []
    total.value = resp.total ?? plans.value.length
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 项目下拉的数据源（项目数量少，取一次即可；失败静默，不阻塞 plan 列表）。
async function fetchProjects(): Promise<void> {
  try {
    const resp = await listProjects()
    projectKeys.value = resp.projects ?? []
  } catch {
    projectKeys.value = []
  }
}

async function onCreate(): Promise<void> {
  if (!newTitle.value.trim() || creating.value) {
    return
  }
  creating.value = true
  try {
    const p = await createPlan({
      title: newTitle.value.trim(),
      description: newDesc.value.trim() || undefined,
      tags: newTags.value.split(',').map((tag) => tag.trim()).filter(Boolean),
    })
    newTitle.value = ''
    newDesc.value = ''
    newTags.value = ''
    createError.value = ''
    void router.push(`/plans/${encodeURIComponent(p.plan_id)}`)
  } catch (e) {
    createError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

// 进度条 / 卡点 / 主会话 / 最近活动都在 PlanOverviewRow 里算（ui/progress.ts 的分段口径）。
// Q3：`plans` 主题（plan / todo / 决策变化）触发重拉当前页；WS 断开 >15s 才由 30s 兜底轮询接手。
const livePlans = createLiveTopic('plans', { initial: false, fetch: fetchPlans })

// 过滤/翻页变化 -> 立即刷新（轮询只重复当前页，不重置 offset）。
watch([() => statusFilter.value.join(','), projectFilter, tagFilter, qFilter, offset], () => void fetchPlans())

onMounted(() => {
  void fetchPlans()
  void fetchProjects()
  livePlans.start()
})

// 计划详情的 BackLink 能回到当前筛选与滚动位置（设计稿 §16）。
useListReturn('plans', '/plans')

onUnmounted(() => {
  livePlans.stop()
})
</script>

<template>
  <div class="board page">
    <div class="board-head">
      <h1 class="title mono">{{ t('plans.list.title') }}</h1>
      <div class="controls mono">
        <ProjectFilter v-model="projectFilter" :options="projectOptions" />
        <FilterChips
          :items="statusChipItems"
          :model-value="statusFilter"
          multiple
          :label="t('plans.list.statusFilter')"
          @update:model-value="onStatusChips"
        />
        <FilterDrawer
          v-model:open="filtersOpen"
          :active-count="drawerFilterCount"
          :clearable="hasFilters"
          @clear="clearFilters"
        >
          <div class="filter-drawer-body">
            <label class="filter">
              <span class="filter-label">{{ t('plans.list.keyword') }}</span>
              <input
                v-model="qInput"
                class="filter-input mono"
                :placeholder="t('plans.list.keywordPlaceholder')"
                spellcheck="false"
                @keydown.enter.prevent="applyQueryInput"
                @blur="applyQueryInput"
              />
            </label>
            <label class="filter">
              <span class="filter-label">{{ COL.tags }}</span>
              <input v-model="tagFilter" class="filter-input mono" :placeholder="t('plans.list.tagPlaceholder')" spellcheck="false" />
            </label>
          </div>
        </FilterDrawer>
        <span class="poll-hint" :class="{ 'poll-hint--on': loading }">●</span>
        <button v-if="!readOnly" class="create-toggle mono" type="button" :aria-expanded="createOpen" @click="createOpen = !createOpen">
          {{ createOpen ? t('plans.list.collapse') : t('plans.list.new') }}
        </button>
      </div>
    </div>

    <form v-if="!readOnly" class="create-row mono" :class="{ 'create-row--open': createOpen }" @submit.prevent="onCreate">
      <label class="create-field">
        <span>{{ COL.title }}</span>
        <input v-model="newTitle" class="create-input mono" :placeholder="t('plans.list.titlePlaceholder')" />
      </label>
      <label class="create-field create-field--desc">
        <span>{{ t('plans.detail.description') }}</span>
        <input v-model="newDesc" class="create-input mono" :placeholder="t('plans.list.descPlaceholder')" />
      </label>
      <label class="create-field">
        <span>{{ COL.tags }}</span>
        <input v-model="newTags" class="create-input mono" :placeholder="t('plans.list.tagsPlaceholder')" />
      </label>
      <button class="create-btn mono" type="submit" :disabled="!newTitle.trim() || creating">
        {{ creating ? t('plans.list.creating') : t('plans.list.createSubmit') }}
      </button>
    </form>

    <p v-if="createError" class="error mono">{{ createError }}</p>
    <p v-if="error" class="error mono">{{ error }}</p>

    <!-- 计划总览（设计稿 §7）：每行一个计划，进度、卡点、主会话、最近活动都在行里。 -->
    <div class="table">
      <div class="thead mono">
        <span>{{ COL.status }}</span>
        <span>{{ t('plans.list.plan') }}</span>
        <span>{{ COL.progress }}</span>
        <span>{{ t('plans.overview.blockers') }}</span>
        <span>{{ t('plans.overview.supervisor') }}</span>
        <span>{{ t('plans.overview.activity') }}</span>
      </div>

      <PlanOverviewRow v-for="p in plans" :key="p.plan_id" :plan="p" />

      <div v-if="plans.length === 0 && !error" class="empty mono">
        <p>{{ hasFilters ? t('plans.list.emptyFiltered') : t('plans.list.empty') }}</p>
      </div>
    </div>

    <!-- F-d 分页：当前页/总条数都由服务端给，上一页/下一页写回 URL（offset）。 -->
    <div v-if="total > 0" class="pager mono">
      <button class="page-btn" type="button" :disabled="!hasPrev || loading" @click="prevPage">
        {{ t('plans.list.prev') }}
      </button>
      <span class="page-info">{{ t('plans.list.page', { from: pageFrom, to: pageTo, total }) }}</span>
      <button class="page-btn" type="button" :disabled="!hasNext || loading" @click="nextPage">
        {{ t('plans.list.next') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.board-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--ink);
  margin: 0;
}
.controls {
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 12px;
}
.filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--ink-3);
}
.filter-label {
  font-size: 11px;
  letter-spacing: 0.06em;
}
.filter-select,
.filter-input,
.create-input {
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.filter-input {
  min-width: 180px;
}
.status-chips { display: inline-flex; flex-wrap: wrap; gap: 5px; }
.filter-select:focus,
.filter-input:focus,
.create-input:focus {
  border-color: var(--accent);
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-hint--on {
  color: var(--accent);
}

.create-row {
  display: flex;
  align-items: flex-end;
  gap: 10px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 10px;
  margin-bottom: 12px;
  background: color-mix(in srgb, var(--surface) 72%, transparent);
}
.create-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 180px;
  color: var(--ink-3);
  font-size: 11px;
  letter-spacing: 0.04em;
}
.create-field--desc {
  flex: 1;
  min-width: 220px;
}
.create-input {
  width: 100%;
  min-height: 28px;
}
.create-btn {
  background: var(--accent);
  color: var(--bg);
  border: 1px solid var(--accent);
  border-radius: var(--r-chip);
  padding: 5px 10px;
  font-size: 12px;
  font-weight: 600;
  min-height: 28px;
}
.create-btn:hover:not(:disabled) {
  opacity: 0.9;
}
.create-btn:disabled {
  opacity: 0.55;
  cursor: default;
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  overflow: hidden;
}
.thead {
  display: grid;
  grid-template-columns: 96px minmax(180px, 1.4fr) 190px minmax(140px, 1fr) 120px 96px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
  background: var(--surface);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--ink-3);
  text-transform: uppercase;
}
.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--ink-3);
  font-size: 13px;
}
.empty p {
  margin: 0;
}

/* F-d 分页条：与表格同宽，居中放"第 a–b 条 / 共 N 条"，两侧是翻页按钮。 */
.pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--ink-3);
}
.page-btn {
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 12px;
}
.page-btn:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.page-btn:disabled {
  opacity: 0.45;
  cursor: default;
}
.page-info {
  letter-spacing: 0.04em;
}

.filter-drawer-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.create-toggle {
  display: none;
  padding: 4px 10px;
  color: var(--ink);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  cursor: pointer;
}

.filter-drawer {
  display: contents;
}
.filter-drawer-toggle {
  display: none;
}
.filter-drawer-body {
  display: contents;
}

/* 手机档（断点与 utils/mobileLayout.ts 的 MOBILE_MAX_WIDTH = 900 一致）：列头隐藏，一行计划
   改成卡片。 */
@media (max-width: 900px) {
  .board-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 10px;
  }
  .controls,
  .create-row {
    flex-wrap: wrap;
  }
  .controls {
    width: 100%;
    gap: 8px;
  }
  .status-chips {
    flex: 1 1 100%;
    overflow-x: auto;
    flex-wrap: nowrap;
    padding-bottom: 2px;
  }
  .status-chip {
    flex: none;
  }
  .filter-drawer {
    display: block;
    position: relative;
    flex: 1 1 auto;
  }
  .filter-drawer-toggle {
    display: inline-flex;
    align-items: center;
    min-height: 28px;
    padding: 4px 10px;
    color: var(--ink);
    border: 1px solid var(--line);
    border-radius: var(--r-chip);
    cursor: pointer;
    list-style: none;
  }
  .filter-drawer-toggle::-webkit-details-marker {
    display: none;
  }
  .filter-drawer-body {
    display: none;
    position: absolute;
    z-index: 3;
    top: calc(100% + 6px);
    left: 0;
    min-width: min(320px, 90vw);
    padding: 10px;
    flex-direction: column;
    gap: 8px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-chip);
    box-shadow: 0 8px 24px rgba(0,0,0,.25);
  }
  .filter-drawer[open] .filter-drawer-body {
    display: flex;
  }
  .filter-drawer-body .filter {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .filter-drawer-body .filter-input,
  .filter-drawer-body .filter-select {
    min-width: 0;
    flex: 1;
  }
  .poll-hint {
    margin-left: auto;
  }
  .create-toggle {
    display: inline-block;
    order: 5;
  }
  .create-field,
  .create-field--desc {
    flex: 1 1 220px;
  }
  .thead {
    display: none;
  }
  .create-toggle {
    display: inline-block;
  }
  .create-row:not(.create-row--open) {
    display: none;
  }
  /* 窄屏一个 plan 压成三行：标题/id + 状态 / project · 更新 / 进度 */
  .trow {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      "plan status"
      "project updated"
      "tags tags"
      "counts counts";
    gap: 3px 10px;
    padding: 8px 10px;
  }
  .trow .col-status { grid-area: status; justify-self: end; }
  .trow .col-plan { grid-area: plan; }
  .trow .col-project { grid-area: project; font-size: 11px; }
  .trow .col-tags { grid-area: tags; font-size: 11px; }
  .trow .col-updated { grid-area: updated; font-size: 11px; justify-self: end; }
  .trow .col-counts { grid-area: counts; font-size: 11px; }
}
</style>
