<script setup lang="ts">
// Issues 批量操作栏：已选计数 + 关闭 / 改状态 / 加标签，执行前统一弹确认框。
// 只负责收集意图并 emit；真正请求与选中状态由 Issues.vue 持有。
import { computed, ref } from 'vue'
import type { TrackerBatchSet } from '../api/types'
import { ISSUE_STATUS_VALUES, issueStatusText, type IssueStatus } from '../utils/statusText'
import { t } from '../i18n'

const props = defineProps<{ count: number; sampleIds: string[]; running: boolean }>()
const emit = defineEmits<{
  (e: 'clear'): void
  (e: 'run', set: TrackerBatchSet): void
}>()

type Kind = 'close' | 'status' | 'tag'
const status = ref<IssueStatus>('open')
const tags = ref('')
const pending = ref<Kind | null>(null)
const reason = ref('')

const tagList = computed(() => tags.value.split(/[,\s，]+/).map((t) => t.trim()).filter(Boolean))
const title = computed(() =>
  pending.value === 'close'
    ? t('issues.batch.close')
    : pending.value === 'status'
      ? t('issues.batch.status')
      : t('issues.batch.tag'),
)
const detail = computed(() => {
  if (pending.value === 'close') return t('issues.batch.closeDetail', { n: props.count })
  if (pending.value === 'status') return t('issues.batch.statusDetail', { n: props.count, status: issueStatusText(status.value) })
  return t('issues.batch.tagDetail', { n: props.count, tags: tagList.value.join(t('util.sep.list')) })
})

function ask(kind: Kind): void {
  pending.value = kind
  reason.value = ''
}
function confirm(): void {
  const kind = pending.value
  if (!kind) return
  pending.value = null
  if (kind === 'close') emit('run', { status: 'closed', ...(reason.value.trim() ? { close_reason: reason.value.trim() } : {}) })
  else if (kind === 'status') emit('run', { status: status.value })
  else emit('run', { add_tags: tagList.value })
}
</script>

<template>
  <div class="batch-bar mono" data-test="batch-bar">
    <span class="batch-count" data-test="batch-count">{{ t('issues.batch.selected', { n: count }) }}</span>
    <button type="button" class="batch-link" data-test="batch-clear" @click="emit('clear')">{{ t('issues.batch.clear') }}</button>
    <span class="batch-sep"></span>
    <button type="button" class="batch-btn danger" :disabled="running" data-test="batch-close" @click="ask('close')">{{ t('issues.batch.close') }}</button>
    <span class="batch-group">
      <select v-model="status" class="batch-input" :aria-label="t('issues.batch.targetStatus')"><option v-for="s in ISSUE_STATUS_VALUES" :key="s" :value="s">{{ issueStatusText(s) }}</option></select>
      <button type="button" class="batch-btn" :disabled="running" data-test="batch-status" @click="ask('status')">{{ t('issues.batch.changeStatus') }}</button>
    </span>
    <span class="batch-group">
      <input v-model="tags" class="batch-input" :placeholder="t('issues.batch.tagsPlaceholder')" :aria-label="t('issues.batch.tagsAria')" />
      <button type="button" class="batch-btn" :disabled="running || tagList.length === 0" data-test="batch-tag" @click="ask('tag')">{{ t('issues.batch.addTag') }}</button>
    </span>
  </div>

  <div v-if="pending" class="bb-overlay" data-test="batch-confirm" @click.self="pending = null">
    <div class="bb-panel" role="dialog" aria-modal="true" :aria-label="title">
      <p class="bb-title mono">{{ title }}</p>
      <p class="bb-detail mono">{{ detail }}</p>
      <p class="bb-ids mono">{{ sampleIds.join(t('util.sep.list')) }}<span v-if="count > sampleIds.length">{{ t('issues.batch.more', { n: count }) }}</span></p>
      <label v-if="pending === 'close'" class="bb-field mono"><span>{{ t('issues.batch.closeReason') }}</span>
        <input v-model="reason" class="batch-input" :placeholder="t('issues.batch.closeReasonPlaceholder')" data-test="batch-reason" @keydown.enter="confirm" />
      </label>
      <p class="bb-note mono">{{ t('issues.batch.note') }}</p>
      <div class="bb-actions">
        <button type="button" class="batch-btn" data-test="batch-cancel" @click="pending = null">{{ t('util.action.cancel') }}</button>
        <button type="button" class="batch-btn primary" data-test="batch-confirm-ok" @click="confirm">{{ t('issues.batch.confirm') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.batch-bar{position:sticky;top:0;z-index:5;display:flex;align-items:center;flex-wrap:wrap;gap:8px 10px;padding:8px 12px;border:1px solid var(--accent);border-radius:var(--r-chip);background:var(--surface);margin-bottom:10px;font-size:12px}
.batch-count{color:var(--accent);font-weight:600}
.batch-link{background:transparent;border:0;color:var(--ink-3);text-decoration:underline;padding:2px;font:inherit;cursor:pointer}
.batch-sep{flex:1 1 0;min-width:0}
.batch-group{display:inline-flex;gap:4px;align-items:center;min-width:0}
.batch-input{background:var(--surface);color:var(--ink);border:1px solid var(--line);border-radius:var(--r-chip);padding:5px 8px;font-size:12px;min-width:0;max-width:100%}
.batch-input:focus{border-color:var(--accent);outline:none}
.batch-btn{border:1px solid var(--line);border-radius:var(--r-chip);background:transparent;color:var(--accent);padding:3px 10px;font-family:var(--font-mono);font-size:11px;cursor:pointer}
.batch-btn:disabled{opacity:.5;cursor:not-allowed}
.batch-btn.danger{color:var(--fail);border-color:var(--fail)}
.batch-btn.primary{background:var(--accent);color:var(--bg);border-color:var(--accent);font-weight:600}
.bb-overlay{position:fixed;inset:0;z-index:50;background:rgba(0,0,0,.55);display:flex;align-items:center;justify-content:center;padding:16px}
.bb-panel{width:min(460px,100%);background:var(--surface);border:1px solid var(--line);border-radius:var(--r-chip);padding:16px;display:flex;flex-direction:column;gap:10px;max-height:90vh;overflow:auto}
.bb-title{margin:0;color:var(--ink);font-size:14px}.bb-detail{margin:0;color:var(--ink);font-size:12px}
.bb-ids,.bb-note{margin:0;color:var(--ink-3);font-size:11px;overflow-wrap:anywhere}
.bb-field{display:flex;flex-direction:column;gap:5px;color:var(--ink-3);font-size:11px}
.bb-actions{display:flex;justify-content:flex-end;gap:8px}
@media (max-width:760px){.batch-bar{gap:6px}.batch-group{flex:1 1 100%}.batch-group .batch-input{flex:1 1 auto}}
</style>
