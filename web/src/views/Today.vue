<script setup lang="ts">
// N3「今天」决策首页（design docs/design/2026-10-09-n3-today-decision-home-design.md §1）：
// 一行早报 → 待我决策（前 5 张，其余进全局浮层）→ 并行中（T2 泳道）→ 贴底状态条。
// 数据与撤销窗口在 store/today.ts，与顶栏「待我决策」浮层共用。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import DecisionQueue from '../components/today/DecisionQueue.vue'
import TodayStatusBar from '../components/today/TodayStatusBar.vue'
import TodayLanes from '../components/today/TodayLanes.vue'
import FocusMode from '../components/today/FocusMode.vue'
import SnoozedDrawer from '../components/today/SnoozedDrawer.vue'
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
import { TOP_N, minutesText } from '../utils/today'

const nowSec = ref(Math.floor(Date.now() / 1000))
let clock: ReturnType<typeof setInterval> | null = null

const digest = computed(() => todayData.value?.digest)
const since = computed(() => digest.value?.since_last)
const oldest = computed(() => {
  const waits = visibleCards.value.map((c) => c.waiting_since).filter((t) => t > 0)
  return waits.length ? `最久 ${minutesText(nowSec.value - Math.min(...waits))}` : ''
})
function openOverlay(): void {
  overlayOpen.value = true
}

function openHandled(): void {
  handledOpen.value = true
}

// 专注处理（T3）：全屏一次一张；退出后回到首页。
const focusOpen = ref(false)
function openFocus(): void {
  if (visibleCards.value.length) focusOpen.value = true
}
const snoozedCount = computed(() => todayData.value?.snoozed ?? 0)

const emptyText = computed(() => `没有等你的事 · 今天处理了 ${handledTodayCount.value} 张`)

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
  <div class="today">
    <p v-if="todayError" class="today-err mono">{{ todayError }}</p>

    <details v-if="digest" class="digest" data-test="today-digest">
      <summary class="mono">
        自上次打开：完成 <b>{{ since?.jobs_done ?? 0 }}</b> ·
        <span :class="{ bad: (since?.jobs_failed ?? 0) > 0 }">失败 <b>{{ since?.jobs_failed ?? 0 }}</b></span> ·
        新提交 <b>{{ since?.commits ?? 0 }}</b>
      </summary>
      <div class="digest-full">
        <p class="digest-title mono">{{ digest.title }}</p>
        <p class="digest-text">{{ digest.text }}</p>
      </div>
    </details>

    <section class="sec" aria-labelledby="today-q">
      <div class="sh">
        <h2 id="today-q">待我决策</h2>
        <span v-if="visibleCards.length" class="count mono">{{ visibleCards.length }} 项</span>
        <span v-if="oldest" class="meta mono">{{ oldest }}</span>
        <label class="toggle mono">
          <input type="checkbox" :checked="includeExec" @change="setIncludeExec(($event.target as HTMLInputElement).checked)" />
          含 exec 待验收
        </label>
        <button
          class="focus-btn"
          type="button"
          data-test="open-focus"
          :disabled="!visibleCards.length"
          @click="openFocus"
        >专注处理</button>
      </div>
      <DecisionQueue
        :cards="visibleCards"
        :now-sec="nowSec"
        :limit="TOP_N"
        :empty-text="emptyText"
        @act="actOnCard"
        @snooze="snoozeCard"
        @more="openOverlay"
        @focus="openFocus"
      />
      <div class="foot mono">
        <button type="button" class="foot-link" data-test="open-snoozed" @click="snoozedOpen = true">已稍后 {{ snoozedCount }}</button>
        <button type="button" class="foot-link" data-test="open-handled" @click="openHandled">已处理</button>
      </div>
    </section>

    <TodayLanes />

    <TodayStatusBar v-if="todayData" :status="todayData.status" />

    <SnoozedDrawer />
    <FocusMode v-if="focusOpen" :now-sec="nowSec" @close="focusOpen = false" />
  </div>
</template>

<style scoped>
.today {
  max-width: 920px;
  margin: 0 auto;
  padding-bottom: 64px;
  min-width: 0;
}
.today-err {
  color: var(--fail);
  font-size: 12px;
}
.digest {
  margin-bottom: 16px;
  padding: 8px 12px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.digest summary {
  cursor: pointer;
  font-size: 12px;
  color: var(--queue);
}
.digest summary b {
  color: var(--paper);
}
.digest .bad,
.digest .bad b {
  color: var(--fail);
}
.digest-full {
  margin-top: 8px;
  font-size: 13px;
}
.digest-title {
  margin: 0 0 4px;
  color: var(--queue);
  font-size: 11px;
}
.digest-text {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.sec {
  margin-bottom: 22px;
}
.sh {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px 10px;
  margin-bottom: 8px;
}
.sh h2 {
  margin: 0;
  font-size: 15px;
}
.count {
  color: var(--run);
  font-size: 12px;
}
.meta {
  color: var(--queue);
  font-size: 11px;
}
.toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-left: auto;
  color: var(--queue);
  font-size: 11px;
}
.focus-btn {
  padding: 4px 12px;
  color: var(--ink);
  background: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  font-size: 12px;
  font-weight: 600;
}
.focus-btn:disabled {
  color: var(--queue);
  background: transparent;
  border-color: var(--line);
}
.foot {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 8px;
}
.foot-link {
  color: var(--queue);
  background: transparent;
  border: 0;
  font-size: 11px;
  text-decoration: underline;
}
.foot-link:hover {
  color: var(--phosphor);
}
@media (max-width: 640px) {
  .toggle {
    margin-left: 0;
  }
}
</style>
