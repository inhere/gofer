<script setup lang="ts">
// 全局「待我决策」浮层（design §1 / §6）：任何页面点顶栏或按 g d 打开，复用同一队列组件，
// 处理完不离开当前页；点卡片标题 / 看 diff 等跳转时自动关闭。
import { onMounted, onUnmounted, ref, watch } from 'vue'
import DecisionQueue from './DecisionQueue.vue'
import {
  actOnCard,
  handledOpen,
  includeExec,
  overlayOpen,
  refreshToday,
  setIncludeExec,
  snoozeCard,
  visibleCards,
} from '../../store/today'

const nowSec = ref(Math.floor(Date.now() / 1000))
let timer: ReturnType<typeof setInterval> | null = null

onMounted(() => {
  timer = setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 30_000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})

watch(overlayOpen, (open) => {
  if (open) {
    nowSec.value = Math.floor(Date.now() / 1000)
    void refreshToday()
  }
})

function close(): void {
  overlayOpen.value = false
}

function openHandled(): void {
  overlayOpen.value = false
  handledOpen.value = true
}
</script>

<template>
  <div v-if="overlayOpen" class="ov-scrim" aria-hidden="true" @click="close"></div>
  <aside v-if="overlayOpen" class="ov" role="dialog" aria-label="待我决策" data-test="decision-overlay">
    <header class="ov-head">
      <span class="ov-title">待我决策 <span class="ov-n mono">{{ visibleCards.length }}</span></span>
      <span class="ov-hint mono">任何页面按 g d</span>
      <button class="ov-x mono" type="button" @click="close">关闭</button>
    </header>
    <div class="ov-tools mono">
      <label class="ov-toggle">
        <input type="checkbox" :checked="includeExec" @change="setIncludeExec(($event.target as HTMLInputElement).checked)" />
        含 exec 待验收
      </label>
      <button class="ov-link" type="button" @click="openHandled">已处理</button>
    </div>
    <div class="ov-body">
      <DecisionQueue
        :cards="visibleCards"
        :now-sec="nowSec"
        @act="actOnCard"
        @snooze="snoozeCard"
        @navigate="close"
      />
    </div>
  </aside>
</template>

<style scoped>
.ov-scrim {
  position: fixed;
  inset: 0;
  z-index: 64;
  background: rgba(0, 0, 0, 0.35);
}
.ov {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 65;
  display: flex;
  flex-direction: column;
  width: min(480px, 100vw);
  background: var(--ink);
  border-left: 1px solid var(--line);
  box-shadow: -12px 0 30px rgba(0, 0, 0, 0.35);
}
.ov-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--line);
  background: var(--panel);
}
.ov-title {
  font-weight: 600;
}
.ov-n {
  color: var(--run);
}
.ov-hint {
  flex: 1;
  color: var(--queue);
  font-size: 11px;
}
.ov-x,
.ov-link {
  color: var(--queue);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
}
.ov-tools {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 8px 14px;
  color: var(--queue);
  font-size: 12px;
}
.ov-toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.ov-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 4px 14px 80px;
}
</style>
