<script setup lang="ts">
// 通知的「限定项目」输入（设置 · 通知的共用组件）：IM 机器人与桌面通知两个侧栏共用。
// 留空 = 全部项目；多个用逗号分隔，写回前由页面统一解析成字符串数组。
import { t } from '../i18n'

const projects = defineModel<string>({ required: true })
withDefaults(defineProps<{ testId?: string }>(), { testId: 'notify-projects' })
</script>

<template>
  <label class="field">
    <span class="field-name">{{ t('settings.notify.projectsLabel') }}</span>
    <input
      v-model="projects"
      class="input"
      :data-testid="testId"
      :placeholder="t('settings.notify.projectsPlaceholder')"
    />
  </label>
</template>

<style scoped>
.field {
  display: grid;
  gap: 4px;
  margin-bottom: 10px;
}
.field-name {
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.input {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 7px 10px;
  font: inherit;
  font-size: var(--fs-sm);
  background: var(--surface);
  color: var(--ink);
}
</style>
