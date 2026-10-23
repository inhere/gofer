<script setup lang="ts">
// 管家笔记抽屉：查看最新版本（Markdown）、编辑（带 version 乐观锁，409 时给出最新版并让
// 用户选择"采用最新版"或"在最新版上覆盖保存"）、历史版本（点开看全文，可以此为基础编辑）。
import { computed, onMounted, ref } from 'vue'
import { ApiError } from '../api/client'
import { getStewardNotes, getStewardNotesHistory, putStewardNotes, type StewardNotes } from '../api/steward'
import { byteLength, notesConflictOf, notesSizeLabel, type NotesConflict } from '../utils/steward'
import { t } from '../i18n'
import MarkdownBlock from './MarkdownBlock.vue'

const emit = defineEmits<{ (e: 'close'): void; (e: 'changed'): void }>()

const latest = ref<StewardNotes>({ version: 0, body: '' })
const history = ref<StewardNotes[]>([])
const viewing = ref<StewardNotes | null>(null) // 正在查看的历史版本
const editing = ref(false)
const draft = ref('')
const baseVersion = ref(0)
const conflict = ref<NotesConflict | null>(null)
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const notice = ref('')

const shown = computed(() => viewing.value ?? latest.value)
const isHistorical = computed(() => viewing.value !== null && viewing.value.version !== latest.value.version)

function fmtTime(at?: number): string {
  if (!at) return ''
  return new Date(at * 1000).toLocaleString('zh-CN', { hour12: false })
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [n, h] = await Promise.all([getStewardNotes(), getStewardNotesHistory()])
    latest.value = n.notes
    history.value = h
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function startEdit(from?: StewardNotes): void {
  draft.value = (from ?? latest.value).body
  baseVersion.value = latest.value.version
  conflict.value = null
  notice.value = from && from.version !== latest.value.version ? t('today.notes.editFromNotice', { version: from.version }) : ''
  editing.value = true
  viewing.value = null
}

function cancelEdit(): void {
  editing.value = false
  conflict.value = null
}

async function save(): Promise<void> {
  if (saving.value) return
  saving.value = true
  error.value = ''
  try {
    const r = await putStewardNotes(draft.value, baseVersion.value)
    latest.value = r.notes
    editing.value = false
    conflict.value = null
    notice.value = t('today.notes.savedAs', { version: r.notes.version })
    history.value = await getStewardNotesHistory()
    emit('changed')
  } catch (e) {
    const c = notesConflictOf(e, draft.value)
    if (c) conflict.value = c
    else if (e instanceof ApiError && e.status === 413) error.value = t('today.notes.tooLarge')
    else error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

// 采用最新版：丢掉我的修改，改在最新版上重新编辑。
function useLatest(): void {
  if (!conflict.value) return
  latest.value = conflict.value.current
  draft.value = conflict.value.current.body
  baseVersion.value = conflict.value.current.version
  conflict.value = null
}

// 覆盖：保留我的文字，把基准版本号换成最新版再保存。
async function overwrite(): Promise<void> {
  if (!conflict.value) return
  baseVersion.value = conflict.value.current.version
  conflict.value = null
  await save()
}

async function openVersion(v: number): Promise<void> {
  error.value = ''
  try {
    viewing.value = (await getStewardNotes(v)).notes
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(() => void load())
</script>

<template>
  <div class="notes-overlay" @click.self="emit('close')">
    <div class="notes-panel" role="dialog" :aria-label="t('today.notes.title')" data-test="steward-notes">
      <div class="notes-head">
        <span class="mono title">{{ t('today.notes.title') }} <span v-if="latest.version" class="muted">v{{ latest.version }} · {{ notesSizeLabel(byteLength(latest.body)) }}</span></span>
        <div class="head-actions">
          <button v-if="!editing" class="btn mono" type="button" data-test="notes-edit" @click="startEdit()">{{ t('today.notes.edit') }}</button>
          <button class="btn mono" type="button" data-test="notes-close" @click="emit('close')">{{ t('today.notes.close') }}</button>
        </div>
      </div>
      <div class="notes-body">
        <p v-if="error" class="msg msg--err mono" data-test="notes-error">{{ error }}</p>
        <p v-if="notice" class="msg msg--ok mono" data-test="notes-notice">{{ notice }}</p>
        <p v-if="loading" class="muted mono">{{ t('today.notes.loading') }}</p>

        <section v-if="conflict" class="conflict" data-test="notes-conflict">
          <p class="msg msg--err mono">{{ t('today.notes.conflict', { version: conflict.current.version, by: conflict.current.by || t('today.notes.unknown') }) }}</p>
          <div class="cols">
            <div><h4 class="mono">{{ t('today.notes.latest') }}</h4><pre class="raw">{{ conflict.current.body }}</pre></div>
            <div><h4 class="mono">{{ t('today.notes.mine') }}</h4><pre class="raw">{{ conflict.mine }}</pre></div>
          </div>
          <div class="head-actions">
            <button class="btn mono" type="button" data-test="notes-use-latest" @click="useLatest">{{ t('today.notes.useLatest') }}</button>
            <button class="btn primary mono" type="button" :disabled="saving" data-test="notes-overwrite" @click="overwrite">{{ t('today.notes.overwrite') }}</button>
          </div>
        </section>

        <template v-if="editing && !conflict">
          <textarea v-model="draft" class="editor mono" rows="16" data-test="notes-textarea" :placeholder="t('today.notes.placeholder')"></textarea>
          <p class="muted mono">{{ t('today.notes.baseHint', { version: baseVersion }) }}</p>
          <div class="head-actions">
            <button class="btn primary mono" type="button" :disabled="saving" data-test="notes-save" @click="save">{{ saving ? t('today.notes.saving') : t('today.notes.save') }}</button>
            <button class="btn mono" type="button" @click="cancelEdit">{{ t('today.notes.cancel') }}</button>
          </div>
        </template>

        <template v-else-if="!editing">
          <p v-if="isHistorical" class="msg mono" data-test="notes-historical">
            {{ t('today.notes.historical', { version: shown.version, by: shown.by || t('today.notes.unknown'), at: fmtTime(shown.at) }) }}
            <button class="link mono" type="button" data-test="notes-edit-from" @click="startEdit(shown)">{{ t('today.notes.editFrom') }}</button>
            <button class="link mono" type="button" @click="viewing = null">{{ t('today.notes.backToLatest') }}</button>
          </p>
          <MarkdownBlock v-if="shown.body.trim()" :text="shown.body" />
          <p v-else-if="!loading" class="muted mono" data-test="notes-empty">{{ t('today.notes.empty') }}</p>
        </template>

        <section class="history" data-test="notes-history">
          <h4 class="mono">{{ t('today.notes.history', { n: history.length }) }}</h4>
          <ul>
            <li v-for="h in history" :key="h.version">
              <button class="link mono" type="button" :data-test="`notes-v${h.version}`" @click="openVersion(h.version)">v{{ h.version }}</button>
              <span class="muted mono">{{ h.by || t('today.notes.unknown') }} · {{ fmtTime(h.at) }} · {{ notesSizeLabel(byteLength(h.body)) }}</span>
            </li>
          </ul>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.notes-overlay { position: fixed; inset: 0; z-index: 90; display: flex; justify-content: flex-end; background: rgba(0, 0, 0, 0.6); }
.notes-panel { display: flex; flex-direction: column; width: min(680px, 100vw); height: 100%; background: var(--surface); border-left: 1px solid var(--line); overflow: hidden; }
.notes-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 10px 14px; border-bottom: 1px solid var(--line); flex: none; }
.title { font-size: 13px; color: var(--ink); }
.head-actions { display: flex; flex-wrap: wrap; gap: 6px; }
.notes-body { flex: 1; overflow-y: auto; padding: 12px 14px 40px; display: flex; flex-direction: column; gap: 12px; }
.editor { width: 100%; box-sizing: border-box; color: var(--ink); background: var(--bg); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 8px; font-size: 12px; }
.raw { margin: 0; white-space: pre-wrap; word-break: break-word; font-size: 11px; color: var(--ink); background: var(--bg); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 8px; max-height: 240px; overflow: auto; }
.cols { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
h4 { margin: 0 0 6px; font-size: 11px; color: var(--ink-3); }
.history ul { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 4px; }
.history li { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.muted { margin: 0; font-size: 11px; color: var(--ink-3); }
.msg { margin: 0; padding: 6px 10px; font-size: 12px; border: 1px solid var(--line); border-radius: var(--r-chip); color: var(--ink); }
.msg--err { color: var(--fail); border-color: var(--fail); }
.msg--ok { color: var(--done); border-color: var(--done); }
.btn { border: 1px solid var(--line); border-radius: var(--r-chip); padding: 6px 12px; cursor: pointer; color: var(--ink); background: transparent; font-size: 12px; }
.btn.primary { color: var(--bg); background: var(--accent); }
.btn:disabled { opacity: 0.45; cursor: not-allowed; }
.link { background: none; border: 0; padding: 0 4px; font-size: 11px; color: var(--accent); cursor: pointer; text-decoration: underline; }
@media (max-width: 760px) { .cols { grid-template-columns: minmax(0, 1fr); } }
</style>
