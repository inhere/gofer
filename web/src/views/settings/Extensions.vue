<script setup lang="ts">
// K5：设置 · 扩展设置。读 GET /v1/settings，写 PUT / DELETE /v1/settings/{key}，
// 导出 GET /v1/settings/export（改过的键的 YAML），导入 POST /v1/settings/import
// （先 dry_run 预览逐键差异，确认后才写）。
//
// 页面按注册表的组（键前缀 work.* / prompt.*）分节，一项一张卡：说明、类型、当前值、
// 默认值、是否改过、最后修改人与时间；按类型换编辑控件（数字 / 开关 / 一行一个的
// 多行输入 / 大文本域）。前端的校验只是少跑一趟 400，服务端的原因原样显示。
//
// 没有 settings.write 权限或调用方上限是只读时整页只读：写按钮全部隐藏并说明原因，
// 服务端同样会 403。
import { computed, onMounted, ref } from 'vue'
import { ApiError } from '../../api/client'
import {
  exportSettings,
  importSettings,
  listSettings,
  resetSetting,
  updateSetting,
} from '../../api/settings'
import { fmtDateTime } from '../../api/time'
import type { SettingItem, SettingsImportResp } from '../../api/types'
import { useWhoami } from '../../store/whoami'
import {
  boundsHint,
  controlFor,
  defaultText,
  dirty,
  draftOf,
  groupSettings,
  importActionLabel,
  importSummary,
  isPrompt,
  kindLabel,
  PROMPT_EMPTY_HINT,
  validateDraft,
  valueOfDraft,
  valueText,
} from '../../utils/extensionSettings'
import type { Draft } from '../../utils/extensionSettings'
import { t } from '../../i18n'

const { readOnly, can } = useWhoami()

// 写的门槛：调用方上限只读，或缺 settings.write（服务端也是这两道）。
const canWrite = computed(() => can('settings.write'))
const readOnlyReason = computed(() =>
  readOnly.value ? t('settings.ext.readonlyCeiling') : t('settings.ext.readonlyPerm'),
)

const items = ref<SettingItem[]>([])
const drafts = ref<Record<string, Draft>>({})
const fieldErrors = ref<Record<string, string>>({})
const notices = ref<Record<string, string>>({})
const showDefault = ref<Record<string, boolean>>({})
const pendingReset = ref('')
const savingKey = ref('')
const loading = ref(false)
const pageError = ref('')

// 导入：选文件后先 dry_run 预览，确认后写。
const importState = ref<{ name: string; yaml: string; preview: SettingsImportResp } | null>(null)
const importing = ref(false)
const importError = ref('')
const importNotice = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

const groups = computed(() => groupSettings(items.value))
const modifiedCount = computed(() => items.value.filter((it) => it.modified).length)

function errText(e: unknown): string {
  if (e instanceof ApiError) return e.detail ? `${e.message}：${e.detail}` : e.message
  return e instanceof Error ? e.message : String(e)
}

function fillDrafts(): void {
  const next: Record<string, Draft> = {}
  for (const it of items.value) next[it.key] = draftOf(it)
  drafts.value = next
  fieldErrors.value = {}
  notices.value = {}
  pendingReset.value = ''
}

async function load(): Promise<void> {
  loading.value = true
  pageError.value = ''
  try {
    items.value = await listSettings()
    fillDrafts()
  } catch (e) {
    pageError.value = errText(e)
  } finally {
    loading.value = false
  }
}

function draft(key: string): Draft {
  return drafts.value[key] ?? ''
}

// draftText 是文本类控件的值（开关用 draft 的 boolean）。
function draftText(key: string): string {
  const d = draft(key)
  return typeof d === 'string' ? d : ''
}

function setDraft(key: string, value: Draft): void {
  drafts.value = { ...drafts.value, [key]: value }
  if (fieldErrors.value[key]) {
    const next = { ...fieldErrors.value }
    delete next[key]
    fieldErrors.value = next
  }
  if (notices.value[key]) {
    const next = { ...notices.value }
    delete next[key]
    notices.value = next
  }
}

function textOf(e: Event): string {
  return (e.target as HTMLInputElement | HTMLTextAreaElement).value
}

function checkedOf(e: Event): boolean {
  return (e.target as HTMLInputElement).checked
}

function isDirty(it: SettingItem): boolean {
  return dirty(it, draft(it.key))
}

// 等于默认值时给一个标记说明「即使改了也和默认一样」，省得用户以为没存上。
function equalsDefault(it: SettingItem): boolean {
  const d = valueOfDraft(it, draft(it.key))
  return JSON.stringify(d) === JSON.stringify(it.default)
}

async function save(it: SettingItem): Promise<void> {
  if (!canWrite.value || savingKey.value) return
  const d = draft(it.key)
  const invalid = validateDraft(it, d)
  if (invalid) {
    fieldErrors.value = { ...fieldErrors.value, [it.key]: invalid }
    return
  }
  savingKey.value = it.key
  try {
    const updated = await updateSetting(it.key, valueOfDraft(it, d))
    items.value = items.value.map((x) => (x.key === it.key ? updated : x))
    drafts.value = { ...drafts.value, [it.key]: draftOf(updated) }
    notices.value = { ...notices.value, [it.key]: t('settings.ext.saved') }
  } catch (e) {
    fieldErrors.value = { ...fieldErrors.value, [it.key]: errText(e) }
  } finally {
    savingKey.value = ''
  }
}

function askReset(key: string): void {
  pendingReset.value = key
}

async function doReset(it: SettingItem): Promise<void> {
  if (!canWrite.value || savingKey.value) return
  savingKey.value = it.key
  try {
    await resetSetting(it.key)
    pendingReset.value = ''
    items.value = await listSettings()
    fillDrafts()
    notices.value = { ...notices.value, [it.key]: t('settings.ext.resetDone') }
  } catch (e) {
    fieldErrors.value = { ...fieldErrors.value, [it.key]: errText(e) }
  } finally {
    savingKey.value = ''
  }
}

function toggleDefault(key: string): void {
  showDefault.value = { ...showDefault.value, [key]: !showDefault.value[key] }
}

async function onExport(): Promise<void> {
  pageError.value = ''
  try {
    await exportSettings()
  } catch (e) {
    pageError.value = t('settings.ext.exportFailed', { error: errText(e) })
  }
}

function pickImport(): void {
  importNotice.value = ''
  fileInput.value?.click()
}

async function onImportFile(e: Event): Promise<void> {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  importNotice.value = ''
  importError.value = ''
  importing.value = true
  try {
    const yaml = await file.text()
    const preview = await importSettings(yaml, true)
    importState.value = { name: file.name, yaml, preview }
  } catch (err) {
    importError.value = t('settings.ext.previewFailed', { error: errText(err) })
  } finally {
    importing.value = false
  }
}

async function confirmImport(): Promise<void> {
  const state = importState.value
  if (!state || importing.value) return
  importing.value = true
  importError.value = ''
  try {
    const res = await importSettings(state.yaml, false)
    importState.value = null
    importNotice.value = res.applied
      ? t('settings.ext.importDone', { summary: importSummary(res.changes) })
      : t('settings.ext.importPartial', { summary: importSummary(res.changes) })
    await load()
  } catch (err) {
    importError.value = t('settings.ext.importFailed', { error: errText(err) })
  } finally {
    importing.value = false
  }
}

function closeImport(): void {
  importState.value = null
  importError.value = ''
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="ext" data-test="extension-settings">
    <div class="head">
      <h2 class="title mono">{{ t('settings.ext.title') }}</h2>
      <span class="count mono">{{ t('settings.ext.count', { n: items.length, modified: modifiedCount }) }}</span>
      <span class="spacer" />
      <button class="mini-btn mono" type="button" :disabled="loading" @click="load()">
        {{ loading ? t('settings.ext.refreshing') : t('act.refresh') }}
      </button>
      <button class="mini-btn mono" type="button" :disabled="loading" @click="onExport()">{{ t('settings.ext.export') }}</button>
      <button
        v-if="canWrite"
        class="mini-btn mono"
        type="button"
        :disabled="importing"
        data-test="settings-import"
        @click="pickImport()"
      >
        {{ importing ? t('settings.ext.importing') : t('settings.ext.import') }}
      </button>
      <input
        ref="fileInput"
        class="file"
        type="file"
        accept=".yaml,.yml,text/yaml,application/yaml"
        data-test="settings-import-file"
        @change="onImportFile"
      />
    </div>

    <p class="note mono">
      {{ t('settings.ext.introBefore') }}
      <code>config.yaml</code>{{ t('settings.ext.introAfter') }}
    </p>
    <p v-if="!canWrite" class="readonly mono" data-test="settings-readonly">{{ readOnlyReason }}</p>
    <p v-if="pageError" class="error mono" :title="pageError">{{ pageError }}</p>
    <p v-if="importNotice" class="ok mono">{{ importNotice }}</p>
    <p v-if="importError" class="error mono">{{ importError }}</p>
    <p v-else-if="loading && items.length === 0" class="note mono">{{ t('settings.ext.reading') }}</p>

    <section v-for="g in groups" :key="g.group" class="group" :data-test="`settings-group-${g.group}`">
      <h3 class="group-title mono">{{ g.label }} <span class="prefix">{{ g.group }}.*</span></h3>

      <div v-for="it in g.items" :key="it.key" class="card" :data-test="`setting-${it.key}`">
        <div class="card-head">
          <span class="key mono">{{ it.key }}</span>
          <span class="tag mono">{{ kindLabel(it.kind) }}</span>
          <span v-if="it.modified" class="tag tag--mod mono">{{ t('settings.ext.modified') }}</span>
          <span class="spacer" />
          <button
            v-if="isPrompt(it) || it.kind === 'strings' || it.kind === 'patterns'"
            class="mini-btn mono"
            type="button"
            @click="toggleDefault(it.key)"
          >
            {{ showDefault[it.key] ? t('settings.ext.collapseDefault') : t('settings.ext.compareDefault') }}
          </button>
          <template v-if="canWrite">
            <button
              v-if="pendingReset === it.key"
              class="mini-btn mini-btn--danger mono"
              type="button"
              :disabled="savingKey === it.key"
              @click="doReset(it)"
            >
              {{ t('settings.ext.confirmReset') }}
            </button>
            <button
              v-if="pendingReset === it.key"
              class="mini-btn mono"
              type="button"
              @click="pendingReset = ''"
            >
              {{ t('act.cancel') }}
            </button>
            <button
              v-else
              class="mini-btn mono"
              type="button"
              :disabled="!it.modified || savingKey === it.key"
              :title="it.modified ? t('settings.ext.resetTitle') : t('settings.ext.resetTitleDefault')"
              @click="askReset(it.key)"
            >
              {{ t('settings.ext.reset') }}
            </button>
            <button
              class="mini-btn mini-btn--primary mono"
              type="button"
              :disabled="!isDirty(it) || savingKey === it.key"
              @click="save(it)"
            >
              {{ savingKey === it.key ? t('settings.ext.saving') : t('act.save') }}
            </button>
          </template>
        </div>

        <p class="desc">{{ it.description }}</p>

        <div class="edit">
          <!-- 开关 -->
          <label v-if="controlFor(it) === 'bool'" class="switch">
            <input
              type="checkbox"
              :checked="draft(it.key) === true"
              :disabled="!canWrite"
              @change="setDraft(it.key, checkedOf($event))"
            />
            <span class="mono">{{ draft(it.key) === true ? t('settings.ext.on') : t('settings.ext.off') }}</span>
          </label>

          <!-- 整数：带上限提示 -->
          <div v-else-if="controlFor(it) === 'int'" class="int-row">
            <input
              class="input mono"
              type="number"
              :min="it.min"
              :max="it.max"
              :value="draftText(it.key)"
              :disabled="!canWrite"
              @input="setDraft(it.key, textOf($event))"
            />
            <span class="hint mono">{{ boundsHint(it) }}</span>
          </div>

          <!-- 列表：一行一个 -->
          <div v-else-if="controlFor(it) === 'list'" class="list-row">
            <textarea
              class="input textarea mono"
              rows="4"
              :value="draftText(it.key)"
              :placeholder="it.kind === 'patterns' ? t('settings.ext.patternPlaceholder') : t('settings.ext.listPlaceholder')"
              :disabled="!canWrite"
              @input="setDraft(it.key, textOf($event))"
            />
            <span class="hint mono">
              {{ it.kind === 'patterns' ? t('settings.ext.patternHint') : t('settings.ext.listHint') }} ·
              {{ boundsHint(it) || t('settings.ext.emptyLines') }}
            </span>
          </div>

          <!-- 提示词：大编辑区 -->
          <div v-else-if="controlFor(it) === 'textarea'" class="list-row">
            <textarea
              class="input textarea textarea--prompt mono"
              rows="10"
              :value="draftText(it.key)"
              :placeholder="PROMPT_EMPTY_HINT"
              :disabled="!canWrite"
              @input="setDraft(it.key, textOf($event))"
            />
            <span class="hint mono">{{ PROMPT_EMPTY_HINT }} · {{ boundsHint(it) }}</span>
          </div>

          <!-- 普通字符串 -->
          <div v-else class="int-row">
            <input
              class="input mono"
              type="text"
              :value="draftText(it.key)"
              :disabled="!canWrite"
              @input="setDraft(it.key, textOf($event))"
            />
            <span class="hint mono">{{ boundsHint(it) }}</span>
          </div>
        </div>

        <pre v-if="showDefault[it.key]" class="default mono" data-test="setting-default">{{ defaultText(it) }}</pre>

        <p class="meta mono">
          {{ t('settings.ext.defaultLabel', { value: defaultText(it) }) }}<template v-if="equalsDefault(it)">{{ t('settings.ext.equalsDefault') }}</template>
          <template v-if="it.modified">
            {{ t('settings.ext.lastModified', { by: it.updated_by || t('settings.ext.unknown'), at: fmtDateTime(it.updated_at) }) }}
          </template>
        </p>
        <p v-if="fieldErrors[it.key]" class="field-error mono" :data-test="`setting-error-${it.key}`">
          {{ fieldErrors[it.key] }}
        </p>
        <p v-if="notices[it.key]" class="field-ok mono" :data-test="`setting-notice-${it.key}`">
          {{ notices[it.key] }}
        </p>
      </div>
    </section>

    <!-- 导入预览：先看逐键差异，确认才写。 -->
    <div v-if="importState" class="modal" data-test="settings-import-preview">
      <div class="modal-card">
        <div class="modal-head">
          <span class="mono">{{ t('settings.ext.importPreview', { name: importState.name }) }}</span>
          <span class="spacer" />
          <button class="mini-btn mono" type="button" @click="closeImport()">{{ t('act.close') }}</button>
        </div>
        <p class="note mono">
          {{ t('settings.ext.importPreviewNote', { summary: importSummary(importState.preview.changes) }) }}
        </p>
        <table class="diff mono">
          <thead>
            <tr>
              <th>{{ t('settings.ext.th.key') }}</th>
              <th>{{ t('settings.ext.th.action') }}</th>
              <th>{{ t('settings.ext.th.old') }}</th>
              <th>{{ t('settings.ext.th.new') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in importState.preview.changes" :key="c.key" :class="{ refused: c.error }">
              <td class="brk">{{ c.key }}</td>
              <td>{{ c.error ? t('settings.ext.refused') : importActionLabel(c.action) }}</td>
              <td class="brk">{{ c.action === 'unchanged' ? '—' : valueText(c.old) }}</td>
              <td class="brk">{{ c.error || valueText(c.new) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-if="importError" class="error mono">{{ importError }}</p>
        <div class="modal-actions">
          <button class="mini-btn mono" type="button" @click="closeImport()">{{ t('act.cancel') }}</button>
          <button
            class="mini-btn mini-btn--primary mono"
            type="button"
            :disabled="importing"
            data-test="settings-import-confirm"
            @click="confirmImport()"
          >
            {{ importing ? t('settings.ext.writing') : t('settings.ext.confirmWrite') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ext {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--ink-3);
  margin: 0;
}
.count {
  font-size: 11px;
  color: var(--ink-3);
}
.spacer {
  flex: 1;
}
.file {
  display: none;
}
.note {
  font-size: 12px;
  color: var(--ink-3);
  margin: 0;
}
.readonly {
  font-size: 12px;
  color: var(--ink-3);
  border: 1px dashed var(--line);
  border-radius: var(--r-chip);
  padding: 6px 10px;
  margin: 0;
}
.group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.group-title {
  font-size: 12px;
  color: var(--accent);
  margin: 4px 0 0;
}
.prefix {
  color: var(--ink-3);
  font-size: 11px;
}
.card {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 10px 14px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.card-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.key {
  font-size: 13px;
  color: var(--ink);
}
.tag {
  font-size: 11px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 0 6px;
  color: var(--ink-3);
}
.tag--mod {
  color: var(--done);
  border-color: var(--done);
}
.desc {
  margin: 0;
  font-size: 12px;
  color: var(--ink);
}
.edit {
  min-width: 0;
}
.switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--ink);
  cursor: pointer;
}
.int-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.list-row {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.input {
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 5px 8px;
  font-size: 12px;
  min-width: 0;
}
.input:disabled {
  color: var(--ink-3);
}
.int-row .input {
  width: 120px;
}
.textarea {
  width: 100%;
  resize: vertical;
  line-height: 1.5;
}
.textarea--prompt {
  min-height: 180px;
}
.hint {
  font-size: 11px;
  color: var(--ink-3);
}
.default {
  margin: 0;
  font-size: 11px;
  color: var(--ink-3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 6px 8px;
  max-height: 220px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
}
.meta {
  margin: 0;
  font-size: 11px;
  color: var(--ink-3);
  word-break: break-word;
}
.field-error {
  margin: 0;
  font-size: 12px;
  color: var(--fail);
}
.field-ok {
  margin: 0;
  font-size: 12px;
  color: var(--done);
}
.ok {
  font-size: 12px;
  color: var(--done);
  margin: 0;
}
.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
}
.mini-btn {
  background: transparent;
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 11px;
  cursor: pointer;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--accent);
}
.mini-btn:disabled {
  color: var(--ink-3);
  cursor: default;
}
.mini-btn--primary {
  color: var(--done);
  border-color: var(--done);
}
.mini-btn--danger {
  color: var(--fail);
  border-color: var(--fail);
}
.modal {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  z-index: 40;
}
.modal-card {
  background: var(--bg, #0d0f0d);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 14px;
  width: min(760px, 100%);
  max-height: 86vh;
  overflow: auto;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.modal-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--ink);
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.diff {
  width: 100%;
  border-collapse: collapse;
  font-size: 11px;
}
.diff th,
.diff td {
  border-bottom: 1px solid var(--line);
  padding: 4px 6px;
  text-align: left;
  vertical-align: top;
  color: var(--ink);
}
.diff th {
  color: var(--ink-3);
  font-weight: normal;
}
.diff tr.refused td {
  color: var(--fail);
}
.brk {
  word-break: break-all;
}
@media (max-width: 640px) {
  .int-row .input {
    width: 100%;
  }
}
</style>
