<script setup lang="ts">
// 列表头的项目筛选（设计稿 §6.4）：一个 chip 显示当前项目，点开是项目列表。
// 受控组件：值进 URL 由 useProjectFilter（ui/projectFilter.ts）负责，这里只发意图。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ChevronDown, ChevronUp } from '@lucide/vue'
import Icon from './Icon.vue'
import { t } from '../i18n'

const props = defineProps<{
  modelValue: string
  options: string[]
  loading?: boolean
  error?: string
}>()

const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)
const menuId = `project-filter-${Math.random().toString(36).slice(2, 9)}`

const label = computed(() => props.modelValue || t('field.allProjects'))

function pick(value: string): void {
  open.value = false
  emit('update:modelValue', value)
}

function onDocumentClick(event: MouseEvent): void {
  if (!open.value) return
  const el = root.value
  if (el && !el.contains(event.target as Node)) {
    open.value = false
  }
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && open.value) {
    event.stopPropagation()
    open.value = false
  }
}

onMounted(() => {
  document.addEventListener('click', onDocumentClick)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocumentClick)
})
</script>

<template>
  <div ref="root" class="project-filter" data-test="project-filter" @keydown="onKeydown">
    <button
      class="pf-chip"
      type="button"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-controls="menuId"
      :title="error || t('field.project')"
      data-test="project-filter-trigger"
      @click="open = !open"
    >
      <span class="pf-label">{{ label }}</span>
      <Icon class="pf-caret" :icon="open ? ChevronUp : ChevronDown" size="sm" />
    </button>
    <div v-if="open" :id="menuId" class="pf-menu" role="listbox" :aria-label="t('field.project')">
      <button
        class="pf-item"
        type="button"
        role="option"
        :aria-selected="modelValue === ''"
        :class="{ 'pf-item--active': modelValue === '' }"
        data-test="project-option-all"
        @click="pick('')"
      >
        {{ t('field.allProjects') }}
      </button>
      <button
        v-for="key in options"
        :key="key"
        class="pf-item"
        type="button"
        role="option"
        :aria-selected="modelValue === key"
        :class="{ 'pf-item--active': modelValue === key }"
        :title="key"
        @click="pick(key)"
      >
        {{ key }}
      </button>
      <p v-if="loading" class="pf-empty">{{ t('hint.loading') }}</p>
      <p v-else-if="options.length === 0" class="pf-empty">{{ t('empty.none') }}</p>
    </div>
  </div>
</template>

<style scoped>
.project-filter {
  position: relative;
  display: inline-flex;
}
.pf-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  min-height: 32px;
  padding: 2px var(--s2);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.pf-chip:hover {
  border-color: var(--accent);
}
.pf-chip:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.pf-label {
  max-width: 24ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pf-caret {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.pf-menu {
  position: absolute;
  z-index: 30;
  top: calc(100% + 4px);
  left: 0;
  min-width: 200px;
  max-height: 320px;
  overflow-y: auto;
  padding: var(--s1);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-panel);
  box-shadow: var(--shadow-pop);
}
.pf-item {
  display: block;
  width: 100%;
  padding: 6px var(--s2);
  background: none;
  border: 0;
  border-radius: var(--r-chip);
  color: var(--ink-2);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}
.pf-item:hover,
.pf-item:focus-visible {
  background: var(--surface);
  color: var(--ink);
  outline: 1px solid var(--focus);
}
.pf-item--active {
  color: var(--accent);
}
.pf-empty {
  margin: var(--s1) var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
/* 手机：菜单贴在底部，拇指够得到（与 Sheet 同一形态） */
@media (max-width: 760px) {
  .pf-menu {
    position: fixed;
    left: var(--s2);
    right: var(--s2);
    bottom: var(--s2);
    top: auto;
    max-height: 60vh;
    border-radius: var(--r-sheet);
  }
  .pf-item {
    min-height: 40px;
  }
}
</style>
