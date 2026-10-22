<script setup lang="ts">
// 返回列表（设计稿 §8、§16）：带上「来源列表」原来的 URL（筛选）与滚动位置；
// 列表侧用 useListReturn（ui/useListReturn.ts）记录，这里只负责跳回去。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { t } from '../i18n'
import { readListReturn } from './listReturn'

const props = withDefaults(
  defineProps<{
    /** 列表标识（与列表页 useListReturn 的 id 一致，如 jobs / plans） */
    list: string
    /** 没有记录的来源时回落到这个地址 */
    to: string
    label?: string
  }>(),
  { label: '' },
)

const router = useRouter()
const text = computed(() => props.label || t('act.back'))

function go(): void {
  const saved = readListReturn(props.list)
  void router.push(saved?.url || props.to)
}
</script>

<template>
  <button
    class="back-link"
    type="button"
    data-test="back-link"
    :data-target="readListReturn(list)?.url || to"
    @click="go"
  >
    <span class="back-arrow" aria-hidden="true">←</span>
    <span class="back-text">{{ text }}</span>
  </button>
</template>

<style scoped>
.back-link {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  min-height: 32px;
  padding: 0 var(--s2);
  background: none;
  border: 1px solid transparent;
  border-radius: var(--r-ctl);
  color: var(--fg-2);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.back-link:hover {
  color: var(--fg);
  border-color: var(--line);
}
.back-link:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
@media (max-width: 760px) {
  .back-link {
    min-height: 40px;
    padding: 0 var(--s3);
  }
}
</style>
