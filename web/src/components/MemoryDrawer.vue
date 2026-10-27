<script setup lang="ts">
// 记忆详情抽屉（P6-记忆页第二版，issue gofer-icqt）：展示 key / 摘要 / 类型 / 来源 /
// 更新时间 / 过期时间 / 标签，并可就地改内容、摘要与标签（标签增删 chip），保存 / 删除 /
// 标记失效。外壳用统一的 ui/Drawer（§8）：Esc 关闭、焦点循环与归还由它负责。
import { computed, ref, watch } from 'vue'
import Drawer from '../ui/Drawer.vue'
import MarkdownBlock from './MarkdownBlock.vue'
import type { MemoryDoctorFinding } from '../api/types'
import {
  addMemoryTag,
  memoryEditOf,
  memorySourceLabel,
  removeMemoryTag,
  type MemoryEdit,
  type SourcedMemoryRow,
} from '../utils/memories'
import { doctorLabel, fmtTrackerTime, MEMORY_KIND_LABEL, memoryKind } from '../utils/trackerView'
import { t } from '../i18n'

const props = defineProps<{
  row: SourcedMemoryRow
  doctor?: MemoryDoctorFinding[]
  saving?: boolean
  flagged?: boolean
  error?: string
}>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'save', edit: MemoryEdit): void
  (event: 'remove'): void
  (event: 'flag', reason: string): void
}>()

const edit = ref<MemoryEdit>(memoryEditOf(props.row))
const tagInput = ref('')
const flagReason = ref('')
const confirmDelete = ref(false)
watch(
  () => props.row,
  (row) => {
    edit.value = memoryEditOf(row)
    tagInput.value = ''
    flagReason.value = ''
    confirmDelete.value = false
  },
  { immediate: true },
)

// 仓库记忆（.gofer/tracker 里的那份）没有标记失效接口，只有 server 存的 scoped 记忆有。
const canFlag = computed(() => props.row.source?.scope !== 'repo' && props.row.source !== undefined)
const expires = computed(() => String(props.row.data.expires_at ?? ''))
const title = computed(() => `${t('memory.drawer.title')} · ${props.row.data.key || props.row.id}`)

function addTag(): void {
  const tag = tagInput.value.trim()
  if (!tag) return
  edit.value = { ...edit.value, tags: addMemoryTag(edit.value.tags, tag) }
  tagInput.value = ''
}
function dropTag(tag: string): void {
  edit.value = { ...edit.value, tags: removeMemoryTag(edit.value.tags, tag) }
}
function save(): void {
  emit('save', { content: edit.value.content, summary: edit.value.summary, tags: [...edit.value.tags] })
}
function remove(): void {
  if (!confirmDelete.value) {
    confirmDelete.value = true
    return
  }
  emit('remove')
}
function flag(): void {
  const reason = flagReason.value.trim()
  if (!reason) return
  emit('flag', reason)
}
</script>

<template>
  <Drawer :model-value="true" :label="title" width="520px" @update:model-value="emit('close')">
    <div class="drawer-body" data-test="memory-drawer">
      <p class="memory-key mono" data-test="drawer-memory-key">{{ row.data.key || row.id }}</p>
      <dl class="memory-meta">
        <div><dt>{{ t('memory.drawer.kind') }}</dt><dd data-test="drawer-memory-kind">{{ MEMORY_KIND_LABEL[memoryKind(row.data)] }}</dd></div>
        <div><dt>{{ t('memory.source.label') }}</dt><dd data-test="drawer-memory-source">{{ memorySourceLabel(row.source) || '—' }}</dd></div>
        <div><dt>{{ t('memory.drawer.updated') }}</dt><dd class="mono">{{ fmtTrackerTime(row.data.updated_at || row.updated_at) }}</dd></div>
        <!-- 原始时间未知（从 bd 迁移等）：created_at 只是导入时间，不能说成创建时间。 -->
        <div v-if="row.data.created_unknown"><dt>{{ t('memory.drawer.created') }}</dt><dd class="mono" data-test="drawer-memory-imported">{{ t('memory.imported', { time: fmtTrackerTime(row.data.created_at || row.data.updated_at || row.updated_at) }) }}</dd></div>
        <div><dt>{{ t('memory.drawer.expires') }}</dt><dd class="mono" data-test="drawer-memory-expires">{{ expires ? fmtTrackerTime(expires) : t('memory.drawer.never') }}</dd></div>
        <div><dt>{{ t('memory.drawer.by') }}</dt><dd>{{ row.data.by || '—' }}</dd></div>
      </dl>

      <ul v-if="doctor?.length" class="flags">
        <li v-for="f in doctor" :key="f.slug" class="memory-flag mono" data-test="drawer-doctor">⚠ {{ doctorLabel(f.slug) }}<template v-if="f.detail">：{{ f.detail }}</template></li>
      </ul>
      <template v-if="row.data.flags?.length">
        <h3>{{ t('memory.drawer.flags') }}</h3>
        <ul class="flags">
          <li v-for="f in row.data.flags" :key="`${f.at}-${f.reason}`" class="memory-flag mono" data-test="drawer-memory-flag">{{ fmtTrackerTime(f.at) }} · {{ f.by || f.job || '—' }} · {{ f.reason }}</li>
        </ul>
      </template>

      <label class="field">
        {{ t('memory.drawer.summary') }}
        <input v-model="edit.summary" class="in" data-test="memory-summary-input" :placeholder="t('memory.drawer.summaryPlaceholder')" />
      </label>

      <section class="field" data-test="memory-tags">
        <span>{{ t('memory.drawer.tags') }}</span>
        <div class="tag-row">
          <span v-for="tag in edit.tags" :key="tag" class="tag-chip" data-test="memory-tag">
            <span class="mono">{{ tag }}</span>
            <button type="button" class="tag-drop" :aria-label="t('memory.drawer.tagRemove', { tag })" data-test="memory-tag-remove" @click="dropTag(tag)">×</button>
          </span>
          <span v-if="edit.tags.length === 0" class="dim">{{ t('memory.tagsNone') }}</span>
        </div>
        <div class="tag-add">
          <input v-model="tagInput" class="in" data-test="memory-tag-input" :placeholder="t('memory.drawer.tagPlaceholder')" @keydown.enter.prevent="addTag" />
          <button type="button" class="btn" data-test="memory-tag-add" :disabled="!tagInput.trim()" @click="addTag">{{ t('memory.drawer.tagAdd') }}</button>
        </div>
      </section>

      <label class="field">
        {{ t('memory.drawer.content') }}
        <textarea v-model="edit.content" class="in content-input" rows="10" data-test="memory-content" />
      </label>
      <section class="preview">
        <MarkdownBlock :text="String(row.data.content ?? '')" />
      </section>

      <p v-if="error" class="err" data-test="memory-drawer-error">{{ error }}</p>
      <div class="actions">
        <button type="button" class="btn btn--primary" :disabled="saving" data-test="memory-save" @click="save">{{ t('memory.save') }}</button>
        <button type="button" class="btn btn--danger" :disabled="saving" data-test="memory-delete" @click="remove">{{ confirmDelete ? t('memory.drawer.deleteConfirm') : t('memory.delete') }}</button>
      </div>

      <section v-if="canFlag" class="flag-box">
        <label class="field">
          {{ t('memory.drawer.flagReason') }}
          <input v-model="flagReason" class="in" data-test="memory-flag-reason" :placeholder="t('memory.drawer.flagReasonPlaceholder')" />
        </label>
        <button type="button" class="btn" :disabled="saving || !flagReason.trim()" data-test="memory-flag" @click="flag">{{ flagged ? t('memory.drawer.flagDone') : t('memory.drawer.flag') }}</button>
      </section>
      <p v-else class="dim hint">{{ t('memory.drawer.flagUnsupported') }}</p>
    </div>
  </Drawer>
</template>

<style scoped>
.drawer-body {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
  min-width: 0;
}
.memory-key {
  margin: 0;
  color: var(--accent);
  font-size: var(--fs-sm);
  overflow-wrap: anywhere;
}
.memory-meta {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: var(--s2) var(--s3);
  margin: 0;
}
.memory-meta div {
  min-width: 0;
}
.memory-meta dt {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.memory-meta dd {
  margin: 2px 0 0;
  color: var(--ink);
  font-size: var(--fs-sm);
  overflow-wrap: anywhere;
}
.flags {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.memory-flag {
  color: var(--run);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 5px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  min-width: 0;
}
.in {
  min-height: 32px;
  padding: 4px var(--s2);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  min-width: 0;
}
.content-input {
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  resize: vertical;
}
.tag-row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1);
  align-items: center;
}
.tag-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 4px 1px var(--s2);
  border: 1px solid var(--line-strong);
  border-radius: 999px;
  font-size: var(--fs-xs);
  color: var(--ink);
}
.tag-drop {
  min-width: 24px;
  min-height: 24px;
  background: transparent;
  border: 0;
  color: var(--ink-3);
  font: inherit;
  cursor: pointer;
}
.tag-drop:hover {
  color: var(--fail);
}
.tag-add {
  display: flex;
  gap: var(--s1);
}
.tag-add .in {
  flex: 1;
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
  cursor: pointer;
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn--danger {
  color: var(--fail);
  border-color: var(--fail);
}
.actions {
  display: flex;
  gap: var(--s2);
  flex-wrap: wrap;
}
.flag-box {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  align-items: flex-start;
  padding-top: var(--s2);
  border-top: 1px solid var(--line);
}
.preview {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  padding: var(--s2) var(--s3);
  background: var(--sunken);
  min-width: 0;
}
.err {
  color: var(--fail);
  font-size: var(--fs-sm);
  margin: 0;
  overflow-wrap: anywhere;
}
.dim {
  color: var(--ink-3);
}
.hint {
  margin: 0;
  font-size: var(--fs-xs);
}
@media (max-width: 760px) {
  .btn {
    min-height: 40px;
  }
}
</style>
