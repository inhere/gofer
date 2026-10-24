<script setup lang="ts">
// 可搜索下拉（issue gofer-b8nh，设计稿 §6.4 / §8）：点开一个带搜索框的面板，
// 按项目 key / 显示名过滤（大小写不敏感、前缀优先），↑ ↓ 移动、Enter 选中、
// Esc 关闭并归还焦点。纯逻辑在 searchSelect.ts（filterOptions），键盘环绕复用 menuSelect.ts。
// 两种形态：chip（列表头的筛选，与 FilterChips 同高）与 field（表单字段，替代原生 <select>）。
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ChevronDown, ChevronUp, Search } from '@lucide/vue'
import Icon from './Icon.vue'
import { filterOptions, initialIndex, type SearchOption } from './searchSelect'
import { nextEnabledIndex, NO_INDEX } from './menuSelect'
import { t } from '../i18n'

const props = withDefaults(
  defineProps<{
    modelValue: string
    options: SearchOption[]
    /** 面板与搜索框的读屏名称 */
    label?: string
    /** 没有选中值时触发器上的文字 */
    placeholder?: string
    /** 搜索框的 placeholder（默认按 label 生成） */
    searchPlaceholder?: string
    /** 有选项但一个都没匹配上 */
    noMatchText?: string
    /** 一个选项都没有（如项目还没加载出来） */
    emptyText?: string
    variant?: 'chip' | 'field'
    disabled?: boolean
    loading?: boolean
    /** 出错时放在触发器的 title 上（列表头的项目筛选会带加载错误） */
    error?: string
    /** data-test 前缀：根节点是 testId，触发器是 testId-trigger */
    testId?: string
    /** 触发器的 id：表单里留着的 <label for> 还能指到它 */
    triggerId?: string
  }>(),
  {
    label: '',
    placeholder: '—',
    searchPlaceholder: '',
    noMatchText: '',
    emptyText: '',
    variant: 'chip',
    disabled: false,
    loading: false,
    error: '',
    testId: 'search-select',
    triggerId: '',
  },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'select', value: string): void
}>()

const open = ref(false)
const query = ref('')
const active = ref(NO_INDEX)
const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
const search = ref<HTMLInputElement | null>(null)
const itemEls = ref<HTMLButtonElement[]>([])
const menuId = `search-select-${Math.random().toString(36).slice(2, 9)}`

const filtered = computed(() => filterOptions(props.options, query.value))
const currentLabel = computed(() => {
  const hit = props.options.find((o) => o.value === props.modelValue)
  if (hit) return hit.label
  return props.modelValue || props.placeholder
})
const searchPlaceholderText = computed(
  () => props.searchPlaceholder || (props.label ? t('util.select.searchFor', { label: props.label }) : t('util.select.search')),
)
const noMatchText = computed(() => props.noMatchText || t('util.select.noMatch'))
const emptyText = computed(() => props.emptyText || t('empty.none'))

function show(): void {
  if (props.disabled) return
  open.value = true
  query.value = ''
  active.value = initialIndex(props.options, props.modelValue)
  void nextTick(() => {
    search.value?.focus()
    scrollActiveIntoView()
  })
}

function close(returnFocus = true): void {
  if (!open.value) return
  open.value = false
  active.value = NO_INDEX
  query.value = ''
  if (returnFocus) trigger.value?.focus()
}

function toggle(): void {
  if (open.value) close()
  else show()
}

function pick(option: SearchOption | undefined): void {
  if (!option || option.disabled) return
  close()
  if (option.value === props.modelValue) return
  emit('update:modelValue', option.value)
  emit('select', option.value)
}

function scrollActiveIntoView(): void {
  const el = itemEls.value[active.value]
  el?.scrollIntoView?.({ block: 'nearest' })
}

function step(delta: number): void {
  const next = nextEnabledIndex(filtered.value, active.value, delta)
  if (next === NO_INDEX) return
  active.value = next
  scrollActiveIntoView()
}

function onTriggerKeydown(e: KeyboardEvent): void {
  if (props.disabled) return
  if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    show()
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    show()
  }
}

function onSearchKeydown(e: KeyboardEvent): void {
  switch (e.key) {
    case 'Escape':
      e.preventDefault()
      e.stopPropagation()
      close()
      return
    case 'Tab':
      close(false)
      return
    case 'ArrowDown':
      e.preventDefault()
      step(1)
      return
    case 'ArrowUp':
      e.preventDefault()
      step(-1)
      return
    case 'Home':
      e.preventDefault()
      active.value = nextEnabledIndex(filtered.value, -1, 1)
      scrollActiveIntoView()
      return
    case 'End':
      e.preventDefault()
      active.value = nextEnabledIndex(filtered.value, 0, -1)
      scrollActiveIntoView()
      return
    case 'Enter':
      e.preventDefault()
      pick(filtered.value[active.value])
      return
  }
}

function onHover(index: number): void {
  active.value = index
}

function onDocPointerDown(e: PointerEvent): void {
  if (!open.value) return
  const el = root.value
  if (el && e.target instanceof Node && !el.contains(e.target)) close(false)
}

onMounted(() => document.addEventListener('pointerdown', onDocPointerDown))
onBeforeUnmount(() => document.removeEventListener('pointerdown', onDocPointerDown))
</script>

<template>
  <div ref="root" class="search-select" :class="`search-select--${variant}`" :data-test="testId">
    <button
      ref="trigger"
      :id="triggerId || undefined"
      type="button"
      class="ss-trigger"
      :class="{ 'ss-trigger--open': open }"
      :disabled="disabled"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-controls="menuId"
      :aria-label="label || undefined"
      :title="error || label || undefined"
      :data-test="`${testId}-trigger`"
      @click="toggle"
      @keydown="onTriggerKeydown"
    >
      <span class="ss-label">{{ currentLabel }}</span>
      <Icon class="ss-caret" :icon="open ? ChevronUp : ChevronDown" size="sm" />
    </button>
    <div v-if="open" class="ss-panel">
      <label class="ss-search">
        <Icon class="ss-search-icon" :icon="Search" size="sm" />
        <input
          ref="search"
          v-model="query"
          class="ss-input"
          type="text"
          role="combobox"
          autocomplete="off"
          spellcheck="false"
          :aria-label="searchPlaceholderText"
          :aria-controls="menuId"
          aria-expanded="true"
          :placeholder="searchPlaceholderText"
          :data-test="`${testId}-search`"
          @keydown="onSearchKeydown"
        />
      </label>
      <ul :id="menuId" class="ss-list" role="listbox" :aria-label="label || undefined">
        <li v-for="(option, index) in filtered" :key="option.value">
          <button
            :id="`${menuId}-opt-${index}`"
            ref="itemEls"
            type="button"
            role="option"
            class="ss-item"
            :class="{ 'ss-item--on': option.value === modelValue, 'ss-item--active': index === active }"
            :aria-selected="option.value === modelValue"
            :disabled="option.disabled"
            :title="option.hint || option.label"
            :tabindex="-1"
            :data-test="`${testId}-option`"
            @click="pick(option)"
            @pointermove="onHover(index)"
          >
            <span class="ss-check" aria-hidden="true">{{ option.value === modelValue ? '✓' : '' }}</span>
            <span class="ss-text">{{ option.label }}</span>
            <span v-if="option.hint" class="ss-hint">{{ option.hint }}</span>
          </button>
        </li>
      </ul>
      <p v-if="loading" class="ss-empty" data-test="search-select-loading">{{ t('hint.loading') }}</p>
      <p v-else-if="options.length === 0" class="ss-empty" data-test="search-select-empty">{{ emptyText }}</p>
      <p v-else-if="filtered.length === 0" class="ss-empty" data-test="search-select-no-match">{{ noMatchText }}</p>
    </div>
  </div>
</template>

<style scoped>
.search-select {
  position: relative;
  display: inline-flex;
  min-width: 0;
}
.search-select--field {
  display: flex;
  width: 100%;
}
.ss-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--s1);
  min-width: 0;
  max-width: 100%;
  min-height: 32px;
  padding: 2px var(--s2);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.search-select--field .ss-trigger {
  flex: 1;
  min-height: 34px;
  padding: 6px var(--s2);
  background: var(--surface-2);
  border-radius: var(--r-ctl);
}
.ss-trigger:hover:not(:disabled) {
  border-color: var(--accent);
}
.ss-trigger:focus-visible,
.ss-trigger--open {
  border-color: var(--accent);
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.ss-trigger:disabled {
  opacity: 0.6;
  cursor: default;
}
.ss-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ss-caret {
  flex: 0 0 auto;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.ss-panel {
  position: absolute;
  z-index: 30;
  top: calc(100% + 4px);
  left: 0;
  display: flex;
  flex-direction: column;
  min-width: 220px;
  max-width: min(360px, 90vw);
  max-height: 340px;
  padding: var(--s1);
  gap: var(--s1);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-panel);
  box-shadow: var(--shadow-pop);
}
.search-select--field .ss-panel {
  min-width: 100%;
}
.ss-search {
  display: flex;
  align-items: center;
  gap: var(--s1);
  padding: 0 var(--s2);
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-chip);
}
.ss-search-icon {
  flex: 0 0 auto;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.ss-input {
  flex: 1;
  min-width: 0;
  min-height: 30px;
  padding: 2px 0;
  background: none;
  border: 0;
  color: var(--ink);
  font: inherit;
  font-size: var(--fs-sm);
  outline: none;
}
.ss-list {
  min-height: 0;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;
}
.ss-item {
  display: flex;
  align-items: center;
  gap: var(--s1);
  width: 100%;
  min-height: 32px;
  padding: 4px var(--s2);
  background: none;
  border: 0;
  border-radius: var(--r-chip);
  color: var(--ink-2);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.ss-item:disabled {
  opacity: 0.5;
  cursor: default;
}
.ss-item--active {
  background: var(--surface);
  color: var(--ink);
  outline: 1px solid var(--focus);
}
.ss-item--on {
  color: var(--accent);
  font-weight: 500;
}
.ss-check {
  flex: 0 0 12px;
  color: var(--accent);
}
.ss-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ss-hint {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.ss-empty {
  margin: var(--s1) var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
/* 手机：面板贴在底部，拇指够得到（与 Sheet 同一形态） */
@media (max-width: 760px) {
  .ss-panel {
    position: fixed;
    left: var(--s2);
    right: var(--s2);
    bottom: var(--s2);
    top: auto;
    max-height: 60vh;
    max-width: none;
  }
  .ss-input {
    min-height: 38px;
    font-size: var(--fs-sm);
  }
  .ss-item {
    min-height: 40px;
  }
}
</style>
