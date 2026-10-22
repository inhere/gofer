<script setup lang="ts">
// 分段进度条（设计稿 §8、§5）：每段一个状态族，旁边写「完成数/总数」与百分比。
// 不只靠颜色区分：每族有不同的纹理（实心 / 斜纹 / 点阵 / 交叉纹 / 虚底），图例带文字与计数。
import { computed } from 'vue'
import { t } from '../i18n'
import { percentOf, type ProgressFamily, type ProgressPart } from './progress'

const props = withDefaults(
  defineProps<{
    parts: ProgressPart[]
    /** 不传时按各段求和 */
    total?: number
    /** 不传时取 done 段 */
    done?: number
    percent?: number | null
    size?: 'sm' | 'md'
    /** 图例（文字 + 计数），默认显示 */
    legend?: boolean
    /** 屏幕阅读器用的整句说明；默认按段拼 */
    label?: string
  }>(),
  { total: undefined, done: undefined, percent: null, size: 'md', legend: true, label: '' },
)

const FAMILY_LABEL_KEY: Record<ProgressFamily, string> = {
  done: 'plans.progress.done',
  doing: 'plans.progress.doing',
  review: 'plans.progress.review',
  blocked: 'plans.progress.blocked',
  todo: 'plans.progress.todo',
}

const sum = computed(() => props.parts.reduce((n, part) => n + Math.max(0, part.count), 0))
const total = computed(() => props.total ?? sum.value)
const done = computed(() => props.done ?? (props.parts.find((p) => p.family === 'done')?.count ?? 0))
const percent = computed(() => props.percent ?? percentOf(done.value, total.value))

const visible = computed(() => props.parts.filter((part) => part.count > 0))

const segments = computed(() =>
  visible.value.map((part) => ({
    ...part,
    width: total.value > 0 ? `${(Math.max(0, part.count) / total.value) * 100}%` : '0%',
  })),
)

function familyLabel(family: ProgressFamily): string {
  return t(FAMILY_LABEL_KEY[family])
}

const summary = computed(() => {
  if (props.label) return props.label
  const frac = total.value > 0 ? `${done.value}/${total.value}` : '0/0'
  const pct = percent.value === null ? '' : `，${percent.value}%`
  const detail = visible.value.map((part) => `${familyLabel(part.family)} ${part.count}`).join('，')
  return `${frac}${pct}${detail ? `，${detail}` : ''}`
})
</script>

<template>
  <div class="progress" :class="[`progress--${size}`]" data-test="progress-bar">
    <div class="bar" role="img" :aria-label="summary" :data-total="total" :data-done="done">
      <span
        v-for="seg in segments"
        :key="seg.family"
        class="seg"
        :class="[`seg--${seg.family}`]"
        :data-family="seg.family"
        :data-count="seg.count"
        :style="{ width: seg.width }"
      ></span>
      <span v-if="segments.length === 0" class="seg seg--empty"></span>
    </div>
    <div class="meta">
      <span class="frac">{{ done }}/{{ total }}</span>
      <span v-if="percent !== null" class="pct">{{ percent }}%</span>
    </div>
    <div v-if="legend && visible.length" class="legend" data-test="progress-legend">
      <span v-for="part in visible" :key="part.family" class="legend-item" :data-family="part.family">
        <span class="swatch" :class="[`seg--${part.family}`]" aria-hidden="true"></span>
        <span class="legend-label">{{ familyLabel(part.family) }}</span>
        <span class="legend-count">{{ part.count }}</span>
      </span>
    </div>
  </div>
</template>

<style scoped>
.progress {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1) var(--s2);
  min-width: 0;
}
.bar {
  display: flex;
  flex: 1 1 120px;
  min-width: 80px;
  height: 6px;
  overflow: hidden;
  border-radius: 3px;
  background: var(--sunken);
  box-shadow: inset 0 0 0 1px var(--line);
}
.progress--sm .bar {
  height: 4px;
}
.seg {
  height: 100%;
  min-width: 0;
}
.seg--empty {
  width: 100%;
  background: var(--line);
}
/* 纹理让色觉不同的用户也能区分（§5、§10：状态不只靠颜色） */
.seg--done {
  background: var(--done);
}
.seg--doing {
  background: repeating-linear-gradient(
    45deg,
    var(--run) 0 3px,
    color-mix(in srgb, var(--run) 55%, transparent) 3px 6px
  );
}
.seg--review {
  background: repeating-radial-gradient(circle at 2px 2px, var(--need) 0 1px, transparent 1px 4px),
    var(--need-soft);
}
.seg--blocked {
  background: repeating-linear-gradient(
    -45deg,
    var(--fail) 0 2px,
    color-mix(in srgb, var(--fail) 50%, transparent) 2px 5px
  );
}
.seg--todo {
  background: var(--line-strong);
}
.meta {
  display: inline-flex;
  align-items: baseline;
  gap: var(--s1);
  color: var(--fg-2);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.frac {
  color: var(--fg);
}
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1) var(--s3);
  flex-basis: 100%;
}
.legend-item {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.swatch {
  width: 8px;
  height: 8px;
  border-radius: 2px;
}
.legend-count {
  color: var(--fg-2);
  font-variant-numeric: tabular-nums;
}
@media (max-width: 760px) {
  .legend {
    gap: var(--s1) var(--s2);
  }
}
</style>
