<script setup lang="ts">
// 筛选 chip 行（设计稿 §8）：桌面横排、手机在框内横向滑动。单选用于状态等互斥维度，
// 多选（multiple）用于计划的状态集合。计数徽标只在给了 count 时出现。
import { t } from '../i18n'
import { isChipSelected, nextChipValue, type ChipValue, type FilterChipItem } from './filterChips'

const props = withDefaults(
  defineProps<{
    items: FilterChipItem[]
    modelValue: ChipValue
    multiple?: boolean
    label?: string
  }>(),
  { multiple: false, label: '' },
)

const emit = defineEmits<{ (e: 'update:modelValue', value: ChipValue): void }>()

function selected(value: string): boolean {
  return isChipSelected(value, props.modelValue)
}

function pick(value: string): void {
  emit('update:modelValue', nextChipValue(value, props.modelValue, props.multiple))
}
</script>

<template>
  <div class="filter-chips" role="group" :aria-label="label || t('act.filter')" data-test="filter-chips">
    <button
      v-for="item in items"
      :key="item.value || '__all'"
      class="chip"
      type="button"
      :aria-pressed="selected(item.value)"
      :class="{ 'chip--on': selected(item.value) }"
      :title="item.title || item.label"
      :data-value="item.value"
      @click="pick(item.value)"
    >
      <span class="chip-label">{{ item.label }}</span>
      <span v-if="item.count !== undefined" class="chip-count">{{ item.count }}</span>
    </button>
  </div>
</template>

<style scoped>
/* 桌面横排；宽度不够时换行，而不是把页面撑出横向滚动条。 */
.filter-chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1);
  min-width: 0;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  min-height: 28px;
  padding: 2px var(--s2);
  background: transparent;
  color: var(--fg-3);
  border: 1px solid var(--line);
  border-radius: 999px;
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.chip:hover {
  color: var(--fg);
  border-color: var(--line-strong);
}
.chip:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.chip--on {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-soft);
}
.chip-label {
  white-space: nowrap;
}
.chip-count {
  min-width: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--surface-2);
  color: var(--fg-2);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
  text-align: center;
}
.chip--on .chip-count {
  color: var(--fg);
}
/* 手机：一行装不下时在框内横向滑动（不换行，不撑宽页面），点按目标 ≥40px。 */
@media (max-width: 760px) {
  .filter-chips {
    flex-wrap: nowrap;
    overflow-x: auto;
    scrollbar-width: none;
    -webkit-overflow-scrolling: touch;
    scroll-snap-type: x proximity;
    padding-bottom: 2px;
  }
  .filter-chips::-webkit-scrollbar {
    display: none;
  }
  .chip {
    flex: none;
    min-height: 40px;
    scroll-snap-align: start;
  }
}
</style>
