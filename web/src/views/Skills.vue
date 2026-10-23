<script setup lang="ts">
// Skills（JOB-10）：server 技能库的列表 / 详情 / 导入 / 更新 / 删除 / 导出。
// 读是任何 caller 都能做的；导入/更新/删除要 can_admin，服务端 403 的原因直接显示在
// 页内提示条（不吞错）。绑定关系（server / agent / project 三层）在配置页编辑。
import { computed, onMounted, ref } from 'vue'
import { ChevronDown, ChevronRight } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import {
  deleteSkill,
  exportSkill,
  getSkill,
  importSkillSource,
  importSkillZip,
  listSkills,
  updateSkill,
} from '../api/skills'
import { fmtAgo, fmtDateTime } from '../api/time'
import { t } from '../i18n'
import type { Skill, SkillChange, SkillDetail } from '../api/types'

const skills = ref<Skill[]>([])
const loading = ref(false)
const loadError = ref('')
// actionError / notice：写操作的结果（403 无权限、400 校验、导入失败……）与成功回执。
const actionError = ref('')
const notice = ref('')

// selectedName 是展开的那一行：详情接口才带 SKILL.md 正文，故按需拉取并缓存最后一次结果。
const selectedName = ref('')
const detail = ref<SkillDetail | null>(null)
const detailLoading = ref(false)
const detailError = ref('')

// busy 是正在跑的写操作（'import' 或技能名）：按钮据此禁用，避免重复提交。
const busy = ref('')
const zipFile = ref<File | null>(null)
// zipInput 是 file input 本身：导入成功后要清掉它的 value，否则再选同一个文件不会再
// 触发 change（浏览器认为值没变），用户会以为按钮坏了。
const zipInput = ref<HTMLInputElement | null>(null)
const sourceSpec = ref('')
// change 挂在触发它的技能名上：更新完列表可能整行换过，diff 仍要指着那一行。
const change = ref<SkillChange | null>(null)
const changeFor = ref('')

const hasSkills = computed(() => skills.value.length > 0)

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

// fmtSize：列里的体积（B/KB/MB）；后端 omitempty 省略 0 字节。
function fmtSize(bytes: number | undefined): string {
  const n = bytes ?? 0
  if (n < 1024) {
    return `${n} B`
  }
  if (n < 1024 * 1024) {
    return `${(n / 1024).toFixed(1)} KB`
  }
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

// shortHash：缩略一串 sha256（version 与每个文件的 sha 都用它），完整值挂在 title 里。
function shortHash(v?: string): string {
  return v ? `${v.slice(0, 12)}…` : '—'
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const resp = await listSkills()
    skills.value = resp.skills ?? []
    if (selectedName.value && !skills.value.some((s) => s.name === selectedName.value)) {
      // 更新/删除后那一行可能已经不存在了：收起详情，别留一个悬空的展开块。
      closeDetail()
    }
  } catch (e) {
    loadError.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}

function closeDetail(): void {
  selectedName.value = ''
  detail.value = null
  detailError.value = ''
}

async function openDetail(name: string): Promise<void> {
  selectedName.value = name
  detail.value = null
  detailError.value = ''
  detailLoading.value = true
  try {
    detail.value = await getSkill(name)
  } catch (e) {
    detailError.value = errorMessage(e)
  } finally {
    detailLoading.value = false
  }
}

function toggleDetail(name: string): void {
  if (selectedName.value === name) {
    closeDetail()
    return
  }
  void openDetail(name)
}

function onZipPicked(e: Event): void {
  const input = e.target as HTMLInputElement
  zipFile.value = input.files?.[0] ?? null
  actionError.value = ''
}

async function runImportZip(): Promise<void> {
  if (busy.value !== '' || zipFile.value === null) {
    return
  }
  const file = zipFile.value
  busy.value = 'import'
  actionError.value = ''
  notice.value = ''
  try {
    const resp = await importSkillZip(file)
    notice.value = t('settings.skills.imported', {
      name: resp.skill.name,
      kind: resp.replaced ? t('settings.skills.replaced') : t('settings.skills.added'),
      source: file.name,
    })
    zipFile.value = null
    if (zipInput.value) {
      zipInput.value.value = ''
    }
    await load()
    await openDetail(resp.skill.name)
  } catch (e) {
    actionError.value = t('settings.skills.importFailed', { error: errorMessage(e) })
  } finally {
    busy.value = ''
  }
}

async function runImportSource(): Promise<void> {
  const spec = sourceSpec.value.trim()
  if (busy.value !== '' || spec === '') {
    return
  }
  busy.value = 'import'
  actionError.value = ''
  notice.value = ''
  try {
    const resp = await importSkillSource(spec)
    notice.value = t('settings.skills.imported', {
      name: resp.skill.name,
      kind: resp.replaced ? t('settings.skills.replaced') : t('settings.skills.added'),
      source: spec,
    })
    sourceSpec.value = ''
    await load()
    await openDetail(resp.skill.name)
  } catch (e) {
    actionError.value = t('settings.skills.importFailed', { error: errorMessage(e) })
  } finally {
    busy.value = ''
  }
}

async function runUpdate(name: string): Promise<void> {
  if (busy.value !== '') {
    return
  }
  busy.value = name
  actionError.value = ''
  notice.value = ''
  change.value = null
  changeFor.value = ''
  try {
    const resp = await updateSkill(name)
    change.value = resp.change
    changeFor.value = name
    const n =
      (resp.change.added?.length ?? 0) +
      (resp.change.changed?.length ?? 0) +
      (resp.change.removed?.length ?? 0)
    notice.value = n === 0 ? t('settings.skills.upToDate', { name }) : t('settings.skills.updated', { name, n })
    await load()
    // 展开这一行：diff 显示在它的详情块里（不展开的话，用户只看到提示条里的一句计数）。
    await openDetail(name)
  } catch (e) {
    actionError.value = t('settings.skills.updateFailed', { name, error: errorMessage(e) })
  } finally {
    busy.value = ''
  }
}

async function runDelete(s: Skill): Promise<void> {
  if (busy.value !== '') {
    return
  }
  if (!window.confirm(t('settings.skills.confirmDelete', { name: s.name }))) {
    return
  }
  busy.value = s.name
  actionError.value = ''
  notice.value = ''
  try {
    await deleteSkill(s.name)
    notice.value = t('settings.skills.deleted', { name: s.name })
    if (changeFor.value === s.name) {
      change.value = null
      changeFor.value = ''
    }
    if (selectedName.value === s.name) {
      closeDetail()
    }
    await load()
  } catch (e) {
    actionError.value = `删除 ${s.name} 失败：${errorMessage(e)}`
  } finally {
    busy.value = ''
  }
}

async function runExport(name: string): Promise<void> {
  if (busy.value !== '') {
    return
  }
  busy.value = name
  actionError.value = ''
  notice.value = ''
  try {
    await exportSkill(name)
    notice.value = t('settings.skills.exported', { name })
  } catch (e) {
    actionError.value = t('settings.skills.exportFailed', { name, error: errorMessage(e) })
  } finally {
    busy.value = ''
  }
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="skills">
    <div class="head">
      <h1 class="title mono">{{ t('settings.skills.title') }}</h1>
      <button class="mini-btn mono" type="button" :disabled="loading" @click="load()">
        {{ loading ? t('settings.skills.refreshing') : t('act.refresh') }}
      </button>
      <span class="poll-hint mono" :class="{ 'poll-hint--on': loading }">●</span>
    </div>

    <p class="scope-note mono">
      {{ t('settings.skills.scopeBefore') }}
      <RouterLink to="/settings/config">{{ t('settings.skills.scopeLink') }}</RouterLink>{{ t('settings.skills.scopeAfter') }}
    </p>

    <p v-if="loadError" class="error mono">{{ loadError }}</p>
    <p v-if="actionError" class="error mono">{{ actionError }}</p>
    <p v-if="notice" class="notice mono">{{ notice }}</p>

    <section class="panel import-panel">
      <h2 class="panel-title mono">{{ t('settings.skills.import') }}</h2>
      <div class="import-row">
        <label class="field">
          <span class="field-name">{{ t('settings.skills.zipLabel') }}</span>
          <input ref="zipInput" class="input" type="file" accept=".zip" @change="onZipPicked" />
        </label>
        <button
          class="mini-btn mono"
          type="button"
          :disabled="busy !== '' || zipFile === null"
          @click="runImportZip()"
        >
          {{ busy === 'import' ? t('settings.skills.importing') : t('settings.skills.importZip') }}
        </button>

        <label class="field field--spec">
          <span class="field-name">{{ t('settings.skills.sourceLabel') }}</span>
          <input
            v-model.trim="sourceSpec"
            class="input mono"
            type="text"
            placeholder="D:\path\to\skill  /  https://…/x.zip  /  git+https://…#subdir"
            @keydown.enter.prevent="runImportSource()"
          />
        </label>
        <button
          class="mini-btn mono"
          type="button"
          :disabled="busy !== '' || sourceSpec === ''"
          @click="runImportSource()"
        >
          {{ busy === 'import' ? t('settings.skills.importing') : t('settings.skills.importSource') }}
        </button>
      </div>
      <p class="hint mono">{{ t('settings.skills.importHint') }}</p>
    </section>

    <div class="table">
      <div class="thead mono">
        <span>{{ t('settings.skills.col.name') }}</span>
        <span>{{ t('settings.skills.col.desc') }}</span>
        <span>{{ t('settings.skills.col.version') }}</span>
        <span>{{ t('settings.skills.col.size') }}</span>
        <span>{{ t('settings.skills.col.updated') }}</span>
        <span>{{ t('settings.skills.col.actions') }}</span>
      </div>

      <template v-for="s in skills" :key="s.name">
        <div class="trow" @click="toggleDetail(s.name)">
          <span class="col-name mono">
            <button
              class="name-btn mono"
              type="button"
              :aria-expanded="selectedName === s.name"
              :aria-controls="`skill-detail-${s.name}`"
              :title="selectedName === s.name ? t('settings.skills.collapseDetail') : t('settings.skills.expandDetail')"
              @click.stop="toggleDetail(s.name)"
            >
              <Icon class="chev" :icon="selectedName === s.name ? ChevronDown : ChevronRight" size="sm" />
              <span class="name-text">{{ s.name }}</span>
            </button>
          </span>
          <span class="col-desc mono" :title="s.description || ''">{{ s.description || '—' }}</span>
          <span class="col-version mono" :title="s.version || ''">{{ shortHash(s.version) }}</span>
          <span class="col-size mono">{{ fmtSize(s.size) }}</span>
          <span class="col-updated mono">
            <span>{{ fmtDateTime(s.updated_at) }}</span>
            <small>{{ fmtAgo(s.updated_at) }}</small>
          </span>
          <span class="col-actions">
            <button
              class="mini-btn mono"
              type="button"
              :disabled="busy !== ''"
              :title="t('settings.skills.updateTitle')"
              @click.stop="runUpdate(s.name)"
            >
              {{ t('settings.skills.update') }}
            </button>
            <button
              class="mini-btn mono"
              type="button"
              :disabled="busy !== ''"
              :title="t('settings.skills.exportTitle')"
              @click.stop="runExport(s.name)"
            >
              {{ t('settings.skills.export') }}
            </button>
            <button
              class="mini-btn mini-btn--danger mono"
              type="button"
              :disabled="busy !== ''"
              @click.stop="runDelete(s)"
            >
              {{ t('settings.skills.delete') }}
            </button>
          </span>
        </div>

        <div v-if="selectedName === s.name" :id="`skill-detail-${s.name}`" class="detail mono">
          <p v-if="detailLoading" class="muted">{{ t('settings.skills.detailLoading') }}</p>
          <p v-else-if="detailError" class="error">{{ detailError }}</p>
          <template v-else-if="detail">
            <div class="detail-grid">
              <span class="dk">source</span><span class="dv">{{ detail.source || '—' }}</span>
              <span class="dk">source_ref</span><span class="dv">{{ detail.source_ref || '—' }}</span>
              <span class="dk">version</span><span class="dv">{{ detail.version || '—' }}</span>
              <span class="dk">size</span><span class="dv">{{ fmtSize(detail.size) }}</span>
              <span class="dk">updated_at</span>
              <span class="dv">{{ fmtDateTime(detail.updated_at) }} · {{ detail.updated_by || '—' }}</span>
            </div>

            <div v-if="changeFor === s.name && change" class="change">
              <div class="detail-head">{{ t('settings.skills.thisUpdate') }}</div>
              <div class="change-lines">
                <span v-for="p in change.added ?? []" :key="`a:${p}`" class="change-line change-line--add">+ {{ p }}</span>
                <span v-for="p in change.changed ?? []" :key="`c:${p}`" class="change-line change-line--mod">~ {{ p }}</span>
                <span v-for="p in change.removed ?? []" :key="`r:${p}`" class="change-line change-line--del">- {{ p }}</span>
                <span
                  v-if="!(change.added?.length ?? 0) && !(change.changed?.length ?? 0) && !(change.removed?.length ?? 0)"
                  class="muted"
                >{{ t('settings.skills.noChange') }}</span>
              </div>
            </div>

            <div class="detail-head">
              {{ t('settings.skills.files', { n: detail.files?.length ?? 0 }) }}
            </div>
            <div class="files">
              <div v-for="f in detail.files ?? []" :key="f.path" class="file-row">
                <span class="file-path">{{ f.path }}</span>
                <span class="file-size">{{ fmtSize(f.size) }}</span>
                <span class="file-sha" :title="f.sha256">{{ shortHash(f.sha256) }}</span>
              </div>
              <div v-if="!(detail.files?.length ?? 0)" class="muted">{{ t('settings.skills.noFiles') }}</div>
            </div>

            <div class="detail-head">SKILL.md</div>
            <pre class="skill-md">{{ detail.content }}</pre>
          </template>
        </div>
      </template>

      <div v-if="!hasSkills && !loadError && !loading" class="empty mono">{{ t('settings.skills.empty') }}</div>
      <div v-if="loading && !hasSkills" class="empty mono">{{ t('settings.skills.loading') }}</div>
    </div>
  </div>
</template>

<style scoped>
.skills {
  max-width: 1160px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--ink);
  margin: 0;
}
.head .mini-btn {
  margin-left: 0;
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-hint--on {
  color: var(--accent);
}
.scope-note {
  color: var(--ink-3);
  font-size: 12px;
  margin: 0 0 12px;
}
.scope-note a {
  color: var(--accent);
}

.error,
.notice {
  font-size: 12px;
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0 0 10px;
  word-break: break-word;
}
.error {
  color: var(--fail);
  border: 1px solid var(--fail);
}
.notice {
  color: var(--done);
  border: 1px solid var(--done);
}
.muted {
  color: var(--ink-3);
}

.panel {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 12px 14px;
}
.import-panel {
  margin-bottom: 14px;
}
.panel-title {
  font-size: 13px;
  letter-spacing: 0.04em;
  color: var(--ink);
  margin: 0 0 10px;
}
.import-row {
  display: flex;
  align-items: flex-end;
  flex-wrap: wrap;
  gap: 10px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 11px;
}
.field--spec {
  flex: 1;
  min-width: 260px;
}
.field-name {
  color: var(--ink-3);
}
.input {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 5px 8px;
  font-family: inherit;
  font-size: 12px;
}
.input:focus {
  border-color: var(--accent);
}
.hint {
  font-size: 11px;
  color: var(--ink-3);
  margin: 8px 0 0;
  line-height: 1.6;
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
.mini-btn--danger {
  color: var(--fail);
}
.mini-btn--danger:hover:not(:disabled) {
  border-color: var(--fail);
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  overflow: hidden;
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 200px minmax(0, 1fr) 130px 90px 150px 190px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.thead {
  background: var(--surface);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--ink-3);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: 13px;
  cursor: pointer;
  outline: none;
}
.trow:hover {
  background: var(--surface);
}
.col-name {
  display: inline-flex;
  align-items: center;
  min-width: 0;
}
.name-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  background: transparent;
  border: 0;
  color: var(--accent);
  cursor: pointer;
  font-size: 13px;
  padding: 0;
  overflow: hidden;
}
.name-btn:hover .name-text,
.name-btn:focus-visible .name-text {
  text-decoration: underline;
}
.name-btn:focus-visible {
  outline: 1px solid var(--accent);
  outline-offset: 2px;
  border-radius: 2px;
}
.chev {
  color: var(--ink-3);
  flex: none;
  font-size: 12px;
  line-height: 1;
}
.name-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-desc {
  color: var(--ink-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-version,
.col-size {
  color: var(--ink);
  font-size: 12px;
}
.col-updated {
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-size: 12px;
  color: var(--ink);
}
.col-updated small {
  color: var(--ink-3);
  font-size: 11px;
}
.col-actions {
  display: inline-flex;
  gap: 6px;
}

.detail {
  border-bottom: 1px solid var(--line);
  background: var(--bg);
  padding: 12px 14px 14px 40px;
  font-size: 12px;
}
.detail-grid {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 4px 12px;
  align-items: baseline;
  margin-bottom: 12px;
}
.dk {
  color: var(--ink-3);
  font-size: 11px;
}
.dv {
  color: var(--ink);
  overflow-wrap: anywhere;
}
.detail-head {
  color: var(--ink-3);
  font-size: 11px;
  letter-spacing: 0.06em;
  margin: 12px 0 6px;
}
.change {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 8px 10px;
}
.change-lines {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;
}
.change-line--add {
  color: var(--done);
}
.change-line--mod {
  color: var(--run);
}
.change-line--del {
  color: var(--fail);
}
.files {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
}
.file-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 90px 140px;
  gap: 10px;
  padding: 5px 10px;
  border-top: 1px solid var(--line);
  font-size: 12px;
}
.file-row:first-child {
  border-top: none;
}
.file-path {
  color: var(--ink);
  overflow-wrap: anywhere;
}
.file-size,
.file-sha {
  color: var(--ink-3);
}
.skill-md {
  margin: 0;
  padding: 10px 12px;
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink);
  font-size: 12px;
  line-height: 1.6;
  max-height: 520px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
}

.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--ink-3);
  font-size: 13px;
}

@media (max-width: 940px) {
  .thead {
    display: none;
  }
  .trow {
    grid-template-columns: minmax(0, 1fr) 150px;
    grid-template-areas:
      'name actions'
      'desc desc'
      'version updated'
      'size updated';
    row-gap: 4px;
  }
  .col-name {
    grid-area: name;
  }
  .col-desc {
    grid-area: desc;
  }
  .col-version {
    grid-area: version;
  }
  .col-size {
    grid-area: size;
  }
  .col-updated {
    grid-area: updated;
  }
  .col-actions {
    grid-area: actions;
    justify-content: flex-end;
    flex-wrap: wrap;
  }
  .detail {
    padding-left: 14px;
  }
  .file-row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
