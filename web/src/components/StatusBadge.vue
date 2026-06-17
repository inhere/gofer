<script setup lang="ts">
// 状态徽标：状态点(statusColor) + 状态文案(mono)。
import { computed } from 'vue'
import { statusColor } from '../api/client'
import type { JobStatus } from '../api/types'

const props = defineProps<{ status: JobStatus }>()

const dotColor = computed(() => statusColor(props.status))
// queued/cancelled 共用 --queue，cancelled 视觉上压暗以区分
const dim = computed(() => props.status === 'cancelled')
</script>

<template>
  <span class="badge mono" :class="{ 'badge--dim': dim }">
    <span class="badge-dot" :style="{ background: dotColor }"></span>
    <span class="badge-text" :style="{ color: dotColor }">{{ status }}</span>
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
</style>
