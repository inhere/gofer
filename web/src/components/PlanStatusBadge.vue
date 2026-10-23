<script setup lang="ts">
// plan 状态徽标：点颜色按状态，文案取中央映射（utils/statusText.ts），接口值不变。
import { computed } from 'vue'
import type { PlanStatus } from '../api/types'
import { planStatusText } from '../utils/statusText'

const props = defineProps<{ status: PlanStatus }>()

const dotClass = computed(() => `badge-dot badge-dot--${props.status}`)
const dim = computed(() => props.status === 'archived')
const label = computed(() => planStatusText(props.status))
</script>

<template>
  <span class="badge mono" :class="{ 'badge--dim': dim }">
    <span :class="dotClass" aria-hidden="true"></span>
    <span class="badge-text">{{ label }}</span>
  </span>
</template>

<style scoped>
.badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--ink);
  font-size: 12px;
  letter-spacing: 0.04em;
}
.badge-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
  background: var(--ink-3);
}
.badge-dot--open {
  background: var(--run);
}
.badge-dot--done {
  background: var(--done);
}
/* blocked：链失败等人处理——与 failed 同色（它是"需要人"的信号，不是终态）。 */
.badge-dot--blocked {
  background: var(--fail);
}
.badge-dot--archived {
  background: var(--ink-3);
}
.badge--dim {
  opacity: 0.6;
}
</style>
