<script setup lang="ts">
// 今天页（设计稿 2026-11-01 §4.3）= 「等我」列表 + 进行中的计划（链到计划页）+ 正在跑的会话 / job 数
// （链到工作台）。没有 digest、整理建议、收集箱这类卡；等我为空时首屏只有一句空态（健康状态不占地方）；
// 状态条只在有告警时出现。数据与撤销窗口在 store/today.ts，与顶栏「待我决策」浮层共用。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import DecisionQueue from '../components/today/DecisionQueue.vue'
import TodayStatusBar from '../components/today/TodayStatusBar.vue'
import TodayPlans from '../components/today/TodayPlans.vue'
import FocusMode from '../components/today/FocusMode.vue'
import SnoozedDrawer from '../components/today/SnoozedDrawer.vue'
import { t } from '../i18n'
import {
  actOnCard,
  beginTodayVisit,
  endTodayVisit,
  handledOpen,
  handledTodayCount,
  highlightedCardKey,
  includeExec,
  loadHandledToday,
  overlayOpen,
  queueCursor,
  refreshToday,
  setIncludeExec,
  snoozeCard,
  snoozedOpen,
  todayData,
  todayError,
  visibleCards,
} from '../store/today'
import { TOP_N } from '../utils/today'
import { ceilingReadOnlyNotice } from '../utils/ceiling'
import { useWhoami } from '../store/whoami'

// 只读身份（调用方上限 read_only）只能看：专注处理整页都在做写操作，灰显并说明原因（gofer-darr）。
const { readOnly } = useWhoami()

const nowSec = ref(Math.floor(Date.now() / 1000))
let clock: ReturnType<typeof setInterval> | null = null

// 「等我」唯一列表（服务端 waiting）；前 5 张直接展开，其余进全局浮层。
const shownCards = visibleCards
// Ctrl/⌘+J 跳过前 5 张后也要能看见高亮的那张：按光标把渲染张数撑开（issue gofer-okz3 ①）。
const queueLimit = computed(() => Math.max(TOP_N, queueCursor.value + 1))
// 正在跑的会话 / job（服务端数，链到工作台）；都是 0 时这一行不出现。
const running = computed(() => todayData.value?.running ?? { sessions: 0, jobs: 0 })
const showRunning = computed(() => running.value.sessions + running.value.jobs > 0)
// 状态条只在有告警时占地方（runner 离线、巡检失败……）。
const showStatus = computed(() => (todayData.value?.status.alerts.length ?? 0) > 0)
const failedToday = computed(() => todayData.value?.digest?.since_last?.jobs_failed ?? 0)

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

// 页面可见时开始一次访问，切走 / 关页 / 离开路由时结束（看够久才推进水位）；
// 切回来是新的一次访问并重拉。
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
        <h1 data-test="today-title">
          {{ shownCards.length ? t('today.title.n', { n: shownCards.length }) : t('today.title.0') }}
        </h1>
      </header>

      <DecisionQueue
        v-if="shownCards.length"
        :cards="shownCards"
        :now-sec="nowSec"
        :limit="queueLimit"
        :highlight-key="highlightedCardKey"
        @act="actOnCard"
        @snooze="snoozeCard"
        @more="openOverlay"
        @focus="openFocus"
      />

      <TodayPlans />

      <p v-if="showRunning" class="today-running" data-test="today-running">
        <RouterLink to="/workbench">{{ t('today.running', { sessions: running.sessions, jobs: running.jobs }) }}</RouterLink>
      </p>

      <div class="today-foot">
        <button
          v-if="shownCards.length"
          class="btn-focus"
          type="button"
          data-test="open-focus"
          :disabled="readOnly"
          :title="readOnly ? ceilingReadOnlyNotice() : undefined"
          @click="openFocus"
        >{{ t('today.focus') }}</button>
        <label class="chk">
          <input type="checkbox" :checked="includeExec" @change="setIncludeExec(($event.target as HTMLInputElement).checked)" />
          {{ t('today.inclExec') }}
        </label>
        <span v-if="shownCards.length" class="keys mono">{{ t('today.card.keys') }}</span>
        <span class="grow"></span>
        <button type="button" class="foot-link" data-test="open-snoozed" @click="snoozedOpen = true">
          {{ t('today.snoozed', { n: snoozedCount }) }}
        </button>
        <button type="button" class="foot-link" data-test="open-handled" @click="openHandled">
          {{ t('today.handledN', { n: handledTodayCount }) }}
        </button>
      </div>
    </section>

    <TodayStatusBar v-if="todayData && showStatus" :status="todayData.status" :failed="failedToday" />

    <SnoozedDrawer />
    <FocusMode v-if="focusOpen" :now-sec="nowSec" @close="focusOpen = false" />
  </div>
</template>

<style scoped>
.today {
  min-width: 0;
}
.today-main {
  min-width: 0;
  max-width: 880px;
}
.today-err {
  margin: 0 0 var(--s2);
  color: var(--fail);
  font-size: var(--fs-xs);
}
.today-hero {
  margin: 0 0 var(--s4);
}
.today-hero h1 {
  margin: 0;
  font-size: 34px;
  font-weight: 650;
  line-height: 1.1;
  letter-spacing: -0.03em;
  overflow-wrap: anywhere;
}
.today-running {
  margin: 0 0 var(--s3);
  font-size: var(--fs-sm);
}
.today-running a {
  color: var(--ink-2);
  text-decoration: none;
}
.today-running a:hover {
  color: var(--accent);
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
@media (max-width: 760px) {
  .today-hero h1 {
    font-size: 27px;
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
