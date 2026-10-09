<script setup lang="ts">
// N3「专注处理」（T3，design §4）：全屏一次一张，处理后自动下一张；顶部「3 / 11」与细进度条。
// 键位见 utils/todayFocus.ts 的 focusCommand（z 撤销走全局监听）；手机左右滑切换。
// 不强迫清空：跳过（s）只在本轮生效，跳过和稍后的卡都留在队列里。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import DecisionCard from './DecisionCard.vue'
import type { TodayAction, TodayCard } from '../../api/today'
import { actOnCard, handledOpen, overlayOpen, snoozeCard, snoozedOpen, visibleCards } from '../../store/today'
import { isTypingTarget } from '../../utils/today'
import { focusCommand, focusProgress, swipeCommand, type FocusCommand } from '../../utils/todayFocus'
import type { SnoozeOption } from '../../utils/todaySnooze'

defineProps<{ nowSec: number }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const skipped = ref<Set<string>>(new Set())
const seen = ref<Set<string>>(new Set())
// 本轮做过操作的卡 → 是否按建议；撤销 / 失败后卡回到队列，就不再算「处理」。
const acted = ref<Map<string, boolean>>(new Map())
const index = ref(0)
const cardRef = ref<{
  runAction(i: number): boolean
  approve(): boolean
  openReply(): boolean
  toggleInfo(): void
  toggleSnooze(): void
  dismiss(): boolean
} | null>(null)
const root = ref<HTMLElement | null>(null)

const queue = computed(() => visibleCards.value.filter((c) => !skipped.value.has(c.key)))
const current = computed<TodayCard | null>(() => queue.value[Math.min(index.value, queue.value.length - 1)] ?? null)
const progress = computed(() => focusProgress(seen.value.size, queue.value.length, index.value))
const visibleKeys = computed(() => new Set(visibleCards.value.map((c) => c.key)))
const doneCount = computed(() => [...acted.value.keys()].filter((k) => !visibleKeys.value.has(k)).length)
const adviceCount = computed(() => [...acted.value].filter(([k, adv]) => adv && !visibleKeys.value.has(k)).length)
const leftCount = computed(() => visibleCards.value.length)

watch(
  queue,
  (q) => {
    const next = new Set(seen.value)
    q.forEach((c) => next.add(c.key))
    if (next.size !== seen.value.size) seen.value = next
    if (index.value > Math.max(0, q.length - 1)) index.value = Math.max(0, q.length - 1)
  },
  { immediate: true },
)

function onAct(card: TodayCard, action: TodayAction, text: string, viaAdvice: boolean): void {
  acted.value = new Map(acted.value).set(card.key, viaAdvice)
  actOnCard(card, action, text, viaAdvice)
}

function onSnooze(card: TodayCard, opt: SnoozeOption): void {
  snoozeCard(card, opt)
}

function run(cmd: FocusCommand): void {
  if (cmd.kind === 'exit') {
    if (cardRef.value?.dismiss()) return
    emit('close')
    return
  }
  const c = current.value
  if (!c) return
  switch (cmd.kind) {
    case 'next':
      index.value = Math.min(index.value + 1, queue.value.length - 1)
      break
    case 'prev':
      index.value = Math.max(index.value - 1, 0)
      break
    case 'skip':
      skipped.value = new Set(skipped.value).add(c.key)
      break
    case 'action':
      cardRef.value?.runAction(cmd.index)
      break
    case 'approve':
      cardRef.value?.approve()
      break
    case 'reply':
      cardRef.value?.openReply()
      break
    case 'info':
      cardRef.value?.toggleInfo()
      break
    case 'snooze':
      cardRef.value?.toggleSnooze()
      break
  }
}

function onKey(ev: KeyboardEvent): void {
  // 浮层 / 抽屉盖在上面时让给它们。
  if (overlayOpen.value || handledOpen.value || snoozedOpen.value) return
  const cmd = focusCommand(ev.key, {
    typing: isTypingTarget(ev.target),
    modifier: ev.ctrlKey || ev.metaKey || ev.altKey,
  })
  if (!cmd) return
  ev.preventDefault()
  run(cmd)
}

let touchX = 0
let touchY = 0
function onTouchStart(ev: TouchEvent): void {
  const t = ev.changedTouches[0]
  if (!t) return
  touchX = t.clientX
  touchY = t.clientY
}
function onTouchEnd(ev: TouchEvent): void {
  const t = ev.changedTouches[0]
  if (!t || isTypingTarget(ev.target)) return
  const cmd = swipeCommand(t.clientX - touchX, t.clientY - touchY)
  if (cmd) run(cmd)
}

onMounted(() => {
  window.addEventListener('keydown', onKey)
  root.value?.focus()
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div ref="root" class="fm" role="dialog" aria-modal="true" aria-label="专注处理" tabindex="-1" data-test="focus-mode">
    <header class="fm-head mono">
      <span v-if="current" class="fm-pos" data-test="focus-pos">{{ progress.pos }} / {{ progress.total }}</span>
      <span v-if="skipped.size" class="fm-skip">跳过 {{ skipped.size }}</span>
      <button class="fm-x" type="button" data-test="focus-exit" @click="emit('close')">退出 Esc</button>
    </header>
    <div class="fm-bar" aria-hidden="true"><i :style="{ width: `${progress.percent}%` }"></i></div>
    <main class="fm-stage" @touchstart.passive="onTouchStart" @touchend="onTouchEnd">
      <DecisionCard
        v-if="current"
        :key="current.key"
        ref="cardRef"
        :card="current"
        :now-sec="nowSec"
        @act="(a, text, adv) => onAct(current!, a, text, adv)"
        @snooze="(o) => onSnooze(current!, o)"
        @navigate="emit('close')"
      />
      <div v-else class="fm-done" data-test="focus-done">
        <h3>全部处理完</h3>
        <p class="mono">本轮处理 {{ doneCount }} 张 · 按建议 {{ adviceCount }} 张</p>
        <p v-if="leftCount" class="mono">跳过和稍后的卡还在队列里</p>
        <button class="fm-back" type="button" data-test="focus-back" @click="emit('close')">回到首页</button>
      </div>
      <nav v-if="current" class="fm-nav mono">
        <button type="button" :disabled="index <= 0" @click="run({ kind: 'prev' })">上一张</button>
        <button type="button" @click="run({ kind: 'skip' })">跳过</button>
        <button type="button" :disabled="index >= queue.length - 1" @click="run({ kind: 'next' })">下一张</button>
      </nav>
    </main>
    <footer class="fm-keys mono">
      <span><kbd>j</kbd>/<kbd>k</kbd> 切换</span>
      <span><kbd>1-9</kbd> 操作</span>
      <span><kbd>a</kbd> 通过/采纳</span>
      <span><kbd>r</kbd> 回复</span>
      <span><kbd>i</kbd> 详情</span>
      <span><kbd>h</kbd> 稍后</span>
      <span><kbd>s</kbd> 跳过</span>
      <span><kbd>z</kbd> 撤销</span>
    </footer>
    <footer class="fm-swipe mono">左右滑切换</footer>
  </div>
</template>

<style scoped>
.fm {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: grid;
  grid-template-rows: auto auto 1fr auto;
  padding-inline: 16px;
  overflow-x: hidden;
  overflow-y: auto;
  background: var(--ink);
  outline: none;
}
.fm-head,
.fm-keys,
.fm-swipe {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  width: 100%;
  max-width: 720px;
  min-width: 0;
  margin: 0 auto;
  padding-block: 14px;
  color: var(--queue);
  font-size: 12px;
}
.fm-pos {
  color: var(--paper);
}
.fm-x {
  margin-left: auto;
  padding: 3px 8px;
  color: var(--queue);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  font-size: 12px;
}
.fm-bar {
  width: 100%;
  max-width: 720px;
  height: 2px;
  margin: 0 auto;
  background: var(--line);
}
.fm-bar i {
  display: block;
  height: 100%;
  background: var(--phosphor);
  transition: width 0.2s;
}
.fm-stage {
  display: flex;
  flex-direction: column;
  gap: 12px;
  width: 100%;
  max-width: 720px;
  min-width: 0;
  margin: 0 auto;
  align-self: center;
  touch-action: pan-y;
}
.fm-stage :deep(.dc-title) {
  font-size: 18px;
}
.fm-stage :deep(.dc-sum) {
  white-space: normal;
}
.fm-nav {
  display: flex;
  justify-content: center;
  gap: 8px;
  font-size: 12px;
}
.fm-nav button {
  padding: 4px 10px;
  color: var(--queue);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.fm-nav button:disabled {
  opacity: 0.4;
}
.fm-done {
  display: grid;
  justify-items: center;
  gap: 10px;
  text-align: center;
}
.fm-done h3 {
  margin: 0;
  font-size: 20px;
}
.fm-done p {
  margin: 0;
  color: var(--queue);
  font-size: 13px;
}
.fm-back {
  padding: 6px 14px;
  color: var(--ink);
  background: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  font-weight: 600;
}
kbd {
  padding: 0 5px;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: 2px;
  font-family: inherit;
}
.fm-swipe {
  display: none;
}
@media (max-width: 640px) {
  .fm-keys {
    display: none;
  }
  .fm-swipe {
    display: flex;
    justify-content: center;
  }
}
@media (prefers-reduced-motion: reduce) {
  .fm-bar i {
    transition: none;
  }
}
</style>
