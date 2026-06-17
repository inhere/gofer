<script setup lang="ts">
// 签名元素「活信号行」：
//  - running：迷你波形（一组 CSS 动画竖条）。传 rate 时按速率调制活跃度；不传走通用轻动画（Board 行用）。
//  - 终态：静态横线 + 耗时文案。
//  - prefers-reduced-motion: reduce -> 冻结波形为静态条。
import { computed } from 'vue'
import { statusColor } from '../api/client'
import { fmtDuration } from '../api/time'
import type { JobStatus } from '../api/types'

const props = defineProps<{
  status: JobStatus
  // 行/秒，仅 JobDetail 传真实速率；Board 不传
  rate?: number
  // 终态耗时（秒），父级算好传入；running 时也可传当前运行时长用于 tooltip（不展示横线）
  durationSec?: number | null
}>()

const isRunning = computed(() => props.status === 'running')
const isQueued = computed(() => props.status === 'queued')

const color = computed(() => statusColor(props.status))

// 速率 -> 动画时长（行/秒越高，竖条跳动越快）。无 rate 时用通用轻动画速度。
const animDuration = computed(() => {
  if (props.rate == null || props.rate <= 0) {
    return '1.1s'
  }
  // rate 1 -> ~0.9s，rate 高 -> 最快 0.28s
  const d = Math.max(0.28, 0.9 / Math.max(1, props.rate))
  return `${d.toFixed(2)}s`
})

// 一组竖条，错相位
const bars = [0, 1, 2, 3, 4]
function barDelay(i: number): string {
  return `${i * 0.12}s`
}

const durationText = computed(() => fmtDuration(props.durationSec ?? null))
</script>

<template>
  <span class="signal" :style="{ '--sig-color': color, '--sig-dur': animDuration }">
    <!-- running：跳动波形 -->
    <span v-if="isRunning" class="wave" aria-label="running">
      <span
        v-for="i in bars"
        :key="i"
        class="bar"
        :style="{ animationDelay: barDelay(i) }"
      ></span>
    </span>
    <!-- queued：等待，静态点阵（轻提示，不算终态） -->
    <span v-else-if="isQueued" class="queued" aria-label="queued">
      <span class="dot"></span><span class="dot"></span><span class="dot"></span>
    </span>
    <!-- 终态：静态横线 + 耗时 -->
    <span v-else class="ended" aria-label="ended">
      <span class="line"></span>
      <span class="dur mono">{{ durationText }}</span>
    </span>
  </span>
</template>

<style scoped>
.signal {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  height: 14px;
}

/* running 波形 */
.wave {
  display: inline-flex;
  align-items: flex-end;
  gap: 2px;
  height: 14px;
}
.bar {
  width: 2px;
  height: 4px;
  background: var(--sig-color);
  border-radius: 1px;
  transform-origin: bottom;
  animation: sig-bounce var(--sig-dur) ease-in-out infinite;
}
@keyframes sig-bounce {
  0%,
  100% {
    height: 3px;
    opacity: 0.55;
  }
  50% {
    height: 14px;
    opacity: 1;
  }
}

/* queued 静态点 */
.queued {
  display: inline-flex;
  align-items: center;
  gap: 3px;
}
.queued .dot {
  width: 3px;
  height: 3px;
  border-radius: 50%;
  background: var(--sig-color);
  opacity: 0.7;
}

/* 终态横线 + 耗时 */
.ended {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.ended .line {
  width: 22px;
  height: 2px;
  background: var(--sig-color);
  opacity: 0.8;
  border-radius: 1px;
}
.ended .dur {
  font-size: 11px;
  color: var(--queue);
}

/* reduced-motion：冻结波形为静态条 */
@media (prefers-reduced-motion: reduce) {
  .bar {
    animation: none;
    height: 8px;
    opacity: 0.8;
  }
}
</style>
