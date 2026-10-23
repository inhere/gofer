<script setup lang="ts">
// 仓库选择器（UI 重设计 P6，issue gofer-hez1）：仓库列表的下拉里每项显示
// 「项目 key / 仓库目录名」（主）与「tracker id · 路径末两级」（次），同一项目下的多个
// 仓库也能一眼区分。选中后的完整路径由 ui/TrackerRepoPath.vue 显示（换行 + 复制）。
//
// 受控组件：只发意图，取值仍由页面负责（/issues 与 /memory 两页共用）。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ChevronDown, ChevronUp } from '@lucide/vue'
import Icon from './Icon.vue'
import { t } from '../i18n'
import { trackerRepoLabel, trackerRepoOptions, trackerRepoTitle, type TrackerRepoLike } from '../utils/trackerView'

const props = defineProps<{
  modelValue: string
  repos: TrackerRepoLike[]
}>()

const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)
const menuId = `tracker-repo-${Math.random().toString(36).slice(2, 9)}`

const options = computed(() => trackerRepoOptions(props.repos))
const current = computed(() => props.repos.find((repo) => repo.tracker_id === props.modelValue) ?? null)
// chip 上只放主文字（项目 / 目录），完整路径在信息条与 title 里。
// 仓库列表还没拿到（或 ?tracker= 指向已删除的仓库）时退回 tracker id 本身，不丢信息。
const label = computed(() => (current.value ? trackerRepoTitle(current.value) : props.modelValue || t('util.repo.pick')))
const title = computed(() => (current.value ? trackerRepoLabel(current.value) : label.value))

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
  <div ref="root" class="trp" data-test="tracker-repo-picker" @keydown="onKeydown">
    <button
      class="trp-chip"
      type="button"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-controls="menuId"
      :title="title"
      data-test="tracker-repo-trigger"
      @click="open = !open"
    >
      <span class="trp-chip-label">{{ label }}</span>
      <Icon class="trp-caret" :icon="open ? ChevronUp : ChevronDown" size="sm" />
    </button>
    <!-- v-show 而不是 v-if：选项常驻 DOM（打开时不重建），测试也不必先模拟点击才看得到列表。 -->
    <div v-show="open" :id="menuId" class="trp-menu" role="listbox" :aria-label="t('util.repo.pick')">
      <button
        v-for="opt in options"
        :key="opt.value"
        class="trp-item"
        type="button"
        role="option"
        :aria-selected="opt.value === modelValue"
        :class="{ 'trp-item--active': opt.value === modelValue }"
        :title="opt.path"
        data-test="tracker-repo-option"
        :data-tracker="opt.value"
        @click="pick(opt.value)"
      >
        <span class="trp-item-title">{{ opt.title }}</span>
        <span class="trp-item-hint mono">{{ opt.hint }}</span>
      </button>
      <p v-if="options.length === 0" class="trp-empty">{{ t('empty.none') }}</p>
    </div>
  </div>
</template>

<style scoped>
.trp {
  position: relative;
  display: inline-flex;
  max-width: 100%;
}
.trp-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  min-height: 32px;
  max-width: 100%;
  padding: 2px var(--s2);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.trp-chip:hover {
  border-color: var(--accent);
}
.trp-chip:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.trp-chip-label {
  min-width: 0;
  max-width: 32ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.trp-caret {
  flex: none;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.trp-menu {
  position: absolute;
  z-index: 30;
  top: calc(100% + 4px);
  left: 0;
  min-width: 280px;
  max-width: min(90vw, 460px);
  max-height: 320px;
  overflow-y: auto;
  padding: var(--s1);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-panel);
  box-shadow: var(--shadow-pop);
}
.trp-item {
  display: flex;
  flex-direction: column;
  gap: 1px;
  width: 100%;
  padding: 6px var(--s2);
  background: none;
  border: 0;
  border-radius: var(--r-chip);
  color: var(--ink-2);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.trp-item:hover,
.trp-item:focus-visible {
  background: var(--surface);
  color: var(--ink);
  outline: 1px solid var(--focus);
}
.trp-item--active {
  color: var(--accent);
}
.trp-item-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.trp-item-hint {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.trp-empty {
  margin: var(--s1) var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
/* 手机：菜单贴在底部，拇指够得到（与 ProjectFilter / Sheet 同一形态） */
@media (max-width: 760px) {
  .trp-menu {
    position: fixed;
    left: var(--s2);
    right: var(--s2);
    bottom: var(--s2);
    top: auto;
    max-width: none;
    max-height: 60vh;
    border-radius: var(--r-sheet);
  }
  .trp-item {
    min-height: 40px;
  }
  .trp-chip {
    min-height: 40px;
  }
}
</style>
