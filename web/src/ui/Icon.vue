<script setup lang="ts">
// Icon：全站图标的唯一入口（issue gofer-h355）。
//
// 只管三件事：尺寸档位（icon.ts 的 sm/md/lg）、统一线宽 1.75、颜色跟随 currentColor
// （lucide 默认 stroke="currentColor"）。图标本体是 lucide 的函数组件，按需 import。
//
// 无障碍：给了 label 就是可读图标（role="img" + aria-label，可再配 title 显示悬停提示）；
// 不给 label 就是装饰性图标，lucide 会自动加 aria-hidden="true"。
// 纯图标按钮的 aria-label 与 title 写在按钮上（i18n 文案），不靠图标本身承载。
import { computed } from 'vue'
import type { LucideIcon } from '@lucide/vue'
import { ICON_STROKE, iconPx, type IconSize } from './icon'

const props = withDefaults(
  defineProps<{
    // lucide 图标组件（如 `Search`），或 ui/icon.ts 的导航图标表里的一项
    icon: LucideIcon
    size?: IconSize | number
    // 无障碍名称：给了才有 role="img"
    label?: string
    // 悬停提示（可读图标才需要）
    title?: string
    // 常驻旋转（运行中状态）
    spin?: boolean
  }>(),
  { size: 'md', label: undefined, title: undefined, spin: false },
)

const px = computed(() => iconPx(props.size))
</script>

<template>
  <component
    :is="icon"
    class="icon"
    :class="{ 'icon--spin': spin }"
    :size="px"
    :stroke-width="ICON_STROKE"
    :aria-hidden="label ? undefined : 'true'"
    :role="label ? 'img' : undefined"
    :aria-label="label"
    :title="title"
  />
</template>
