<script setup lang="ts">
// 页签（设计稿 §8、§10）：值以 URL 的 ?tab= 为准，可深链、可前进后退；键盘可达：
// ← → 在页签间移动并立刻切换，Home / End 到首尾。内容由默认插槽以 active 渲染。
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { t } from '../i18n'
import { queryWithTab, tabFromQuery, type TabItem } from './tabs'

const props = withDefaults(
  defineProps<{
    tabs: TabItem[]
    /** URL 参数名，默认 tab */
    param?: string
    /** 没有 ?tab= 时用哪个页签（默认第一个） */
    fallback?: string
    label?: string
  }>(),
  { param: 'tab', fallback: '', label: '' },
)

const emit = defineEmits<{ (e: 'change', value: string): void }>()

const route = useRoute()
const router = useRouter()
const buttons = ref<HTMLButtonElement[]>([])

const active = computed(() => tabFromQuery(route.query, props.tabs, props.param, props.fallback))

function select(value: string): void {
  if (!value) return
  emit('change', value)
  void router.replace({ path: route.path, query: queryWithTab(route.query, value, props.tabs, props.param, props.fallback) })
}

function onKeydown(event: KeyboardEvent, index: number): void {
  const total = props.tabs.length
  if (total === 0) return
  let next: number | null = null
  if (event.key === 'ArrowRight') next = (index + 1) % total
  else if (event.key === 'ArrowLeft') next = (index - 1 + total) % total
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = total - 1
  if (next === null) return
  event.preventDefault()
  select(props.tabs[next].value)
  buttons.value[next]?.focus()
}
</script>

<template>
  <div class="tabs" role="tablist" :aria-label="label || t('act.filter')" data-test="tabs">
    <button
      v-for="(tab, index) in tabs"
      :key="tab.value"
      ref="buttons"
      class="tab"
      type="button"
      role="tab"
      :id="`tab-${param}-${tab.value}`"
      :aria-selected="active === tab.value"
      :aria-controls="`panel-${param}-${tab.value}`"
      :tabindex="active === tab.value ? 0 : -1"
      :class="{ 'tab--on': active === tab.value }"
      :data-value="tab.value"
      @click="select(tab.value)"
      @keydown="onKeydown($event, index)"
    >
      <span class="tab-label">{{ tab.label }}</span>
      <span v-if="tab.count !== undefined" class="tab-count">{{ tab.count }}</span>
      <span v-if="tab.badge" class="tab-badge" aria-hidden="true">●</span>
    </button>
  </div>
  <div
    class="tab-panel"
    role="tabpanel"
    :id="`panel-${param}-${active}`"
    :aria-labelledby="`tab-${param}-${active}`"
  >
    <slot :active="active" />
  </div>
</template>

<style scoped>
.tabs {
  display: flex;
  gap: var(--s1);
  min-width: 0;
  border-bottom: 1px solid var(--line);
  overflow-x: auto;
  scrollbar-width: none;
}
.tabs::-webkit-scrollbar {
  display: none;
}
.tab {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  min-height: 36px;
  padding: 0 var(--s3);
  background: none;
  border: 0;
  border-bottom: 2px solid transparent;
  color: var(--fg-3);
  font: inherit;
  font-size: var(--fs-sm);
  white-space: nowrap;
  cursor: pointer;
}
.tab:hover {
  color: var(--fg);
}
.tab:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: -2px;
}
.tab--on {
  color: var(--fg);
  border-bottom-color: var(--accent);
}
.tab-count {
  min-width: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--surface-2);
  color: var(--fg-2);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
  text-align: center;
}
/* 黄色只代表「需要人」（§5） */
.tab-badge {
  color: var(--need);
  font-size: 8px;
}
.tab-panel {
  padding-top: var(--s3);
}
@media (max-width: 760px) {
  .tab {
    min-height: 40px;
  }
}
</style>
