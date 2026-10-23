<script setup lang="ts">
// 下拉菜单（设计稿 §8：取代原生 <select>）：触发器一个按钮，面板 role=menu、
// 选项 role=menuitemradio（当前值带勾），键盘 ↑ ↓ 移动、Enter 选中、Esc 关闭并把焦点还回触发器。
// 纯逻辑在 menuSelect.ts（可单测），这里只管渲染与开关。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { menuIndex, nextEnabledIndex, NO_INDEX, type MenuOption } from './menuSelect'
import type { ButtonSize } from './button'

const props = withDefaults(
  defineProps<{
    modelValue: string
    options: MenuOption[]
    /** 面板的读屏名称 */
    label?: string
    /** 没有选中值时触发器上的文字 */
    placeholder?: string
    size?: ButtonSize
    disabled?: boolean
  }>(),
  { label: '', placeholder: '—', size: 'sm', disabled: false },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'select', value: string): void
}>()

const root = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
const itemEls = ref<HTMLButtonElement[]>([])
const open = ref(false)
const active = ref(NO_INDEX)

const current = computed(() => menuIndex(props.options, props.modelValue))
const currentLabel = computed(() => props.options.find((o) => o.value === props.modelValue)?.label ?? '')

function toggle(): void {
  if (props.disabled) return
  if (open.value) close()
  else show(current.value)
}

function show(from: number): void {
  open.value = true
  active.value = nextEnabledIndex(props.options, from, 1)
  void nextTick(() => itemEls.value[active.value]?.focus())
}

function close(returnFocus = true): void {
  if (!open.value) return
  open.value = false
  active.value = NO_INDEX
  if (returnFocus) trigger.value?.focus()
}

function pick(option: MenuOption): void {
  if (option.disabled) return
  close()
  if (option.value === props.modelValue) return
  emit('update:modelValue', option.value)
  emit('select', option.value)
}

function onTriggerKeydown(e: KeyboardEvent): void {
  if (props.disabled) return
  if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    show(current.value)
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    show(props.options.length - 1)
  }
}

function onMenuKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    e.preventDefault()
    close()
    return
  }
  if (e.key === 'Tab') {
    close(false)
    return
  }
  const delta = e.key === 'ArrowDown' ? 1 : e.key === 'ArrowUp' ? -1 : 0
  if (!delta) return
  e.preventDefault()
  const next = nextEnabledIndex(props.options, open.value ? active.value : current.value, delta)
  if (next === NO_INDEX) return
  active.value = next
  itemEls.value[next]?.focus()
}

function onDocPointerDown(e: PointerEvent): void {
  if (!open.value) return
  const el = root.value
  if (el && e.target instanceof Node && !el.contains(e.target)) close(false)
}

// 选项列表在打开时重建：清掉过期的元素引用，免得焦点跑到已经不存在的项上。
watch(
  () => props.options,
  () => {
    itemEls.value = []
  },
)

onMounted(() => document.addEventListener('pointerdown', onDocPointerDown))
onBeforeUnmount(() => document.removeEventListener('pointerdown', onDocPointerDown))
</script>

<template>
  <div ref="root" class="menu-select">
    <button
      ref="trigger"
      type="button"
      class="ms-trigger"
      :class="[`ms-trigger--${size}`]"
      :disabled="disabled"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-label="label || undefined"
      @click="toggle"
      @keydown="onTriggerKeydown"
    >
      <span class="ms-label">{{ currentLabel || placeholder }}</span>
      <span class="ms-caret" :class="{ 'ms-caret--up': open }" aria-hidden="true"></span>
    </button>
    <div v-if="open" class="ms-menu" role="menu" :aria-label="label || undefined" @keydown="onMenuKeydown">
      <button
        v-for="option in options"
        :key="option.value"
        ref="itemEls"
        type="button"
        role="menuitemradio"
        class="ms-item"
        :class="{ 'ms-item--on': option.value === modelValue }"
        :aria-checked="option.value === modelValue"
        :disabled="option.disabled"
        :title="option.title || undefined"
        :tabindex="option === options[active] ? 0 : -1"
        @click="pick(option)"
      >
        <span class="ms-check" aria-hidden="true">{{ option.value === modelValue ? '✓' : '' }}</span>
        <span class="ms-text">{{ option.label }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.menu-select {
  position: relative;
  display: inline-flex;
  min-width: 0;
}
/* 触发器与统一按钮同一套 token（ui/Button.vue）：次要按钮形态。 */
.ms-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--s1);
  min-width: 0;
  max-width: 100%;
  font: inherit;
  font-weight: 500;
  line-height: 1.2;
  color: var(--ink-2);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  white-space: nowrap;
  cursor: pointer;
}
.ms-trigger--sm {
  min-height: 28px;
  padding: 3px 10px;
  font-size: var(--fs-xs);
}
.ms-trigger--md {
  min-height: 34px;
  padding: 5px var(--s3);
  font-size: var(--fs-sm);
}
.ms-trigger:hover:not(:disabled) {
  color: var(--ink);
  border-color: var(--accent);
}
.ms-trigger:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.ms-trigger:disabled {
  opacity: 0.45;
  cursor: default;
}
.ms-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 箭头用边框画，不依赖图标字体（同 InfoCard 的 caret）。 */
.ms-caret {
  display: inline-block;
  width: 5px;
  height: 5px;
  margin: -3px 0 0 2px;
  border-right: 1.5px solid currentColor;
  border-bottom: 1.5px solid currentColor;
  transform: rotate(45deg);
}
.ms-caret--up {
  margin-top: 2px;
  transform: rotate(-135deg);
}
.ms-menu {
  position: absolute;
  z-index: 30;
  top: calc(100% + 4px);
  left: 0;
  min-width: 100%;
  max-height: 280px;
  overflow-y: auto;
  padding: 4px;
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-panel);
  box-shadow: var(--shadow-pop);
}
.ms-item {
  display: flex;
  align-items: center;
  gap: var(--s1);
  width: 100%;
  min-height: 32px;
  padding: 4px 8px;
  font: inherit;
  font-size: var(--fs-sm);
  color: var(--ink);
  text-align: left;
  background: transparent;
  border: 0;
  border-radius: var(--r-chip);
  white-space: nowrap;
  cursor: pointer;
}
.ms-item:hover:not(:disabled),
.ms-item:focus-visible {
  background: var(--accent-soft);
  color: var(--accent);
  outline: none;
}
.ms-item:disabled {
  opacity: 0.5;
  cursor: default;
}
.ms-check {
  width: 12px;
  color: var(--accent);
}
.ms-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
@media (max-width: 760px) {
  .ms-trigger--sm,
  .ms-trigger--md {
    min-height: 40px;
    font-size: var(--fs-sm);
  }
  .ms-item {
    min-height: 40px;
  }
}
</style>
