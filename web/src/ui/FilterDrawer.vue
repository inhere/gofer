<script setup lang="ts">
// 筛选抽屉（设计稿 §8）：筛选维度多的页面塞进底部抽屉，手机上尤其需要。只做外壳——
// 触发按钮 + Sheet，内容由调用方通过默认插槽提供。
import { computed } from 'vue'
import { t } from '../i18n'
import Sheet from './Sheet.vue'

const props = withDefaults(
  defineProps<{
    /** 已生效的筛选维度数，显示在触发按钮上（0 时不显示） */
    activeCount?: number
    label?: string
    /** 有生效的筛选时显示「清空过滤」 */
    clearable?: boolean
  }>(),
  { activeCount: 0, label: '', clearable: false },
)

const open = defineModel<boolean>('open', { default: false })

const emit = defineEmits<{ (e: 'clear'): void }>()

const buttonLabel = computed(() => {
  const base = props.label || t('act.moreFilters')
  return props.activeCount > 0 ? `${base}（${props.activeCount}）` : base
})
</script>

<template>
  <button
    class="fd-trigger"
    type="button"
    aria-haspopup="dialog"
    :aria-expanded="open"
    data-test="filter-drawer-trigger"
    @click="open = true"
  >
    {{ buttonLabel }}
  </button>
  <Sheet v-model="open" :label="label || t('act.moreFilters')">
    <div class="fd-body">
      <slot />
    </div>
    <div v-if="clearable" class="fd-foot">
      <button class="fd-clear" type="button" data-test="filter-drawer-clear" @click="emit('clear')">
        {{ t('act.clearFilters') }}
      </button>
      <button class="fd-done" type="button" @click="open = false">{{ t('act.close') }}</button>
    </div>
  </Sheet>
</template>

<style scoped>
.fd-trigger {
  min-height: 28px;
  padding: 2px var(--s2);
  background: transparent;
  color: var(--fg-2);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.fd-trigger:hover {
  color: var(--fg);
  border-color: var(--line-strong);
}
.fd-trigger:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.fd-body {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
}
.fd-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--s2);
  margin-top: var(--s4);
}
.fd-clear,
.fd-done {
  min-height: 36px;
  padding: 0 var(--s3);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.fd-clear {
  background: transparent;
  color: var(--fg-2);
  border: 1px solid var(--line-strong);
}
.fd-done {
  background: var(--accent);
  color: var(--accent-ink);
  border: 1px solid var(--accent);
}
@media (max-width: 760px) {
  .fd-trigger {
    min-height: 40px;
  }
}
</style>
