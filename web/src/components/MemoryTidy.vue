<script setup lang="ts">
// 记忆页的「待整理」区：doctor 里需要人判断的项（被标记失效的、疑似重复的、久未更新的）。
// 每条三个操作——保留（清除标记）、改写（打开详情抽屉）、归档（仅仓库记忆）。
// 不提醒、不带数字徽标：条数只写在区内的小标题里。取数与操作在 views/Memory.vue，这里只渲染。
import type { TidyItem } from '../utils/memories'
import { memorySourceLabel, tidyCanArchive } from '../utils/memories'
import { doctorLabel, MEMORY_KIND_LABEL, memoryKind, memorySummary } from '../utils/trackerView'
import { t } from '../i18n'

defineProps<{
  items: TidyItem[]
  // 正在处理的那一条（来源 + key），其按钮置灰。
  busyKey?: string
  error?: string
  // 合并视图（全部）下才标来源。
  showSource?: boolean
}>()
const emit = defineEmits<{
  (event: 'keep', item: TidyItem): void
  (event: 'rewrite', item: TidyItem): void
  (event: 'archive', item: TidyItem): void
}>()
</script>

<template>
  <section class="tidy" data-test="tidy-panel">
    <header class="tidy-head">
      <h2 class="tidy-title">{{ t('memory.tidy.title') }}</h2>
      <span class="tidy-count" data-test="tidy-count">{{ t('memory.tidy.count', { n: items.length }) }}</span>
    </header>
    <p class="tidy-help">{{ t('memory.tidy.help') }}</p>
    <p v-if="error" class="tidy-err" data-test="tidy-error">{{ error }}</p>
    <p v-if="items.length === 0" class="tidy-empty" data-test="tidy-empty">{{ t('memory.tidy.empty') }}</p>
    <article v-for="item in items" :key="item.key" class="tidy-item" data-test="tidy-item">
      <div class="tidy-main">
        <span class="mono tidy-key" data-test="tidy-key">{{ item.row.data.key || item.row.id }}</span>
        <span class="tidy-kind mono">{{ MEMORY_KIND_LABEL[memoryKind(item.row.data)] }}</span>
        <span v-if="showSource" class="tidy-source" data-test="tidy-source">{{ memorySourceLabel(item.row.source) }}</span>
        <span class="tidy-summary">{{ memorySummary(item.row.data) || '—' }}</span>
      </div>
      <ul class="tidy-findings">
        <li v-for="f in item.findings" :key="f.slug" class="mono" data-test="tidy-finding">⚠ {{ doctorLabel(f.slug) }}<template v-if="f.detail">：{{ f.detail }}</template></li>
      </ul>
      <div class="tidy-actions">
        <button type="button" class="btn" data-test="tidy-keep" :disabled="busyKey === item.key" :title="t('memory.tidy.keepTitle')" @click="emit('keep', item)">{{ t('memory.tidy.keep') }}</button>
        <button type="button" class="btn" data-test="tidy-rewrite" :disabled="busyKey === item.key" :title="t('memory.tidy.rewriteTitle')" @click="emit('rewrite', item)">{{ t('memory.tidy.rewrite') }}</button>
        <button
          type="button"
          class="btn"
          data-test="tidy-archive"
          :disabled="busyKey === item.key || !tidyCanArchive(item.row)"
          :title="tidyCanArchive(item.row) ? t('memory.tidy.archiveTitle') : t('memory.tidy.archiveUnsupported')"
          @click="emit('archive', item)"
        >{{ t('memory.tidy.archive') }}</button>
      </div>
    </article>
  </section>
</template>

<style scoped>
.tidy {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  margin-top: var(--s3);
}
.tidy-head {
  display: flex;
  align-items: baseline;
  gap: var(--s2);
}
.tidy-title {
  margin: 0;
  font-size: var(--fs-md, 15px);
  color: var(--ink);
}
.tidy-count,
.tidy-help,
.tidy-empty {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.tidy-help,
.tidy-empty {
  margin: 0;
}
.tidy-err {
  color: var(--fail);
  font-size: var(--fs-sm);
  margin: 0;
  overflow-wrap: anywhere;
}
.tidy-item {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  padding: 10px 12px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
}
.tidy-main {
  display: flex;
  align-items: baseline;
  gap: var(--s2);
  flex-wrap: wrap;
  min-width: 0;
}
.tidy-key {
  color: var(--ink);
  overflow-wrap: anywhere;
}
.tidy-kind,
.tidy-source {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.tidy-summary {
  color: var(--ink-2, var(--ink));
  font-size: var(--fs-sm);
  overflow-wrap: anywhere;
}
.tidy-findings {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  color: var(--need-ink);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.tidy-actions {
  display: flex;
  gap: var(--s2);
  flex-wrap: wrap;
}
.btn {
  display: inline-flex;
  align-items: center;
  min-height: 32px;
  padding: 0 var(--s3);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  white-space: nowrap;
}
.btn:hover {
  border-color: var(--accent);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
@media (max-width: 760px) {
  .btn {
    min-height: 40px;
  }
}
</style>
