<script setup lang="ts">
// 状态徽标：状态点(statusColor) + 状态文案(mono)。
import { computed } from 'vue'
import { statusColor } from '../api/client'
import type { JobStatus } from '../api/types'

const props = defineProps<{ status: JobStatus }>()

const dotColor = computed(() => statusColor(props.status))
// queued/cancelled 共用 --queue，cancelled 视觉上压暗以区分（rejected 也压暗：
// 与 cancelled 一样是"没交付"的终态，不该和 done/failed 抢注意力）。
const dim = computed(() => props.status === 'cancelled' || props.status === 'rejected')

// 文案映射：多数状态直接用原值，pending_interaction/needs_review 用「⚠ ...」提示
// （两者都是"等人"的信号，只是等的动作不同：答问题 vs 验收）。
const LABELS: Partial<Record<JobStatus, string>> = {
  pending_interaction: '⚠ 待应答',
  needs_review: '⚠ 待验收',
}
const label = computed(() => LABELS[props.status] ?? props.status)
// needs_review 同样脉冲：agent 干完了，现在轮到人。
const attention = computed(() => props.status === 'pending_interaction' || props.status === 'needs_review')
</script>

<template>
  <span class="badge mono" :class="{ 'badge--dim': dim, 'badge--attn': attention }">
    <span class="badge-dot" :style="{ background: dotColor }"></span>
    <span class="badge-text" :style="{ color: dotColor }">{{ label }}</span>
  </span>
</template>

<style scoped>
.badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  letter-spacing: 0.04em;
}
.badge-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
}
.badge--dim {
  opacity: 0.6;
}
/* pending_interaction：待应答，状态点轻脉冲提示 */
.badge--attn .badge-dot {
  animation: badge-attn 1.3s ease-in-out infinite;
}
@keyframes badge-attn {
  0%,
  100% {
    opacity: 1;
    transform: scale(1);
  }
  50% {
    opacity: 0.45;
    transform: scale(0.82);
  }
}
@media (prefers-reduced-motion: reduce) {
  .badge--attn .badge-dot {
    animation: none;
  }
}
</style>
