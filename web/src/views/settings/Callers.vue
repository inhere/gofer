<script setup lang="ts">
// 设置 → 用户（配置入库 P1）：根管理员在这里新增 / 轮换 / 停用 / 启用调用方，改上限与配额，
// 并查看改动审计。新增、发 token、改权限都不用改配置文件、不用重启（`server.callers` 是热载）。
//
// 两条安全约定：
// - token 只在「新增」和「轮换」的响应里出现一次。它只活在这个组件的 ref 里，关掉提示就没了；
//   不写浏览器本地存储，不写 URL，不进日志。之后只能用 token_hint 区分。
// - 配置文件里声明的用户（source=file）不能在这里轮换 / 停用 / 启用（服务端 409，会把明文写回
//   配置文件）；它的上限与配额可以改，写回文件，界面因此提示 `server:` 块的注释会丢。
import { computed, onMounted, reactive, ref } from 'vue'
import {
  addCaller,
  disableCaller,
  enableCaller,
  listCallerAudit,
  listCallers,
  rotateCaller,
  setCallerCeiling,
  setCallerLimits,
} from '../../api/callers'
import { fmtDateTime } from '../../api/time'
import type { CallerAuditEntry, CallerCeilingView, CallerView } from '../../api/types'
import { t } from '../../i18n'
import { useWhoami } from '../../store/whoami'
import FormSection from '../../ui/FormSection.vue'
import {
  addBlocker,
  canDisable,
  canEnable,
  canRotate,
  ceilingBlocker,
  ceilingFromForm,
  ceilingModeText,
  ceilingSummaryLines,
  ceilingToForm,
  emptyCeilingForm,
  fileSourceNote,
  isFileCaller,
  limitBlocker,
  quotaText,
  sourceText,
  stateText,
  stateTone,
} from '../../utils/callers'
import type { CeilingMode } from '../../utils/callers'

const { isRoot } = useWhoami()

const rows = ref<CallerView[]>([])
const audit = ref<CallerAuditEntry[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const notice = ref('')
const warnings = ref<string[]>([])
const busyId = ref('')

// reveal 是「刚拿到的 token」：只有新增与轮换会填它，closeReveal 之后引用清空，页面上不再可见。
const reveal = ref<{ id: string; token: string } | null>(null)
const copied = ref(false)

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function showWarnings(list?: string[]): void {
  warnings.value = list ?? []
}

async function load(): Promise<void> {
  if (!isRoot.value) return
  loading.value = true
  error.value = ''
  try {
    const r = await listCallers()
    rows.value = r.callers ?? []
    loaded.value = true
    audit.value = (await listCallerAudit(50)).entries ?? []
  } catch (e) {
    error.value = msg(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})

function closeReveal(): void {
  reveal.value = null
  copied.value = false
}

async function copyToken(): Promise<void> {
  const token = reveal.value?.token
  if (!token) return
  try {
    await navigator.clipboard.writeText(token)
    copied.value = true
  } catch {
    copied.value = false
    notice.value = t('settings.callers.copyFailed')
  }
}

// ---- 动作（轮换 / 停用 / 启用）----
async function onRotate(v: CallerView): Promise<void> {
  if (!window.confirm(t('settings.callers.confirmRotate', { id: v.id }))) return
  await act(v.id, async () => {
    const change = await rotateCaller(v.id)
    if (change.token) reveal.value = { id: change.caller.id, token: change.token }
    notice.value = t('settings.callers.rotated', { id: v.id })
    showWarnings(change.warnings)
  })
}

async function onDisable(v: CallerView): Promise<void> {
  if (!window.confirm(t('settings.callers.confirmDisable', { id: v.id }))) return
  await act(v.id, async () => {
    const change = await disableCaller(v.id)
    notice.value = t('settings.callers.disabled', { id: v.id })
    showWarnings(change.warnings)
  })
}

async function onEnable(v: CallerView): Promise<void> {
  await act(v.id, async () => {
    const change = await enableCaller(v.id)
    notice.value = t('settings.callers.enabled', { id: v.id })
    showWarnings(change.warnings)
  })
}

async function act(id: string, fn: () => Promise<void>): Promise<void> {
  busyId.value = id
  error.value = ''
  notice.value = ''
  warnings.value = []
  try {
    await fn()
    await load()
  } catch (e) {
    error.value = msg(e)
  } finally {
    busyId.value = ''
  }
}

// ---- 新增 ----
const addOpen = ref(false)
const addState = reactive({ id: '', name: '', maxConcurrent: '', rateLimit: '', rateBurst: '' })
const addMode = ref<CeilingMode>('limited')
const addCeiling = reactive(emptyCeilingForm())
const addError = computed(() =>
  addBlocker({ id: addState.id, mode: addMode.value, form: addCeiling }),
)

function resetAdd(): void {
  addState.id = ''
  addState.name = ''
  addState.maxConcurrent = ''
  addState.rateLimit = ''
  addState.rateBurst = ''
  addMode.value = 'limited'
  Object.assign(addCeiling, emptyCeilingForm())
}

async function onCreate(): Promise<void> {
  if (addError.value) return
  const id = addState.id.trim()
  busyId.value = id
  error.value = ''
  notice.value = ''
  warnings.value = []
  try {
    const change = await addCaller({
      id,
      name: addState.name.trim(),
      max_concurrent_jobs: Number(addState.maxConcurrent.trim() || 0),
      rate_limit: Number(addState.rateLimit.trim() || 0),
      rate_burst: Number(addState.rateBurst.trim() || 0),
      ceiling: addMode.value === 'limited' ? (ceilingFromForm(addCeiling) ?? undefined) : undefined,
      no_ceiling: addMode.value === 'none',
    })
    if (change.token) reveal.value = { id: change.caller.id, token: change.token }
    notice.value = t('settings.callers.added', { id: change.caller.id })
    showWarnings(change.warnings)
    addOpen.value = false
    resetAdd()
    await load()
  } catch (e) {
    error.value = msg(e)
  } finally {
    busyId.value = ''
  }
}

// ---- 改上限 / 改配额（一次只开一个编辑面板）----
const panel = ref<{ id: string; kind: 'ceiling' | 'limits' } | null>(null)
const editMode = ref<CeilingMode>('limited')
const editCeiling = reactive(emptyCeilingForm())
const editLimits = reactive({ maxConcurrent: '', rateLimit: '', rateBurst: '' })
const panelBlocker = computed(() =>
  panel.value?.kind === 'ceiling'
    ? ceilingBlocker(editMode.value, editCeiling)
    : limitBlocker(editLimits.maxConcurrent, editLimits.rateLimit, editLimits.rateBurst),
)
const panelRow = computed(() => rows.value.find((r) => r.id === panel.value?.id) ?? null)

function openPanel(v: CallerView, kind: 'ceiling' | 'limits'): void {
  panel.value = { id: v.id, kind }
  notice.value = ''
  warnings.value = []
  error.value = ''
  if (kind === 'ceiling') {
    editMode.value = v.ceiling ? 'limited' : 'none'
    Object.assign(editCeiling, ceilingToForm(v.ceiling))
  } else {
    editLimits.maxConcurrent = v.max_concurrent_jobs ? String(v.max_concurrent_jobs) : ''
    editLimits.rateLimit = v.rate_limit ? String(v.rate_limit) : ''
    editLimits.rateBurst = v.rate_burst ? String(v.rate_burst) : ''
  }
}

function closePanel(): void {
  panel.value = null
}

async function onSavePanel(): Promise<void> {
  const p = panel.value
  if (!p || panelBlocker.value) return
  await act(p.id, async () => {
    if (p.kind === 'ceiling') {
      const ceil = editMode.value === 'limited' ? ceilingFromForm(editCeiling) : null
      const change = ceil
        ? await setCallerCeiling(p.id, { ceiling: ceil })
        : await setCallerCeiling(p.id, { no_ceiling: true })
      notice.value = t('settings.callers.ceilingSaved', { id: p.id })
      showWarnings(change.warnings)
    } else {
      const change = await setCallerLimits(p.id, {
        max_concurrent_jobs: Number(editLimits.maxConcurrent.trim() || 0),
        rate_limit: Number(editLimits.rateLimit.trim() || 0),
        rate_burst: Number(editLimits.rateBurst.trim() || 0),
      })
      notice.value = t('settings.callers.limitsSaved', { id: p.id })
      showWarnings(change.warnings)
    }
    panel.value = null
  })
}

function ceilingOf(v: CallerView): CallerCeilingView | null {
  return v.ceiling ?? null
}
</script>

<template>
  <div class="callers">
    <header class="page-head">
      <div class="grow">
        <h1>{{ t('settings.callers.title') }}</h1>
        <p class="sub">{{ t('settings.callers.sub') }}</p>
      </div>
      <button v-if="isRoot" class="btn" type="button" :disabled="loading" @click="load()">
        {{ loading ? t('settings.callers.refreshing') : t('act.refresh') }}
      </button>
    </header>

    <p v-if="!isRoot" class="msg" data-testid="callers-root-only">{{ t('settings.callers.rootOnly') }}</p>

    <template v-else>
      <p v-if="error" class="msg msg--err" :title="error">{{ t('settings.callers.failed', { error }) }}</p>
      <p v-if="notice" class="msg msg--ok">{{ notice }}</p>
      <p v-for="w in warnings" :key="w" class="msg msg--warn">{{ w }}</p>

      <!-- 刚拿到的 token：只在这里显示一次，关掉就没有了 -->
      <div v-if="reveal" class="reveal" data-testid="caller-token">
        <p class="reveal-title">{{ t('settings.callers.tokenOnce.title', { id: reveal.id }) }}</p>
        <code class="reveal-token" data-testid="caller-token-value">{{ reveal.token }}</code>
        <div class="reveal-actions">
          <button class="btn" type="button" @click="copyToken()">
            {{ copied ? t('settings.callers.tokenOnce.copied') : t('settings.callers.tokenOnce.copy') }}
          </button>
          <button class="btn" type="button" data-testid="caller-token-close" @click="closeReveal()">
            {{ t('settings.callers.tokenOnce.close') }}
          </button>
        </div>
        <p class="reveal-hint">{{ t('settings.callers.tokenOnce.hint') }}</p>
      </div>

      <p class="note">{{ t('settings.callers.intro') }}</p>

      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>ID</th>
              <th>{{ t('settings.callers.col.name') }}</th>
              <th>{{ t('settings.callers.col.source') }}</th>
              <th>{{ t('settings.callers.col.state') }}</th>
              <th>token</th>
              <th>{{ t('settings.callers.col.ceiling') }}</th>
              <th>{{ t('settings.callers.col.quota') }}</th>
              <th>{{ t('settings.callers.col.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="v in rows" :key="v.id" :data-caller="v.id">
              <td class="mono">{{ v.id }}</td>
              <td>{{ v.name || '-' }}</td>
              <td>
                <span class="tag" :class="isFileCaller(v) ? 'tag--file' : 'tag--db'">{{ sourceText(v.source) }}</span>
              </td>
              <td>
                <span class="tag" :class="`tag--${stateTone(v.state)}`">{{ stateText(v.state) }}</span>
              </td>
              <td class="mono">{{ v.token_hint || (v.token_set ? t('settings.callers.tokenSet') : '-') }}</td>
              <td>
                <span>{{ ceilingModeText(ceilingOf(v)) }}</span>
                <ul v-if="ceilingSummaryLines(ceilingOf(v)).length > 0" class="ceiling">
                  <li v-for="line in ceilingSummaryLines(ceilingOf(v))" :key="line">{{ line }}</li>
                </ul>
              </td>
              <td class="mono">{{ quotaText(v) }}</td>
              <td class="acts">
                <button
                  class="btn btn--sm"
                  type="button"
                  :disabled="!canRotate(v) || busyId === v.id"
                  :title="isFileCaller(v) ? fileSourceNote() : ''"
                  @click="onRotate(v)"
                >
                  {{ t('settings.callers.act.rotate') }}
                </button>
                <button
                  v-if="v.state !== 'active'"
                  class="btn btn--sm"
                  type="button"
                  :disabled="!canEnable(v) || busyId === v.id"
                  :title="isFileCaller(v) ? fileSourceNote() : ''"
                  @click="onEnable(v)"
                >
                  {{ t('settings.callers.act.enable') }}
                </button>
                <button
                  v-else
                  class="btn btn--sm"
                  type="button"
                  :disabled="!canDisable(v) || busyId === v.id"
                  :title="isFileCaller(v) ? fileSourceNote() : ''"
                  @click="onDisable(v)"
                >
                  {{ t('settings.callers.act.disable') }}
                </button>
                <button class="btn btn--sm" type="button" @click="openPanel(v, 'ceiling')">
                  {{ t('settings.callers.act.ceiling') }}
                </button>
                <button class="btn btn--sm" type="button" @click="openPanel(v, 'limits')">
                  {{ t('settings.callers.act.limits') }}
                </button>
              </td>
            </tr>
            <tr v-if="loaded && rows.length === 0">
              <td colspan="8">{{ t('settings.callers.empty') }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- 改上限 / 改配额 -->
      <FormSection
        v-if="panel && panelRow"
        :title="t('settings.callers.panel.title', { id: panel.id })"
        :desc="isFileCaller(panelRow) ? t('settings.callers.panel.fileDesc') : t('settings.callers.panel.desc')"
      >
        <template v-if="panel.kind === 'ceiling'">
          <label class="field">
            <span>{{ t('settings.callers.field.mode') }}</span>
            <select v-model="editMode" class="mono">
              <option value="limited">{{ t('settings.callers.ceilingLimited') }}</option>
              <option value="none">{{ t('settings.callers.ceilingNone') }}</option>
            </select>
          </label>
          <template v-if="editMode === 'limited'">
            <label class="field">
              <span>{{ t('settings.callers.field.projects') }}</span>
              <input v-model="editCeiling.projects" class="mono" :placeholder="t('settings.callers.ph.list')" />
            </label>
            <label class="field">
              <span>{{ t('settings.callers.field.runners') }}</span>
              <input v-model="editCeiling.runners" class="mono" :placeholder="t('settings.callers.ph.runners')" />
              <small>{{ t('settings.callers.hint.runners') }}</small>
            </label>
            <label class="field">
              <span>{{ t('settings.callers.field.workers') }}</span>
              <input v-model="editCeiling.workers" class="mono" :placeholder="t('settings.callers.ph.list')" />
            </label>
            <label class="field">
              <span>{{ t('settings.callers.field.agents') }}</span>
              <input v-model="editCeiling.agents" class="mono" :placeholder="t('settings.callers.ph.list')" />
            </label>
            <label class="check"><input v-model="editCeiling.allowExec" type="checkbox" /> {{ t('settings.callers.field.allowExec') }}</label>
            <label class="check"><input v-model="editCeiling.globalMemory" type="checkbox" /> {{ t('settings.callers.field.globalMemory') }}</label>
            <label class="check"><input v-model="editCeiling.readOnly" type="checkbox" /> {{ t('settings.callers.field.readOnly') }}</label>
          </template>
        </template>
        <template v-else>
          <label class="field">
            <span>{{ t('settings.callers.field.maxConcurrent') }}</span>
            <input v-model="editLimits.maxConcurrent" class="mono" inputmode="numeric" />
          </label>
          <label class="field">
            <span>{{ t('settings.callers.field.rateLimit') }}</span>
            <input v-model="editLimits.rateLimit" class="mono" inputmode="decimal" />
          </label>
          <label class="field">
            <span>{{ t('settings.callers.field.rateBurst') }}</span>
            <input v-model="editLimits.rateBurst" class="mono" inputmode="numeric" />
          </label>
          <small>{{ t('settings.callers.hint.quotaZero') }}</small>
        </template>

        <p v-if="panelBlocker" class="msg msg--err">{{ panelBlocker }}</p>
        <div class="row-actions">
          <button class="btn" type="button" :disabled="!!panelBlocker || busyId === panel.id" @click="onSavePanel()">
            {{ t('act.save') }}
          </button>
          <button class="btn" type="button" @click="closePanel()">{{ t('act.cancel') }}</button>
        </div>
      </FormSection>

      <!-- 新增 -->
      <FormSection :title="t('settings.callers.add.title')" :desc="t('settings.callers.add.desc')">
        <p v-if="!addOpen" class="row-actions">
          <button class="btn" type="button" data-testid="caller-add-open" @click="addOpen = true">
            {{ t('settings.callers.add.open') }}
          </button>
        </p>
        <template v-else>
          <label class="field">
            <span>{{ t('settings.callers.field.id') }}</span>
            <input v-model="addState.id" class="mono" :placeholder="t('settings.callers.ph.id')" />
          </label>
          <label class="field">
            <span>{{ t('settings.callers.col.name') }}</span>
            <input v-model="addState.name" :placeholder="t('settings.callers.ph.name')" />
          </label>
          <label class="field">
            <span>{{ t('settings.callers.field.mode') }}</span>
            <select v-model="addMode" class="mono" data-testid="caller-add-mode">
              <option value="limited">{{ t('settings.callers.ceilingLimited') }}</option>
              <option value="none">{{ t('settings.callers.ceilingNone') }}</option>
            </select>
            <small>{{ t('settings.callers.hint.mode') }}</small>
          </label>
          <template v-if="addMode === 'limited'">
            <label class="field">
              <span>{{ t('settings.callers.field.projects') }}</span>
              <input v-model="addCeiling.projects" class="mono" :placeholder="t('settings.callers.ph.list')" />
            </label>
            <label class="field">
              <span>{{ t('settings.callers.field.runners') }}</span>
              <input v-model="addCeiling.runners" class="mono" :placeholder="t('settings.callers.ph.runners')" />
              <small>{{ t('settings.callers.hint.runners') }}</small>
            </label>
            <label class="field">
              <span>{{ t('settings.callers.field.workers') }}</span>
              <input v-model="addCeiling.workers" class="mono" :placeholder="t('settings.callers.ph.list')" />
            </label>
            <label class="field">
              <span>{{ t('settings.callers.field.agents') }}</span>
              <input v-model="addCeiling.agents" class="mono" :placeholder="t('settings.callers.ph.list')" />
            </label>
            <label class="check"><input v-model="addCeiling.allowExec" type="checkbox" /> {{ t('settings.callers.field.allowExec') }}</label>
            <label class="check"><input v-model="addCeiling.globalMemory" type="checkbox" /> {{ t('settings.callers.field.globalMemory') }}</label>
            <label class="check"><input v-model="addCeiling.readOnly" type="checkbox" /> {{ t('settings.callers.field.readOnly') }}</label>
          </template>
          <label class="field">
            <span>{{ t('settings.callers.field.maxConcurrent') }}</span>
            <input v-model="addState.maxConcurrent" class="mono" inputmode="numeric" />
          </label>
          <label class="field">
            <span>{{ t('settings.callers.field.rateLimit') }}</span>
            <input v-model="addState.rateLimit" class="mono" inputmode="decimal" />
          </label>
          <label class="field">
            <span>{{ t('settings.callers.field.rateBurst') }}</span>
            <input v-model="addState.rateBurst" class="mono" inputmode="numeric" />
          </label>
          <small>{{ t('settings.callers.hint.quotaZero') }}</small>
          <p v-if="addError" class="msg msg--err">{{ addError }}</p>
          <div class="row-actions">
            <button class="btn" type="button" data-testid="caller-add-submit" :disabled="!!addError" @click="onCreate()">
              {{ t('settings.callers.add.submit') }}
            </button>
            <button class="btn" type="button" @click="addOpen = false">{{ t('act.cancel') }}</button>
          </div>
        </template>
      </FormSection>

      <!-- 审计 -->
      <FormSection :title="t('settings.callers.audit.title')" :desc="t('settings.callers.audit.desc')">
        <ul class="audit">
          <li v-for="e in audit" :key="e.id ?? `${e.path}-${e.at}`" class="mono">
            <span class="at">{{ fmtDateTime(e.at) }}</span>
            <span class="actor">{{ e.actor }}</span>
            <span class="path">{{ e.path }}</span>
            <span class="diff">{{ e.before_masked || '-' }} → {{ e.after_masked || '-' }}</span>
          </li>
        </ul>
        <p v-if="audit.length === 0" class="msg">{{ t('settings.callers.audit.empty') }}</p>
      </FormSection>
    </template>
  </div>
</template>

<style scoped>
.callers {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.page-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}
.page-head .grow {
  flex: 1;
  min-width: 0;
}
.page-head h1 {
  margin: 0 0 4px;
  font-size: var(--fs-xl);
  color: var(--ink);
}
.sub {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink-3);
}

.note {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink-3);
  line-height: 1.6;
}

.table-wrap {
  overflow-x: auto;
}
.table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--fs-sm);
}
.table th,
.table td {
  text-align: left;
  padding: 6px 8px;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
}
.table th {
  color: var(--ink-3);
  font-weight: 500;
  white-space: nowrap;
}
.ceiling {
  margin: 4px 0 0;
  padding-left: 14px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.acts {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.tag {
  font-size: var(--fs-xs);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 6px;
  color: var(--ink-2);
  white-space: nowrap;
}
.tag--ok {
  color: var(--accent);
  border-color: var(--accent);
}
.tag--off {
  color: var(--fail);
  border-color: var(--fail);
}
.tag--warn {
  color: var(--need);
  border-color: var(--need);
}
.tag--file,
.tag--db {
  color: var(--ink-3);
}

.reveal {
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.reveal-title {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink);
}
.reveal-token {
  word-break: break-all;
  color: var(--accent);
}
.reveal-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.reveal-hint {
  margin: 0;
  font-size: var(--fs-xs);
  color: var(--ink-3);
}

.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 10px;
  font-size: var(--fs-sm);
}
.field small,
.check + small {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.check {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: var(--fs-sm);
  margin-bottom: 6px;
}
.row-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 10px;
}

.audit {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--fs-xs);
}
.audit li {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  color: var(--ink-2);
  word-break: break-all;
}
.audit .at,
.audit .actor {
  color: var(--ink-3);
}

.btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 6px 12px;
  font: inherit;
  font-size: var(--fs-sm);
  background: transparent;
  color: var(--ink);
}
.btn--sm {
  padding: 3px 8px;
  font-size: var(--fs-xs);
}
.btn:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.btn:disabled {
  opacity: 0.5;
  cursor: default;
}

.msg {
  margin: 0;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 10px 12px;
  font-size: var(--fs-sm);
  color: var(--ink-2);
}
.msg--err {
  color: var(--fail);
  border-color: var(--fail);
  word-break: break-word;
}
.msg--warn {
  color: var(--need);
  border-color: var(--need);
  word-break: break-word;
}
.msg--ok {
  color: var(--accent);
  border-color: var(--accent);
}

/* 手机档：表格保持最小宽度，横向滚动交给 wrapper，页面不横向撑破。 */
@media (max-width: 900px) {
  .table-wrap .table {
    min-width: 860px;
  }
  .page-head {
    flex-direction: column;
  }
}
</style>
