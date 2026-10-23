<script setup lang="ts">
// 顶栏「随手记」：任何页面都能把一句话丢进收集箱（POST /v1/work-items/capture）。
// 桌面是顶栏下方的小浮层，手机是贴底的面板（拇指可及，输入框 16px 防 iOS 缩放）。
// 提交成功后弹一条带「去收集箱」链接的提示，几秒后自行消失；Esc / 点遮罩关闭。
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { SquarePen, X } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import { captureWorkItem, listProjects } from '../api/client'
import { t } from '../i18n'
import {
  buildCaptureRequest,
  CAPTURE_BOX_LINK,
  CAPTURE_KINDS,
  CAPTURE_TEXT_MAX,
  captureKindLabel,
  DEFAULT_CAPTURE_KIND,
} from '../utils/capture'

const open = ref(false)
const text = ref('')
const kind = ref<string>(DEFAULT_CAPTURE_KIND)
const project = ref('')
const projects = ref<string[]>([])
const busy = ref(false)
const error = ref('')
const done = ref<{ kind: string } | null>(null)
const area = ref<HTMLTextAreaElement | null>(null)
let doneTimer: ReturnType<typeof setTimeout> | undefined

watch(open, async (v) => {
  if (!v) return
  error.value = ''
  if (!projects.value.length) projects.value = (await listProjects().catch(() => ({ projects: [] as string[] }))).projects ?? []
  await nextTick()
  area.value?.focus()
})

function close(): void {
  open.value = false
}

async function submit(): Promise<void> {
  if (busy.value) return
  const built = buildCaptureRequest({ text: text.value, kind: kind.value, project: project.value })
  if (!built.req) {
    error.value = built.error
    return
  }
  busy.value = true
  error.value = ''
  try {
    await captureWorkItem(built.req)
    done.value = { kind: built.req.kind }
    text.value = ''
    open.value = false
    clearTimeout(doneTimer)
    doneTimer = setTimeout(() => (done.value = null), 6000)
  } catch (e) {
    error.value = e instanceof Error ? (('detail' in e && (e as { detail?: string }).detail) || e.message) : String(e)
  } finally {
    busy.value = false
  }
}

function onKey(ev: KeyboardEvent): void {
  if (ev.key === 'Escape') close()
  else if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey) && !ev.isComposing) {
    ev.preventDefault()
    void submit()
  }
}

onBeforeUnmount(() => clearTimeout(doneTimer))
</script>

<template>
  <div class="qc-wrap">
    <button class="qc-btn mono" type="button" :aria-expanded="open" :title="t('common.quickCapture.buttonTitle')" data-test="quick-capture-btn" @click="open = !open">
      <Icon :icon="SquarePen" size="sm" /><span class="qc-label">{{ t('common.quickCapture.button') }}</span>
    </button>

    <template v-if="open">
      <div class="qc-scrim" aria-hidden="true" @click="close"></div>
      <form class="qc-panel" role="dialog" :aria-label="t('common.quickCapture.button')" data-test="quick-capture-form" @submit.prevent="submit" @keydown="onKey">
        <textarea
          ref="area"
          v-model="text"
          class="qc-text"
          rows="4"
          :maxlength="CAPTURE_TEXT_MAX"
          :placeholder="t('common.quickCapture.placeholder')"
          data-test="quick-capture-text"
        ></textarea>
        <div class="qc-kinds mono" role="radiogroup" :aria-label="t('common.quickCapture.kindLabel')">
          <button
            v-for="k in CAPTURE_KINDS"
            :key="k.key"
            type="button"
            role="radio"
            class="qc-kind"
            :class="{ on: kind === k.key }"
            :aria-checked="kind === k.key"
            :data-kind="k.key"
            @click="kind = k.key"
          >{{ k.label }}</button>
        </div>
        <input v-model="project" class="qc-proj mono" type="text" list="qc-projects" :placeholder="t('common.quickCapture.project')" data-test="quick-capture-project" />
        <datalist id="qc-projects"><option v-for="p in projects" :key="p" :value="p"></option></datalist>
        <p v-if="error" class="qc-error mono" role="alert" data-test="quick-capture-error">{{ error }}</p>
        <div class="qc-actions">
          <button class="qc-submit mono" type="submit" :disabled="busy" data-test="quick-capture-submit">{{ busy ? t('common.quickCapture.saving') : t('common.quickCapture.submit') }}</button>
          <button class="qc-cancel mono" type="button" :disabled="busy" @click="close">{{ t('common.quickCapture.cancel') }}</button>
        </div>
      </form>
    </template>

    <div v-if="done" class="qc-toast mono" role="status" aria-live="polite" data-test="quick-capture-toast">
      <span>{{ t('common.quickCapture.done', { kind: captureKindLabel(done.kind) }) }}</span>
      <RouterLink :to="CAPTURE_BOX_LINK" class="qc-toast-link" data-test="quick-capture-link" @click="done = null">{{ t('common.quickCapture.goto') }}</RouterLink>
      <button class="qc-toast-x" type="button" :aria-label="t('common.quickCapture.dismiss')" @click="done = null"><Icon :icon="X" size="sm" /></button>
    </div>
  </div>
</template>

<style scoped>
.qc-wrap { position: relative; display: inline-flex; align-items: center; }
.qc-btn {
  display: inline-flex; align-items: center; gap: 6px; padding: 4px 9px; font-size: 12px; line-height: 1.2;
  background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); color: var(--ink-3); white-space: nowrap; cursor: pointer;
}
.qc-btn:hover, .qc-btn[aria-expanded='true'] { border-color: var(--accent); color: var(--ink); }
.qc-btn:focus-visible, .qc-kind:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.qc-scrim { position: fixed; inset: 0; z-index: 60; background: rgba(0, 0, 0, 0.25); }
.qc-panel {
  position: fixed; z-index: 61; top: 48px; right: 14px; width: min(380px, calc(100vw - 28px));
  display: flex; flex-direction: column; gap: 10px; padding: 12px;
  background: var(--surface); border: 1px solid var(--line); border-top: 2px solid var(--accent);
  border-radius: var(--r-chip); box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
}
.qc-text, .qc-proj {
  width: 100%; box-sizing: border-box; padding: 7px 9px; color: var(--ink); background: var(--sunken);
  border: 1px solid var(--line); border-radius: var(--r-chip); font: inherit; font-size: 13px; line-height: 1.5; resize: vertical;
}
.qc-text:focus, .qc-proj:focus { outline: none; border-color: var(--accent); }
.qc-kinds { display: flex; border: 1px solid var(--line); border-radius: var(--r-chip); overflow: hidden; }
.qc-kind { flex: 1; padding: 6px 4px; font-size: 12px; color: var(--ink-3); background: transparent; border: 0; border-right: 1px solid var(--line); cursor: pointer; }
.qc-kind:last-child { border-right: 0; }
.qc-kind.on { color: var(--ink); background: color-mix(in srgb, var(--accent) 18%, transparent); }
.qc-error { margin: 0; font-size: 12px; color: var(--fail); overflow-wrap: anywhere; }
.qc-actions { display: flex; gap: 8px; }
.qc-submit, .qc-cancel { padding: 5px 14px; font-size: 12px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); cursor: pointer; }
.qc-submit { border-color: var(--accent); color: var(--accent); font-weight: 600; }
.qc-submit:disabled, .qc-cancel:disabled { opacity: 0.5; cursor: not-allowed; }
.qc-toast {
  position: fixed; z-index: 70; right: 18px; bottom: 18px; display: flex; align-items: center; gap: 12px;
  padding: 10px 14px; font-size: 12px; color: var(--ink); background: var(--surface);
  border: 1px solid var(--done); border-radius: var(--r-chip); box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
}
.qc-toast-link { color: var(--accent); text-decoration: underline; }
.qc-toast-x { background: none; border: 0; color: var(--ink-3); cursor: pointer; font-size: 14px; }
@media (max-width: 760px) {
  .qc-label { display: none; }
  .qc-btn { padding: 4px 10px; font-size: 14px; }
  .qc-panel { top: auto; right: 0; bottom: 0; left: 0; width: auto; border-radius: var(--r-chip) var(--r-chip) 0 0; padding-bottom: calc(12px + env(safe-area-inset-bottom, 0px)); }
  .qc-text, .qc-proj { font-size: 16px; }
  .qc-kind { padding: 10px 4px; font-size: 14px; }
  .qc-submit, .qc-cancel { padding: 10px 18px; font-size: 14px; }
  .qc-toast { left: 14px; right: 14px; bottom: calc(70px + env(safe-area-inset-bottom, 0px)); }
}
</style>
