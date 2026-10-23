<script setup lang="ts">
// 拒绝验收弹层（REV-01）：note 必填 + 「拒绝后自动续投」复选框。
// 验收台列表与 job 详情验收面板共用（同一套按钮语义，避免两处各写一遍）。
// 校验在本地做（note 为空禁用提交）；请求失败由父组件把 error 传回来，弹层保持打开。
import { onMounted, onUnmounted, ref } from 'vue'
import { t } from '../i18n'

const props = defineProps<{ submitting: boolean; error: string }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'confirm', note: string, resume: boolean): void
}>()

const note = ref('')
const resume = ref(false)

function onKeydown(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && !props.submitting) {
    emit('close')
  }
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
})
onUnmounted(() => {
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="rd-overlay" @click.self="!submitting && emit('close')">
    <div class="rd-panel" role="dialog" aria-modal="true" :aria-label="t('review.reject.title')">
      <div class="rd-head">
        <span class="rd-title mono">{{ t('review.reject.title') }}</span>
        <button class="rd-x mono" type="button" :disabled="submitting" @click="emit('close')">
          {{ t('review.reject.close') }}
        </button>
      </div>

      <label class="rd-field mono">
        <span class="rd-label">{{ t('review.reject.noteLabel') }}</span>
        <textarea
          v-model="note"
          class="rd-note mono"
          rows="4"
          :placeholder="t('review.reject.notePlaceholder')"
        ></textarea>
      </label>

      <label class="rd-check mono">
        <input v-model="resume" type="checkbox" />
        <span>{{ t('review.reject.autoResume') }}</span>
      </label>

      <p v-if="error" class="rd-err mono">{{ error }}</p>

      <div class="rd-actions">
        <button class="rd-btn mono" type="button" :disabled="submitting" @click="emit('close')">
          {{ t('review.reject.cancel') }}
        </button>
        <button
          class="rd-btn rd-btn--danger mono"
          type="button"
          :disabled="submitting || !note.trim()"
          @click="emit('confirm', note.trim(), resume)"
        >
          {{ submitting ? t('review.reject.submitting') : t('review.reject.confirm') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.rd-overlay {
  position: fixed;
  inset: 0;
  z-index: 40;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}
.rd-panel {
  width: 100%;
  max-width: 460px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.rd-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.rd-title {
  color: var(--fail);
  font-size: 13px;
  letter-spacing: 0.06em;
}
.rd-x {
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 2px 8px;
  font-size: 12px;
}
.rd-x:hover:not(:disabled) {
  color: var(--ink);
  border-color: var(--ink);
}
.rd-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.rd-label {
  color: var(--ink-3);
  font-size: 11px;
}
.rd-note {
  width: 100%;
  box-sizing: border-box;
  background: var(--sunken);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 6px 8px;
  font-size: 12px;
  resize: vertical;
}
.rd-check {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--ink-3);
  font-size: 12px;
}
.rd-err {
  margin: 0;
  color: var(--fail);
  font-size: 12px;
  word-break: break-word;
}
.rd-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.rd-btn {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 12px;
  font-size: 12px;
}
.rd-btn:hover:not(:disabled) {
  color: var(--accent);
  border-color: var(--accent);
}
.rd-btn:disabled {
  color: var(--ink-3);
  cursor: default;
  opacity: 0.65;
}
.rd-btn--danger {
  border-color: var(--fail);
  color: var(--fail);
}
.rd-btn--danger:hover:not(:disabled) {
  background: var(--fail);
  color: var(--bg);
}
</style>
