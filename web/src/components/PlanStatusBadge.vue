<script setup lang="ts">
import { computed } from 'vue'
import type { PlanStatus } from '../api/types'

const props = defineProps<{ status: PlanStatus }>()

const dotClass = computed(() => `badge-dot badge-dot--${props.status}`)
const dim = computed(() => props.status === 'archived')
</script>

<template>
  <span class="badge mono" :class="{ 'badge--dim': dim }">
    <span :class="dotClass" aria-hidden="true"></span>
    <span class="badge-text">{{ status }}</span>
  </span>
</template>

<style scoped>
.badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--paper);
  font-size: 12px;
  letter-spacing: 0.04em;
}
.badge-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
  background: var(--queue);
}
.badge-dot--active {
  background: var(--phosphor);
}
.badge-dot--done {
  background: var(--done);
}
.badge-dot--archived {
  background: var(--queue);
}
.badge--dim {
  opacity: 0.6;
}
</style>
