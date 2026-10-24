<script setup lang="ts">
// 列表头的项目筛选（设计稿 §6.4）：一个 chip 显示当前项目，点开是可搜索的项目列表。
// 受控组件：值进 URL 由 useProjectFilter（ui/projectFilter.ts）负责，这里只发意图。
// 下拉本体是共享的 ui/SearchSelect（与表单里的项目字段同一套过滤 / 键盘行为）。
import { computed } from 'vue'
import SearchSelect from './SearchSelect.vue'
import type { SearchOption } from './searchSelect'
import { t } from '../i18n'

const props = defineProps<{
  modelValue: string
  options: string[]
  loading?: boolean
  error?: string
}>()

const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

// 第一项是「全部项目」（值为空串，与 URL 里不带 ?project= 等价）。
const items = computed<SearchOption[]>(() => [
  { value: '', label: t('field.allProjects') },
  ...props.options.filter(Boolean).map((key) => ({ value: key, label: key })),
])
</script>

<template>
  <SearchSelect
    class="project-filter"
    test-id="project-filter"
    variant="chip"
    :model-value="modelValue"
    :options="items"
    :label="t('field.project')"
    :placeholder="t('field.allProjects')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('util.select.searchFor', { label: t('field.project') })"
    @update:model-value="emit('update:modelValue', $event)"
  />
</template>

<style scoped>
.project-filter {
  display: inline-flex;
}
</style>
