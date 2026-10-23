<script setup lang="ts">
// P3 今天页（设计稿 §7.1）：首屏「N 件事在等你」+ 一行摘要 → 「自上次打开」摘要 → 类型 / 项目 chip
// → 决策卡片队列（前 5 张，其余进全局浮层）→ 卡片底部入口；右栏并行中 + 刚处理过；贴底状态条。
// 数据与撤销窗口在 store/today.ts，与顶栏「待我决策」浮层共用。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import DecisionQueue from '../components/today/DecisionQueue.vue'
import TodayStatusBar from '../components/today/TodayStatusBar.vue'
import TodayLanes from '../components/today/TodayLanes.vue'
import TodayRecentHandled from '../components/today/TodayRecentHandled.vue'
import FocusMode from '../components/today/FocusMode.vue'
import SnoozedDrawer from '../components/today/SnoozedDrawer.vue'
import FilterChips from '../ui/FilterChips.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import { useProjectFilter } from '../ui/projectFilter'
import StatusGlyph from '../ui/StatusGlyph.vue'
import { t } from '../i18n'
import {
  actOnCard,
  beginTodayVisit,
  endTodayVisit,
  handledOpen,
  handledTodayCount,
  includeExec,
  loadHandledToday,
  overlayOpen,
  refreshToday,
  setIncludeExec,
  snoozeCard,
  snoozedOpen,
  todayData,
  todayError,
  visibleCards,
} from '../store/today'
import { TOP_N } from '../utils/today'
import { filterByProject, filterByType, laneAgentStats, typeCounts, TODAY_TYPE_GROUPS } from '../utils/todayFilters'
import type { TodayLanesResp } from '../api/todayLanes'
import { formatDate } from '../utils/format'
import { ceilingReadOnlyNotice } from '../utils/ceiling'
import { useWhoami } from '../store/whoami'

// 只读身份（调用方上限 read_only）只能看：专注处理整页都在做写操作，灰显并说明原因（gofer-darr）。
const { readOnly } = useWhoami()

const nowSec = ref(Math.floor(Date.now() / 1000))
let clock: ReturnType<typeof setInterval> | null = null

// 项目是筛选条件（§6.4）：值的真源是 URL 的 ?project=。
const { project, setProject, options: projectKeys, loadOptions: loadProjectOptions, loadError: projectError } =
  useProjectFilter('/today')
const typeFilter = ref('')

const digest = computed(() => todayData.value?.digest)
const since = computed(() => digest.value?.since_last)
const dateText = computed(() => formatDate(nowSec.value * 1000))
const doneToday = computed(() => since.value?.jobs_done ?? 0)

// 首屏一行摘要：agent 在跑 / 空闲会话来自并行中的泳道，今天完成来自 digest。
const lanes = ref<TodayLanesResp | null>(null)
const stats = computed(() => laneAgentStats(lanes.value?.lanes))

// 队列先按项目、再按类型过滤；卡片底部与空态用过滤后的数量。
const projectCards = computed(() => filterByProject(visibleCards.value, project.value))
const counts = computed(() => typeCounts(projectCards.value))
const shownCards = computed(() => filterByType(projectCards.value, typeFilter.value))
const typeItems = computed(() => [
  { value: '', label: t('today.filter.all'), count: projectCards.value.length },
  ...TODAY_TYPE_GROUPS.map((g) => ({ value: g, label: t(`today.filter.${g}`), count: counts.value[g] })),
])
const emptyText = computed(() =>
  typeFilter.value || project.value
    ? t('today.queue.emptyHint')
    : t('today.queue.empty', { n: handledTodayCount.value }),
)

function openOverlay(): void {
  overlayOpen.value = true
}

function openHandled(): void {
  handledOpen.value = true
}

// 专注处理（T3）：全屏一次一张；退出后回到首页。
const focusOpen = ref(false)
function openFocus(): void {
  if (shownCards.value.length) focusOpen.value = true
}
const snoozedCount = computed(() => todayData.value?.snoozed ?? 0)

// 「自上次打开」：页面可见时开始一次访问，切走 / 关页 / 离开路由时结束（看够久才推进水位）；
// 切回来是新的一次访问，水位换成上一次访问的开始时间并重拉。
function onVisibility(): void {
  if (document.visibilityState === 'hidden') {
    endTodayVisit()
  } else {
    beginTodayVisit()
    void refreshToday()
  }
}
function onPageHide(): void {
  endTodayVisit()
}

onMounted(() => {
  if (document.visibilityState !== 'hidden') beginTodayVisit()
  document.addEventListener('visibilitychange', onVisibility)
  window.addEventListener('pagehide', onPageHide)
  void refreshToday()
  void loadHandledToday()
  void loadProjectOptions()
  clock = setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 30_000)
})
onUnmounted(() => {
  if (clock) clearInterval(clock)
  document.removeEventListener('visibilitychange', onVisibility)
  window.removeEventListener('pagehide', onPageHide)
  endTodayVisit()
})
</script>

<template>
  <div class="today page">
    <section class="today-main">
      <p v-if="todayError" class="today-err mono">{{ todayError }}</p>

      <header class="today-hero">
        <p class="date mono">{{ dateText }}</p>
        <h1 data-test="today-title">
          {{ shownCards.length ? t('today.title.n', { n: shownCards.length }) : t('today.title.0') }}
        </h1>
      </header>
      <div class="today-summary" data-test="today-summary">
        <span><StatusGlyph status="running" :size="13" />{{ t('today.sum.running', { n: stats.running }) }}</span>
        <span><StatusGlyph status="idle" :size="13" />{{ t('today.sum.idle', { n: stats.idle }) }}</span>
        <span><StatusGlyph status="done" :size="13" />{{ t('today.sum.done', { n: doneToday }) }}</span>
      </div>

      <details v-if="digest" class="digest" data-test="today-digest">
        <summary>
          {{ t('today.digest') }}
          <span class="dg-n"><b>{{ since?.jobs_done ?? 0 }}</b> {{ t('today.digest.done') }}</span>
          <span class="dg-n" :class="{ 'k-fail': (since?.jobs_failed ?? 0) > 0 }"><b>{{ since?.jobs_failed ?? 0 }}</b> {{ t('today.digest.failed') }}</span>
          <span class="dg-n"><b>{{ since?.commits ?? 0 }}</b> {{ t('today.digest.commits') }}</span>
        </summary>
        <div class="digest-full">
          <p class="digest-title mono">{{ digest.title }}</p>
          <p class="digest-text">{{ digest.text }}</p>
        </div>
      </details>

      <div class="today-filters">
        <FilterChips v-model="typeFilter" :items="typeItems" :label="t('today.filter.type')" />
        <ProjectFilter
          :model-value="project"
          :options="projectKeys"
          :error="projectError"
          @update:model-value="setProject"
        />
      </div>

      <DecisionQueue
        :cards="shownCards"
        :now-sec="nowSec"
        :limit="TOP_N"
        :empty-text="emptyText"
        @act="actOnCard"
        @snooze="snoozeCard"
        @more="openOverlay"
        @focus="openFocus"
      />

      <div class="today-foot">
        <button
          class="btn-focus"
          type="button"
          data-test="open-focus"
          :disabled="!shownCards.length || readOnly"
          :title="readOnly ? ceilingReadOnlyNotice() : undefined"
          @click="openFocus"
        >{{ t('today.focus') }}</button>
        <label class="chk">
          <input type="checkbox" :checked="includeExec" @change="setIncludeExec(($event.target as HTMLInputElement).checked)" />
          {{ t('today.inclExec') }}
        </label>
        <span class="keys mono">{{ t('today.card.keys') }}</span>
        <span class="grow"></span>
        <button type="button" class="foot-link" data-test="open-snoozed" @click="snoozedOpen = true">
          {{ t('today.snoozed', { n: snoozedCount }) }}
        </button>
        <button type="button" class="foot-link" data-test="open-handled" @click="openHandled">
          {{ t('today.handledN', { n: handledTodayCount }) }}
        </button>
      </div>
    </section>

    <aside class="today-aside">
      <TodayLanes @loaded="lanes = $event" />
      <TodayRecentHandled />
    </aside>

    <TodayStatusBar v-if="todayData" :status="todayData.status" :failed="since?.jobs_failed ?? 0" />

    <SnoozedDrawer />
    <FocusMode v-if="focusOpen" :now-sec="nowSec" @close="focusOpen = false" />
  </div>
</template>

<style scoped>
.today {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 340px;
  gap: var(--s5);
  min-width: 0;
}
.today-main {
  min-width: 0;
}
.today-err {
  margin: 0 0 var(--s2);
  color: var(--fail);
  font-size: var(--fs-xs);
}
.today-hero {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--s2) var(--s3);
}
.today-hero .date {
  width: 100%;
  margin: 0;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.today-hero h1 {
  margin: 0;
  font-size: 34px;
  font-weight: 650;
  line-height: 1.1;
  letter-spacing: -0.03em;
  overflow-wrap: anywhere;
}
.today-summary {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1) var(--s3);
  margin: var(--s2) 0 var(--s4);
  color: var(--ink-2);
  font-size: var(--fs-sm);
}
.today-summary span {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
}
.digest {
  margin: 0 0 var(--s3);
  color: var(--ink-2);
  font-size: var(--fs-sm);
}
.digest summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1) var(--s3);
  cursor: pointer;
  list-style: none;
}
.digest summary::-webkit-details-marker {
  display: none;
}
.digest summary::before {
  content: '';
  width: 0;
  height: 0;
  border-left: 5px solid var(--ink-3);
  border-top: 4px solid transparent;
  border-bottom: 4px solid transparent;
  transition: transform 0.2s var(--ease);
}
.digest[open] summary::before {
  transform: rotate(90deg);
}
.digest .dg-n b {
  color: var(--ink);
  font-variant-numeric: tabular-nums;
}
.digest .dg-n.k-fail b {
  color: var(--fail);
}
.digest-full {
  margin-top: var(--s2);
  padding-left: var(--s3);
}
.digest-title {
  margin: 0 0 var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.digest-text {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.today-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1) var(--s2);
  margin-bottom: var(--s3);
  min-width: 0;
}
.today-foot {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1) var(--s2);
  margin-top: var(--s3);
}
.btn-focus {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  min-height: 32px;
  padding: 0 var(--s3);
  color: var(--ink);
  background: var(--accent-soft);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  font-weight: 600;
  cursor: pointer;
}
.btn-focus:hover:not(:disabled) {
  background: var(--accent);
  color: var(--bg);
}
.btn-focus:disabled {
  color: var(--ink-3);
  background: transparent;
  border-color: var(--line);
  cursor: default;
}
.btn-focus:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.chk {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.grow {
  flex: 1;
}
.keys {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.foot-link {
  padding: 0;
  color: var(--ink-3);
  background: transparent;
  border: 0;
  font: inherit;
  font-size: var(--fs-sm);
  text-decoration: underline;
  cursor: pointer;
}
.foot-link:hover {
  color: var(--accent);
}
.today-aside {
  min-width: 0;
}
@media (max-width: 1279px) {
  .today {
    grid-template-columns: minmax(0, 1fr) 300px;
    gap: var(--s4);
  }
}
@media (max-width: 1099px) {
  .today {
    grid-template-columns: minmax(0, 1fr);
  }
}
@media (max-width: 760px) {
  .today-hero h1 {
    font-size: 27px;
  }
  .today-summary {
    gap: var(--s1) var(--s2);
  }
  .today-filters {
    flex-wrap: nowrap;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .today-filters::-webkit-scrollbar {
    display: none;
  }
  .btn-focus,
  .foot-link {
    min-height: 40px;
  }
  /* 手机上没有实体键盘，快捷键提示不占位 */
  .keys {
    display: none;
  }
}
</style>
