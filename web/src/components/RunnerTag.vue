<script setup lang="ts">
// 执行机标签（工作台会话列表 / 会话标题）：一行小字显示这台会话跑在哪台机器上。
// 颜色由 worker id 派生（utils/runnerTag.ts），同一台 worker 恒为同一色；本机内置 runner 显示
// `server` 且用中性色（G043：只改显示，wire / DB 里仍是 `local`）。
import { computed } from 'vue'
import { t } from '../i18n'
import { runnerTag } from '../utils/runnerTag'

const props = defineProps<{ name?: string }>()

const tag = computed(() => runnerTag(props.name))
</script>

<template>
  <span
    v-if="tag"
    class="runner-tag mono"
    :class="`runner-tag--${tag.tone}`"
    :style="{ color: tag.color, background: tag.soft }"
    :title="t('workbench.row.execMachine', { name: tag.label })"
    data-test="runner-tag"
    >{{ tag.label }}</span
  >
</template>

<style scoped>
.runner-tag {
  display: inline-block;
  flex: none;
  max-width: 12em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 0 5px;
  border-radius: var(--r-chip);
  font-size: 10.5px;
  line-height: 16px;
  vertical-align: baseline;
}
</style>
