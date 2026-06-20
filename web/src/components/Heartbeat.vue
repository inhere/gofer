<script setup lang="ts">
// 签名元素「心跳脉冲」：扩展 Signal.vue 的波形语言，专用于 worker 行的活体感。
//  - connected（且不过期）：phosphor 跳动脉冲（一组 CSS 动画竖条，错相位）。
//  - stale（心跳过期 age > ~2×ping，约 >30s）：amber/减速脉冲（提示心跳变慢）。
//  - flatline（disconnected / unknown）：静态横线（--fail / --queue），不跳动。
//  - prefers-reduced-motion: reduce -> 冻结脉冲为静态条（全局 tokens 也兜底）。
// 仅 worker 使用本组件；peer-http/local 用静态点（见 Runners.vue），故舰队“可见地在跳”。
import { computed } from 'vue'

type Beat = 'connected' | 'stale' | 'flatline'

const props = defineProps<{
  // 心跳态：由父级按 status + heartbeat_age_ms 计算
  beat: Beat
  // 无障碍文案（如 "connected" / "no heartbeat 41s" / "offline"）
  label?: string
}>()

const isBeating = computed(() => props.beat === 'connected' || props.beat === 'stale')

// 心跳态 -> 颜色 token：connected=phosphor，stale=amber(run)，flatline=fail。
const color = computed(() => {
  if (props.beat === 'connected') {
    return 'var(--phosphor)'
  }
  if (props.beat === 'stale') {
    return 'var(--run)'
  }
  return 'var(--fail)'
})

// stale 时脉冲减速（更慢、更弱的“喘息”），connected 稳定常速。
const animDuration = computed(() => (props.beat === 'stale' ? '2.2s' : '1.2s'))

// 一组竖条，错相位（与 Signal 的 wave 一致的语言，但更克制）
const bars = [0, 1, 2, 3, 4]
function barDelay(i: number): string {
  return `${i * 0.13}s`
}

const ariaLabel = computed(() => props.label ?? props.beat)
</script>

<template>
  <span
    class="heartbeat"
    :style="{ '--hb-color': color, '--hb-dur': animDuration }"
    role="img"
    :aria-label="ariaLabel"
  >
    <!-- connected / stale：跳动脉冲 -->
    <span v-if="isBeating" class="pulse" :class="{ 'pulse--stale': beat === 'stale' }">
      <span
        v-for="i in bars"
        :key="i"
        class="bar"
        :style="{ animationDelay: barDelay(i) }"
      ></span>
    </span>
    <!-- flatline：静态横线（断连） -->
    <span v-else class="flatline"></span>
  </span>
</template>

<style scoped>
.heartbeat {
  display: inline-flex;
  align-items: center;
  height: 14px;
}

/* 跳动脉冲 */
.pulse {
  display: inline-flex;
  align-items: flex-end;
  gap: 2px;
  height: 14px;
}
.bar {
  width: 2px;
  height: 3px;
  background: var(--hb-color);
  border-radius: 1px;
  transform-origin: bottom;
  animation: hb-beat var(--hb-dur) ease-in-out infinite;
}
@keyframes hb-beat {
  0%,
  100% {
    height: 3px;
    opacity: 0.5;
  }
  50% {
    height: 14px;
    opacity: 1;
  }
}
/* stale：脉冲幅度更低、更暗，传达“喘”的感觉 */
.pulse--stale .bar {
  opacity: 0.7;
}
.pulse--stale .bar {
  animation-name: hb-beat-stale;
}
@keyframes hb-beat-stale {
  0%,
  100% {
    height: 3px;
    opacity: 0.4;
  }
  50% {
    height: 9px;
    opacity: 0.85;
  }
}

/* flatline：静态横线（断连），不跳动 */
.flatline {
  display: inline-block;
  width: 24px;
  height: 2px;
  background: var(--hb-color);
  opacity: 0.85;
  border-radius: 1px;
}

/* reduced-motion：冻结脉冲为静态条 */
@media (prefers-reduced-motion: reduce) {
  .bar {
    animation: none;
    height: 8px;
    opacity: 0.8;
  }
}
</style>
