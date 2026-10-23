<script setup lang="ts">
// 拒绝一条 worker 自助注册申请：理由（可选，≤200 字）与「屏蔽该来源 10 分钟」勾选。
// 屏蔽用于应对重复骚扰申请（block_source_min=10；期间该来源再申请会 429）。
// 纯逻辑（错误文案、来源文案）在 utils/enrollments.ts。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { X } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import { rejectEnrollment } from '../api/client'
import type { WorkerEnrollment } from '../api/types'
import { hostLine, rejectErrorText } from '../utils/enrollments'

const props = defineProps<{ item: WorkerEnrollment }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'rejected'): void }>()

// 勾选时屏蔽的分钟数：与设计「拒绝时可选屏蔽该来源（默认 10 分钟）」一致。
const BLOCK_SOURCE_MIN = 10

const reason = ref('')
const block = ref(false)
const busy = ref(false)
const error = ref('')

const host = computed(() => hostLine(props.item.host))

async function submit(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await rejectEnrollment(props.item.id, {
      reason: reason.value.trim(),
      block_source_min: block.value ? BLOCK_SOURCE_MIN : 0,
    })
    emit('rejected')
  } catch (e) {
    error.value = rejectErrorText(e)
  } finally {
    busy.value = false
  }
}

function onKey(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && !busy.value) emit('close')
}

onMounted(() => document.addEventListener('keydown', onKey))
onUnmounted(() => document.removeEventListener('keydown', onKey))
</script>

<template>
  <div class="er-overlay" @click.self="!busy && emit('close')">
    <form class="er-panel" role="dialog" aria-modal="true" aria-label="拒绝注册申请" data-test="enroll-reject-dialog" @submit.prevent="submit">
      <header class="er-head">
        <div class="er-head-main">
          <h2 class="er-title">拒绝注册申请</h2>
          <p class="er-sub mono">{{ item.source || '来源未知' }}<template v-if="host"> · {{ host }}</template></p>
        </div>
        <button class="er-x mono" type="button" :disabled="busy" aria-label="关闭" @click="emit('close')"><Icon :icon="X" size="sm" /></button>
      </header>

      <div class="er-field">
        <span class="er-hint mono">理由（可选）</span>
        <input v-model="reason" class="er-input mono" maxlength="200" placeholder="为什么拒绝（可选）" data-test="enroll-reject-reason" />
      </div>

      <label class="er-check">
        <input v-model="block" type="checkbox" data-test="enroll-reject-block" />
        屏蔽该来源 {{ BLOCK_SOURCE_MIN }} 分钟
        <span class="er-hint mono">期间该来源再发起申请会直接被拒（429）</span>
      </label>

      <p v-if="error" class="er-msg mono" role="alert" data-test="enroll-reject-error">{{ error }}</p>

      <footer class="er-actions">
        <button type="button" class="er-btn" :disabled="busy" @click="emit('close')">取消</button>
        <button type="submit" class="er-btn er-btn--bad" :disabled="busy" data-test="enroll-reject-submit">{{ busy ? '拒绝中…' : '拒绝' }}</button>
      </footer>
    </form>
  </div>
</template>

<style scoped>
.er-overlay { position: fixed; inset: 0; z-index: 60; background: rgba(0, 0, 0, 0.55); display: flex; align-items: center; justify-content: center; padding: 20px; }
.er-panel { width: 100%; max-width: 460px; max-height: 100%; overflow: auto; display: flex; flex-direction: column; gap: 12px; padding: 16px; background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-chip); }
.er-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.er-head-main { min-width: 0; }
.er-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--ink); }
.er-sub { margin: 2px 0 0; font-size: 12px; color: var(--ink-3); overflow-wrap: anywhere; }
.er-x { font-size: 18px; line-height: 1; padding: 2px 8px; color: var(--ink-3); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); }
.er-field { display: grid; gap: 5px; min-width: 0; }
.er-hint { font-size: 12px; color: var(--ink-3); letter-spacing: 0.04em; }
.er-input { background: var(--sunken); color: var(--ink); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 7px 9px; font-size: 13px; min-width: 0; }
.er-check { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; font-size: 13px; color: var(--ink); }
.er-msg { margin: 0; padding: 8px 12px; border: 1px solid var(--fail); border-radius: var(--r-chip); color: var(--fail); font-size: 13px; overflow-wrap: anywhere; }
.er-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
.er-btn { font: inherit; font-size: 12px; padding: 6px 14px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); }
.er-btn:hover:not(:disabled) { border-color: var(--accent); }
.er-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.er-btn--bad { color: var(--fail); border-color: var(--fail); }
@media (max-width: 760px) {
  .er-overlay { padding: 0; align-items: flex-end; }
  .er-panel { max-width: none; max-height: 92vh; border-radius: var(--r-chip) var(--r-chip) 0 0; }
}
</style>
