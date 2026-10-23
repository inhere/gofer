<script setup lang="ts">
// 给一个终端会话临时授权（设置之外的入口：Sessions 行与会话抽屉）。
// 只列可授予会话的权限（目录 session_grantable）；下方预览调 GET /v1/auth/expand（防抖），
// 规则只在服务端一份；有效期 1 / 4 / 24 小时，POST /v1/auth/grants 的 subject 为 session:<sid>。
// 纯逻辑在 utils/sessionGrant.ts 与 utils/expandPreview.ts。
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { X } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import { createAuthGrant, expandGrantPattern, getPermissionCatalog } from '../api/permissions'
import { createPreviewScheduler } from '../utils/expandPreview'
import { emptyExpandResult, expandProblemText, grantConfirmRules, grantFormBlocker, riskTone } from '../utils/permissions'
import type { ExpandResult, PermissionCatalog } from '../utils/permissions'
import {
  SESSION_GRANT_DEFAULT_EXPIRY_SEC,
  SESSION_GRANT_ENDS_NOTE,
  SESSION_GRANT_EXPIRY_OPTIONS,
  SESSION_GRANT_THREAT_NOTE,
  buildSessionGrantRequests,
  orderedSelection,
  selectionPatternText,
  sessionGrantable,
  sessionPresets,
  sessionSubject,
} from '../utils/sessionGrant'

const props = defineProps<{ sid: string; label?: string }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'granted'): void }>()

const catalog = ref<PermissionCatalog>({ permissions: [], presets: [] })
const loadError = ref('')
const loading = ref(true)
const grantable = computed(() => sessionGrantable(catalog.value.permissions))
const presets = computed(() => sessionPresets(catalog.value.presets, grantable.value))

const selected = ref<string[]>([])
const form = reactive({ expiry: SESSION_GRANT_DEFAULT_EXPIRY_SEC, note: '', confirmed: false })
const names = computed(() => orderedSelection(selected.value, grantable.value))
const patternText = computed(() => selectionPatternText(names.value))

const expansion = ref<ExpandResult>(emptyExpandResult('empty'))
const scheduler = createPreviewScheduler(expandGrantPattern, (r) => { expansion.value = r })
watch(patternText, (t) => {
  form.confirmed = false
  scheduler.schedule(t, sessionSubject(props.sid))
})

const rules = computed(() => grantConfirmRules(expansion.value))
const blocker = computed(() => {
  if (names.value.length === 0) return '至少选一项权限'
  return grantFormBlocker({ subject: sessionSubject(props.sid), pattern: patternText.value, note: form.note, confirmed: form.confirmed }, expansion.value)
})
const saving = ref(false)
const error = ref('')

function toggle(name: string): void {
  selected.value = selected.value.includes(name) ? selected.value.filter((n) => n !== name) : [...selected.value, name]
}
function applyPreset(perms: string[]): void {
  selected.value = [...perms]
}

async function submit(): Promise<void> {
  if (blocker.value || saving.value) return
  saving.value = true
  error.value = ''
  const reqs = buildSessionGrantRequests(props.sid, names.value, form.expiry, form.note)
  const done: string[] = []
  try {
    for (const r of reqs) {
      await createAuthGrant(r)
      done.push(r.pattern)
    }
    emit('granted')
    emit('close')
  } catch (e) {
    // 已建成的不回滚：留在弹窗里，说明哪些已授权，剩下的可以重试。
    selected.value = selected.value.filter((n) => !done.includes(n))
    if (done.length) emit('granted')
    error.value = (done.length ? `已授权 ${done.join('、')}，其余失败：` : '') + (e instanceof Error ? e.message : String(e))
  } finally {
    saving.value = false
  }
}

function onKey(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && !saving.value) emit('close')
}

onMounted(async () => {
  document.addEventListener('keydown', onKey)
  try {
    catalog.value = await getPermissionCatalog()
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})
onUnmounted(() => {
  scheduler.cancel()
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div class="sg-overlay" @click.self="!saving && emit('close')">
    <form class="sg-panel" role="dialog" aria-modal="true" aria-label="授权这个会话" data-test="sg-dialog" @submit.prevent="submit">
      <header class="sg-head">
        <div>
          <h2 class="sg-title">授权这个会话</h2>
          <p class="sg-sub mono" :title="sid">{{ label || sid }}</p>
        </div>
        <button class="sg-x mono" type="button" :disabled="saving" aria-label="关闭" @click="emit('close')"><Icon :icon="X" size="sm" /></button>
      </header>

      <p class="sg-threat" data-test="sg-threat">
        <strong>谁会得到这些权限</strong>
        {{ SESSION_GRANT_THREAT_NOTE }}
      </p>

      <p v-if="loadError" class="sg-msg sg-msg--error mono" role="alert">{{ loadError }}</p>
      <p v-else-if="loading" class="sg-hint mono">读取权限目录…</p>

      <template v-else>
        <div v-if="presets.length" class="sg-presets" data-test="sg-presets">
          <span class="sg-hint mono">常用</span>
          <button v-for="p in presets" :key="p.key" type="button" class="sg-chip" @click="applyPreset(p.permissions)">{{ p.label }}</button>
        </div>

        <fieldset class="sg-list" data-test="sg-list">
          <legend class="sg-hint mono">可授予会话的权限（{{ grantable.length }}）</legend>
          <label v-for="p in grantable" :key="p.name" class="sg-row" :class="`risk--${riskTone(p.risk)}`">
            <input type="checkbox" :checked="selected.includes(p.name)" :data-test="`sg-perm-${p.name}`" @change="toggle(p.name)" />
            <code>{{ p.name }}</code>
            <span class="sg-risk mono">{{ p.risk_label }}</span>
            <span class="sg-desc">{{ p.description }}</span>
          </label>
        </fieldset>

        <div class="sg-preview" data-test="sg-preview" aria-live="polite">
          <p v-if="expansion.problem && patternText" class="sg-hint mono">{{ expandProblemText(expansion.problem, expansion.message) }}</p>
          <ul v-if="expansion.permissions.length" class="sg-manifest">
            <li v-for="p in expansion.permissions" :key="p.name" :class="`risk--${riskTone(p.risk)}`"><code>{{ p.name }}</code> <span class="sg-risk mono">{{ p.risk_label }}</span></li>
          </ul>
          <p v-else-if="!patternText" class="sg-hint mono">勾选后在这里预览服务端会授予的权限。</p>
        </div>

        <div v-if="rules.needsConfirm" class="sg-confirm" data-test="sg-confirm">
          <p>
            <template v-if="rules.high.length">高风险：<code v-for="n in rules.high" :key="n">{{ n }}</code>。</template>
            授权后，这个会话里的命令立即可以使用。
          </p>
          <label class="sg-check"><input v-model="form.confirmed" type="checkbox" data-test="sg-confirm-box" /> 我确认要授予以上权限</label>
        </div>

        <div class="sg-row2">
          <label class="sg-field">
            <span class="sg-hint mono">有效期</span>
            <select v-model.number="form.expiry" class="mono" data-test="sg-expiry">
              <option v-for="o in SESSION_GRANT_EXPIRY_OPTIONS" :key="o.sec" :value="o.sec">{{ o.label }}</option>
            </select>
          </label>
          <label class="sg-field sg-field--grow">
            <span class="sg-hint mono">原因（选填）</span>
            <input v-model="form.note" placeholder="为什么要授权" data-test="sg-note" />
          </label>
        </div>
        <p class="sg-hint" data-test="sg-ends">{{ SESSION_GRANT_ENDS_NOTE }}</p>
      </template>

      <p v-if="error" class="sg-msg sg-msg--error mono" role="alert" data-test="sg-error">{{ error }}</p>

      <footer class="sg-actions">
        <span v-if="blocker && !loading" class="sg-blocker mono" data-test="sg-blocker">{{ blocker }}</span>
        <button type="button" class="sg-btn" :disabled="saving" @click="emit('close')">取消</button>
        <button type="submit" class="sg-btn sg-btn--primary" :disabled="!!blocker || saving || loading" data-test="sg-submit">{{ saving ? '授权中…' : '授权' }}</button>
      </footer>
    </form>
  </div>
</template>

<style scoped>
.mono { font-family: var(--font-mono); }
.sg-overlay { position: fixed; inset: 0; z-index: 60; background: rgba(0, 0, 0, 0.55); display: flex; align-items: center; justify-content: center; padding: 20px; }
.sg-panel { width: 100%; max-width: 560px; max-height: 100%; overflow: auto; display: flex; flex-direction: column; gap: 12px; padding: 16px; background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-chip); }
.sg-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.sg-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--ink); }
.sg-sub { margin: 2px 0 0; font-size: 12px; color: var(--ink-3); overflow-wrap: anywhere; }
.sg-x { font-size: 18px; line-height: 1; padding: 2px 8px; color: var(--ink-3); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); cursor: pointer; }
/* 威胁说明：左侧色条用「高风险」色，先读它再选权限 */
.sg-threat { margin: 0; padding: 9px 12px; font-size: 13px; line-height: 1.55; border: 1px solid var(--line); border-left: 3px solid var(--run); border-radius: var(--r-chip); background: var(--sunken); }
.sg-threat strong { display: block; color: var(--run); font-weight: 600; margin-bottom: 2px; }
.sg-hint { margin: 0; font-size: 12px; color: var(--ink-3); line-height: 1.5; }
.sg-presets { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.sg-chip { font: inherit; font-size: 12px; padding: 4px 10px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: 999px; cursor: pointer; }
.sg-chip:hover { border-color: var(--accent); color: var(--accent); }
.sg-list { margin: 0; padding: 0; border: 0; min-width: 0; display: grid; gap: 2px; max-height: 240px; overflow: auto; }
.sg-list legend { padding: 0 0 4px; }
.sg-row { --risk: var(--ink-3); display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; padding: 5px 8px; border-left: 3px solid var(--risk); cursor: pointer; }
.sg-row:hover { background: var(--sunken); }
.sg-row input { align-self: center; }
.sg-desc { color: var(--ink-3); font-size: 12px; flex: 1 1 200px; min-width: 0; }
.risk--low { --risk: var(--ink-3); }
.risk--medium { --risk: var(--accent); }
.risk--high { --risk: var(--run); }
.risk--danger { --risk: var(--fail); }
.sg-risk { font-size: 11px; color: var(--risk); border: 1px solid var(--risk); border-radius: 3px; padding: 0 5px; }
code { font-family: var(--font-mono); font-size: 12.5px; }
.sg-preview { min-height: 40px; padding: 8px; border: 1px dashed var(--line); border-radius: var(--r-chip); background: var(--sunken); }
.sg-manifest { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; }
.sg-manifest li { --risk: var(--ink-3); border-left: 3px solid var(--risk); padding: 2px 8px; }
.sg-confirm { padding: 10px 12px; border: 1px solid var(--run); border-radius: var(--r-chip); display: grid; gap: 8px; font-size: 13px; }
.sg-confirm p { margin: 0; }
.sg-confirm code { margin-right: 6px; color: var(--run); }
.sg-check { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--ink-3); }
.sg-row2 { display: flex; flex-wrap: wrap; gap: 12px; }
.sg-field { display: grid; gap: 4px; min-width: 0; }
.sg-field--grow { flex: 1 1 200px; }
input:not([type='checkbox']), select { background: var(--sunken); color: var(--ink); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 6px 8px; font-size: 13px; min-width: 0; }
input:focus-visible, select:focus-visible, .sg-btn:focus-visible, .sg-chip:focus-visible, .sg-x:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.sg-msg { margin: 0; padding: 8px 12px; border: 1px solid var(--line); border-radius: var(--r-chip); font-size: 13px; overflow-wrap: anywhere; }
.sg-msg--error { color: var(--fail); border-color: var(--fail); }
.sg-actions { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 8px; }
.sg-blocker { margin-right: auto; font-size: 12px; color: var(--run); }
.sg-btn { font: inherit; font-size: 12px; padding: 6px 14px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); cursor: pointer; }
.sg-btn:hover:not(:disabled) { border-color: var(--accent); }
.sg-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.sg-btn--primary { color: var(--bg); background: var(--accent); border-color: var(--accent); }
@media (max-width: 760px) {
  .sg-overlay { padding: 0; align-items: flex-end; }
  .sg-panel { max-width: none; max-height: 92vh; border-radius: var(--r-chip) var(--r-chip) 0 0; }
  .sg-list { max-height: 30vh; }
  .sg-field { flex: 1 1 100%; }
}
@media (prefers-reduced-motion: reduce) { * { transition: none !important; animation: none !important; } }
</style>
