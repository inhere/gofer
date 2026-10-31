<script setup lang="ts">
// StatusPill：状态族的字形 + 文字，可带底色（设计稿 §5）。已取代旧的 StatusBadge /
// PlanStatusBadge / InfoCard 的 .sbadge / statusPresentation 的圆点；页面里的状态徽标都用它。
//
// 文字优先级：label > status.<状态值>（i18n）> 原样显示状态值（后端新增的状态不会变成空白）
// > 无状态值时用 status.family.<族>。
// 悬停提示等其余属性（title、data-test …）直接落在根元素上。
import { computed } from 'vue'
import {
  statusFamily,
  statusFamilyClass,
  type StatusFamily,
} from '../utils/statusFamily'
import { t, te } from '../i18n'
import StatusGlyph from './StatusGlyph.vue'

const props = withDefaults(
  defineProps<{
    status?: string | null
    family?: StatusFamily
    // 自定义文案（业务内容或已翻译好的文字）
    label?: string
    size?: 'sm' | 'md'
    // 只显示字形（表格里空间紧时用），无障碍名称仍保留
    glyphOnly?: boolean
  }>(),
  { status: null, family: undefined, label: undefined, size: 'sm', glyphOnly: false },
)

const fam = computed<StatusFamily>(() => props.family ?? statusFamily(props.status))

const text = computed<string>(() => {
  if (props.label) return props.label
  const status = props.status
  if (!status) return t(`status.family.${fam.value}`)
  const key = `status.${status}`
  return te(key) ? t(key) : status
})
</script>

<template>
  <span
    class="pill"
    :class="[statusFamilyClass(fam), { 'pill-lg': size === 'md' }]"
    :data-status="status ?? undefined"
    data-testid="status-pill"
  >
    <StatusGlyph :status="status" :family="fam" :label="text" :size="size === 'md' ? 15 : 13" />
    <span v-if="!glyphOnly" class="pill-text">{{ text }}</span>
  </span>
</template>
