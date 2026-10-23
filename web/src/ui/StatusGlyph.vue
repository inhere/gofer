<script setup lang="ts">
// StatusGlyph：只画状态族的图标，不带文字。设计稿 §5（字形）+ issue gofer-h355（改用 lucide）。
//
// 族 → 图标在 ui/statusGlyph.ts；颜色跟随族的 --c（styles/status.css）。
// 运行中（run）常驻旋转，其余静止。
import { computed } from 'vue'
import { statusFamily, statusFamilyClass, type StatusFamily } from '../utils/statusFamily'
import { glyphSpec } from './statusGlyph'
import Icon from './Icon.vue'
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
  <Icon
    class="glyph"
    :class="[statusFamilyClass(fam), `glyph-${spec.kind}`]"
    :icon="spec.icon"
    :size="size"
    :spin="spec.spin"
    :label="ariaLabel"
    :data-family="fam"
    :data-kind="spec.kind"
    data-testid="status-glyph"
  />
</template>
