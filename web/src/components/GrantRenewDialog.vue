<script setup lang="ts">
// 续期会话授权（设置 → 权限的会话授权行、会话抽屉的「已授权」条共用这一个弹窗）。
// 一件事：把同一条授权的到期时间从「现在」往后挪，不新发一条，所以权限范围、授权人与
// 绑定的会话都不变；有效期仍是 1 / 4 / 24 小时（与「授权」同一组选项）。
// 纯逻辑（选项、请求体、剩余时间、失败文案）在 utils/grantRenew.ts（有单测），这里只管渲染与调用。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError } from '../api/client'
import { renewAuthGrant } from '../api/permissions'
import { fmtDateTime } from '../api/time'
import {
  RENEW_DEFAULT_EXPIRY_SEC,
  RENEW_EXPIRY_OPTIONS,
  buildGrantRenewRequest,
  grantRemainingText,
  renewFailureText,
} from '../utils/grantRenew'

const props = defineProps<{ ids: string[]; label?: string; expiresAt?: number }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'renewed'): void }>()

const expiry = ref(RENEW_DEFAULT_EXPIRY_SEC)
const saving = ref(false)
const error = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))
const remaining = computed(() => grantRemainingText(props.expiresAt, nowSec.value))
const target = computed(() => props.label || `${props.ids.length} 条授权`)

function failText(e: unknown): string {
  if (e instanceof ApiError) return renewFailureText(e.status, e.message)
  return e instanceof Error ? e.message : String(e)
}

async function submit(): Promise<void> {
  if (saving.value || props.ids.length === 0) return
  saving.value = true
  error.value = ''
  let done = 0
  try {
    for (const id of props.ids) {
      const req = buildGrantRenewRequest(id, expiry.value)
      await renewAuthGrant(req.id, req.expires_sec)
      done++
    }
    emit('renewed')
    emit('close')
  } catch (e) {
    // 已延成的不回滚：留在弹窗里说清延到哪一条，剩下的可以重试（与授权弹窗同一口径）。
    if (done > 0) emit('renewed')
    error.value = (done > 0 ? `已续期 ${done} 条，其余失败：` : '') + failText(e)
  } finally {
    saving.value = false
  }
}

function onKey(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && !saving.value) emit('close')
}

onMounted(() => document.addEventListener('keydown', onKey))
onUnmounted(() => document.removeEventListener('keydown', onKey))
</script>

<template>
  <div class="rg-overlay" @click.self="!saving && emit('close')">
    <form class="rg-panel" role="dialog" aria-modal="true" aria-label="续期会话授权" data-test="rg-dialog" @submit.prevent="submit">
      <header class="rg-head">
        <div>
          <h2 class="rg-title">续期会话授权</h2>
          <p class="rg-sub mono" :title="label || target">{{ target }}</p>
        </div>
        <button class="rg-x mono" type="button" :disabled="saving" aria-label="关闭" @click="emit('close')">×</button>
      </header>

      <p class="rg-note" data-test="rg-note">
        <strong>只挪到期时间</strong>
        模式、展开后的权限、授权人与绑定的会话都不变，续期不会放宽范围也不换会话。已撤销 / 已过期 /
        会话已结束的授权不能续期，要在 Sessions 页重新授权。
      </p>

      <p class="rg-hint mono" data-test="rg-remaining">
        <span v-if="remaining">{{ remaining }}</span>
        <span v-if="expiresAt">· 至 {{ fmtDateTime(expiresAt) }}</span>
      </p>

      <label class="rg-field">
        <span class="rg-hint mono">续期后有效期（从「现在」起算）</span>
        <select v-model.number="expiry" class="mono" data-test="rg-expiry" :disabled="saving">
          <option v-for="o in RENEW_EXPIRY_OPTIONS" :key="o.sec" :value="o.sec">{{ o.label }}</option>
        </select>
      </label>

      <p v-if="error" class="rg-msg rg-msg--error mono" role="alert" data-test="rg-error">{{ error }}</p>

      <footer class="rg-actions">
        <button type="button" class="rg-btn" :disabled="saving" @click="emit('close')">取消</button>
        <button type="submit" class="rg-btn rg-btn--primary" :disabled="saving || ids.length === 0" data-test="rg-submit">
          {{ saving ? '续期中…' : '续期' }}
        </button>
      </footer>
    </form>
  </div>
</template>

<style scoped>
.mono { font-family: var(--font-mono); }
.rg-overlay { position: fixed; inset: 0; z-index: 60; background: rgba(0, 0, 0, 0.55); display: flex; align-items: center; justify-content: center; padding: 20px; }
.rg-panel { width: 100%; max-width: 460px; max-height: 100%; overflow: auto; display: flex; flex-direction: column; gap: 12px; padding: 16px; background: var(--panel); border: 1px solid var(--line); border-radius: var(--radius); }
.rg-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.rg-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--paper); }
.rg-sub { margin: 2px 0 0; font-size: 12px; color: var(--queue); overflow-wrap: anywhere; }
.rg-x { font-size: 18px; line-height: 1; padding: 2px 8px; color: var(--queue); background: transparent; border: 1px solid var(--line); border-radius: var(--radius); cursor: pointer; }
/* 说明：左侧色条用「运行中」色，先讲清续期只改一件事 */
.rg-note { margin: 0; padding: 9px 12px; font-size: 13px; line-height: 1.55; border: 1px solid var(--line); border-left: 3px solid var(--run); border-radius: var(--radius); background: var(--term-bg); }
.rg-note strong { display: block; color: var(--run); font-weight: 600; margin-bottom: 2px; }
.rg-hint { margin: 0; font-size: 12px; color: var(--queue); line-height: 1.5; display: flex; flex-wrap: wrap; gap: 6px; }
.rg-field { display: grid; gap: 4px; min-width: 0; }
select { background: var(--term-bg); color: var(--paper); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 8px; font-size: 13px; min-width: 0; }
select:focus-visible, .rg-btn:focus-visible, .rg-x:focus-visible { outline: 2px solid var(--phosphor); outline-offset: 2px; }
.rg-msg { margin: 0; padding: 8px 12px; border: 1px solid var(--line); border-radius: var(--radius); font-size: 13px; overflow-wrap: anywhere; }
.rg-msg--error { color: var(--fail); border-color: var(--fail); }
.rg-actions { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 8px; }
.rg-btn { font: inherit; font-size: 12px; padding: 6px 14px; color: var(--paper); background: transparent; border: 1px solid var(--line); border-radius: var(--radius); cursor: pointer; }
.rg-btn:hover:not(:disabled) { border-color: var(--phosphor); }
.rg-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.rg-btn--primary { color: var(--ink); background: var(--phosphor); border-color: var(--phosphor); }
@media (max-width: 640px) {
  .rg-overlay { padding: 0; align-items: flex-end; }
  .rg-panel { max-width: none; max-height: 92vh; border-radius: var(--radius) var(--radius) 0 0; }
  .rg-field { flex: 1 1 100%; }
}
@media (prefers-reduced-motion: reduce) { * { transition: none !important; animation: none !important; } }
</style>
