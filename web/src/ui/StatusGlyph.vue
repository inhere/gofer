<script setup lang="ts">
// StatusGlyph：只画状态族的字形，不带文字。设计稿 §5。
// 替换现有的圆点/小方块表达；P0 只新增，不替换旧徽标。
import { computed } from 'vue'
import { statusFamily, statusFamilyClass, type StatusFamily } from '../utils/statusFamily'
import { glyphSpec } from './statusGlyph'
import { t } from '../i18n'

const props = withDefaults(
  defineProps<{
    // 接口里的状态值（job / todo / plan / 会话 …），按 utils/statusFamily 映射到族
    status?: string | null
    // 直接指定族：多义状态（如 pending）或只想显示族时用
    family?: StatusFamily
    size?: number
    // 无障碍名称；不给就用族名（status.family.<族>）
    label?: string
  }>(),
  { status: null, family: undefined, size: 14, label: undefined },
)

const fam = computed<StatusFamily>(() => props.family ?? statusFamily(props.status))
const spec = computed(() => glyphSpec(fam.value))
const ariaLabel = computed(() => props.label ?? t(spec.value.labelKey))
</script>

<template>
  <svg
    class="glyph"
    :class="[statusFamilyClass(fam), `glyph-${spec.kind}`]"
    :width="size"
    :height="size"
    viewBox="0 0 16 16"
    role="img"
    :aria-label="ariaLabel"
    :data-family="fam"
    :data-kind="spec.kind"
    data-testid="status-glyph"
  >
    <template v-if="spec.kind === 'dot-pulse'">
      <circle class="glyph-pulse" cx="8" cy="8" r="6.2" fill="currentColor" opacity=".45" />
      <circle cx="8" cy="8" r="3.6" fill="currentColor" />
    </template>
    <rect
      v-else-if="spec.kind === 'diamond'"
      x="4.4"
      y="4.4"
      width="7.2"
      height="7.2"
      rx="1"
      transform="rotate(45 8 8)"
      fill="currentColor"
    />
    <template v-else-if="spec.kind === 'check'">
      <circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path
        d="M5 8.3l2.1 2.1L11 6.2"
        fill="none"
        stroke="currentColor"
        stroke-width="1.7"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </template>
    <template v-else-if="spec.kind === 'cross'">
      <rect
        x="2.6"
        y="2.6"
        width="10.8"
        height="10.8"
        rx="3"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
      />
      <path
        d="M6 6l4 4M10 6l-4 4"
        fill="none"
        stroke="currentColor"
        stroke-width="1.6"
        stroke-linecap="round"
      />
    </template>
    <template v-else-if="spec.kind === 'clock'">
      <circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path
        d="M8 4.4V8l2.6 1.6"
        fill="none"
        stroke="currentColor"
        stroke-width="1.6"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </template>
    <circle
      v-else-if="spec.kind === 'ring'"
      cx="8"
      cy="8"
      r="6.2"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
    />
    <circle
      v-else
      cx="8"
      cy="8"
      r="6.2"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-dasharray="2.4 2.6"
    />
  </svg>
</template>
