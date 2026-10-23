<script setup lang="ts">
// 状态徽标：状态点 + 状态文案(mono)。颜色走状态族：类名 .k-<族> 给出 --c（见 styles/status.css），
// 本组件不再自带「状态 → 颜色」表。
// 文案取 utils/statusText.ts 的中央映射（各页面、筛选芯片同一份，见该文件头注释）。
import { computed } from 'vue'
import type { JobStatus } from '../api/types'
import { t } from '../i18n'
import { statusFamily, statusFamilyClass } from '../utils/statusFamily'
import { statusText } from '../utils/statusText'

const props = defineProps<{ status: JobStatus; holder?: string }>()

const familyClass = computed(() => statusFamilyClass(statusFamily(props.status)))
// queued/cancelled 同为「没在跑」的灰色族，cancelled 视觉上压暗以区分（rejected 也压暗：
// 与 cancelled 一样是"没交付"的终态，不该和 done/failed 抢注意力）。
const dim = computed(() => props.status === 'cancelled' || props.status === 'rejected')

// 文案：多数状态在中央映射里有中文；pending_interaction/needs_review 等带「⚠ ...」提示
// （两者都是"等人"的信号，只是等的动作不同：答问题 vs 验收）。
const label = computed(() => statusText(props.status))
// needs_review 同样脉冲：agent 干完了，现在轮到人；awaiting_approval（待批准）也是在等人点头。
const attention = computed(
  () =>
    props.status === 'pending_interaction' ||
    props.status === 'needs_review' ||
    props.status === 'awaiting_input' ||
    props.status === 'awaiting_approval',
)
// waiting_dir 的悬停提示点名持有目录锁的 job：芯片只有两三个字，说不清"在等谁"。
const title = computed(() =>
  props.status === 'waiting_dir' && props.holder ? t('util.statusBadge.holder', { holder: props.holder }) : undefined,
)
</script>

<template>
  <span
    class="badge mono"
    :class="[familyClass, { 'badge--dim': dim, 'badge--attn': attention }]"
    :title="title"
  >
    <span class="badge-dot"></span>
    <span class="badge-text">{{ label }}</span>
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
  background: var(--c);
}
.badge-text {
  color: var(--c);
}
/* need 族在浅色主题下万寿菊做正文色对比不足，文字用 --need-ink（形状/点仍是 --need）。 */
.badge.k-need .badge-text {
  color: var(--need-ink);
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
