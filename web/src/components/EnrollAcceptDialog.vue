<script setup lang="ts">
// 接受一条 worker 自助注册申请：配对码（4+4 两段，自动大写、吃掉空格与横线）、worker id
// （默认取申请里提议的值，可改）、项目多选与标签。
//
//  - 配对码要在 **worker 终端**上看，web 不显示码——这正是防冒名的一步（攻击者的申请显示的是
//    它自己的码，与真 worker 终端上的码对不上）。
//  - 保留名 server / local 与格式错误当场提示；id 是否已存在的最终判定在服务端（409，申请回到
//    pending，人可以改 id 再试）。
//  - 纯逻辑（分段 / 规范化 / 校验 / 请求构造 / 错误文案）在 utils/enrollments.ts，本组件只管渲染。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { acceptEnrollment } from '../api/client'
import type { WorkerEnrollment } from '../api/types'
import {
  PAIRING_SEGMENT,
  acceptErrorText,
  buildAcceptRequest,
  cleanSegment,
  decideCountdown,
  hostLine,
  pairingFromSegments,
  pairingHint,
  workerIdError,
  type AcceptForm,
} from '../utils/enrollments'

const props = defineProps<{
  item: WorkerEnrollment
  projects: string[]
  nowSec: number
  // 已知的 worker id（在线 worker + 已接受的申请）：命中就当场提示要先删旧 worker
  existingWorkerIds?: string[]
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'accepted', workerID: string): void }>()

const codeFirst = ref('')
const codeSecond = ref('')
const workerID = ref((props.item.requested_id ?? '').trim())
const labels = ref((props.item.labels ?? []).join(','))
const picked = ref<string[]>(
  (props.item.projects ?? []).filter((p) => props.projects.includes(p)),
)
const busy = ref(false)
const error = ref('')

const firstEl = ref<HTMLInputElement | null>(null)
const secondEl = ref<HTMLInputElement | null>(null)

const code = computed(() => pairingFromSegments(codeFirst.value, codeSecond.value))
// 输入过半才提示，免得刚打开弹窗就报错
const codeHint = computed(() => (code.value.length === 0 ? '' : pairingHint(code.value)))
const idHint = computed(() =>
  workerID.value.trim() === (props.item.requested_id ?? '').trim()
    ? ''
    : workerIdError(workerID.value, props.existingWorkerIds ?? []),
)
const left = computed(() => decideCountdown(props.item.decide_until, props.nowSec))
const host = computed(() => hostLine(props.item.host))
const agents = computed(() => props.item.host?.agents ?? [])
const roots = computed(() => props.item.host?.roots ?? [])

function onSegmentInput(which: 1 | 2, ev: Event): void {
  const el = ev.target as HTMLInputElement
  const clean = cleanSegment(el.value)
  if (which === 1) {
    codeFirst.value = clean
  } else {
    codeSecond.value = clean
  }
  // 输入框里回填规范化后的值（大写、无分隔符）
  el.value = clean
  if (clean.length === PAIRING_SEGMENT && which === 1) secondEl.value?.focus()
}

function onCodeKey(ev: KeyboardEvent): void {
  if (ev.key === 'Backspace' && (ev.target as HTMLInputElement).value === '') firstEl.value?.focus()
}

function toggleProject(key: string): void {
  picked.value = picked.value.includes(key)
    ? picked.value.filter((p) => p !== key)
    : [...picked.value, key]
}

async function submit(): Promise<void> {
  if (busy.value) return
  const form: AcceptForm = {
    codeFirst: codeFirst.value,
    codeSecond: codeSecond.value,
    workerId: workerID.value,
    labels: labels.value,
    projects: picked.value,
  }
  const built = buildAcceptRequest(form, props.existingWorkerIds ?? [])
  if (!built.body) {
    error.value = built.error
    return
  }
  busy.value = true
  error.value = ''
  try {
    const out = await acceptEnrollment(props.item.id, built.body)
    emit('accepted', out.worker_id)
  } catch (e) {
    error.value = acceptErrorText(e)
  } finally {
    busy.value = false
  }
}

function onKey(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && !busy.value) emit('close')
}

onMounted(() => {
  document.addEventListener('keydown', onKey)
  firstEl.value?.focus()
})
onUnmounted(() => {
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div class="ea-overlay" @click.self="!busy && emit('close')">
    <form class="ea-panel" role="dialog" aria-modal="true" aria-label="接受 worker 注册申请" data-test="enroll-accept-dialog" @submit.prevent="submit">
      <header class="ea-head">
        <div class="ea-head-main">
          <h2 class="ea-title">接受注册申请</h2>
          <p class="ea-sub mono" data-test="enroll-accept-facts">
            {{ item.source || '来源未知' }}<template v-if="host"> · {{ host }}</template><template v-if="left"> · {{ left }}</template>
          </p>
        </div>
        <button class="ea-x mono" type="button" :disabled="busy" aria-label="关闭" @click="emit('close')">×</button>
      </header>

      <p class="ea-note">
        <strong>配对码在 worker 终端上</strong>
        申请方运行 <code class="mono">gofer worker join</code> 时命令行会显示 8 位配对码；web 不显示码，
        照着输入以确认这是那台机器发起的申请（不是冒名）。
      </p>

      <div class="ea-field">
        <span class="ea-hint mono">配对码（4+4）</span>
        <div class="ea-code">
          <input
            ref="firstEl"
            class="ea-code-in mono"
            type="text"
            autocomplete="off"
            autocapitalize="characters"
            spellcheck="false"
            maxlength="4"
            placeholder="XXXX"
            aria-label="配对码前 4 位"
            data-test="enroll-code-first"
            :value="codeFirst"
            @input="onSegmentInput(1, $event)"
            @keydown="onCodeKey"
          />
          <span class="ea-dash mono" aria-hidden="true">–</span>
          <input
            ref="secondEl"
            class="ea-code-in mono"
            type="text"
            autocomplete="off"
            autocapitalize="characters"
            spellcheck="false"
            maxlength="4"
            placeholder="XXXX"
            aria-label="配对码后 4 位"
            data-test="enroll-code-second"
            :value="codeSecond"
            @input="onSegmentInput(2, $event)"
          />
        </div>
        <p v-if="codeHint" class="ea-hint ea-hint--warn mono" data-test="enroll-code-hint">{{ codeHint }}</p>
      </div>

      <div class="ea-field">
        <span class="ea-hint mono">WORKER ID</span>
        <input v-model="workerID" class="ea-input mono" autocomplete="off" placeholder="worker 标识" data-test="enroll-worker-id" />
        <p v-if="idHint" class="ea-hint ea-hint--warn mono" data-test="enroll-id-hint">{{ idHint }}</p>
      </div>

      <div class="ea-field">
        <span class="ea-hint mono">项目（可选多个）</span>
        <div v-if="projects.length" class="ea-projects" data-test="enroll-projects">
          <label v-for="p in projects" :key="p" class="ea-row">
            <input type="checkbox" :checked="picked.includes(p)" :data-test="`enroll-project-${p}`" @change="toggleProject(p)" />
            <code class="mono">{{ p }}</code>
          </label>
        </div>
        <p v-else class="ea-hint mono">server 上没有可选项目。</p>
      </div>

      <div class="ea-field">
        <span class="ea-hint mono">标签（逗号分隔，可选）</span>
        <input v-model="labels" class="ea-input mono" autocomplete="off" placeholder="linux,gpu" data-test="enroll-labels" />
      </div>

      <div v-if="agents.length || roots.length || item.labels?.length" class="ea-facts mono" data-test="enroll-accept-detail">
        <p v-if="item.labels?.length"><b>申请方标签</b>{{ item.labels.join(', ') }}</p>
        <p v-if="agents.length"><b>agents</b>{{ agents.join(', ') }}</p>
        <p v-if="roots.length"><b>roots</b>{{ roots.join(' · ') }}</p>
      </div>

      <p v-if="error" class="ea-msg mono" role="alert" data-test="enroll-accept-error">{{ error }}</p>

      <footer class="ea-actions">
        <button type="button" class="ea-btn" :disabled="busy" @click="emit('close')">取消</button>
        <button type="submit" class="ea-btn ea-btn--primary" :disabled="busy" data-test="enroll-accept-submit">{{ busy ? '接受中…' : '接受' }}</button>
      </footer>
    </form>
  </div>
</template>

<style scoped>
.ea-overlay { position: fixed; inset: 0; z-index: 60; background: rgba(0, 0, 0, 0.55); display: flex; align-items: center; justify-content: center; padding: 20px; }
.ea-panel { width: 100%; max-width: 520px; max-height: 100%; overflow: auto; display: flex; flex-direction: column; gap: 12px; padding: 16px; background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-chip); }
.ea-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.ea-head-main { min-width: 0; }
.ea-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--ink); }
.ea-sub { margin: 2px 0 0; font-size: 12px; color: var(--ink-3); overflow-wrap: anywhere; }
.ea-x { font-size: 18px; line-height: 1; padding: 2px 8px; color: var(--ink-3); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); }
.ea-note { margin: 0; padding: 9px 12px; font-size: 13px; line-height: 1.55; border: 1px solid var(--line); border-left: 3px solid var(--accent); border-radius: var(--r-chip); background: var(--log-bg); }
.ea-note strong { display: block; color: var(--accent); font-weight: 600; margin-bottom: 2px; }
.ea-field { display: grid; gap: 5px; min-width: 0; }
.ea-hint { margin: 0; font-size: 12px; color: var(--ink-3); letter-spacing: 0.04em; }
.ea-hint--warn { color: var(--run); }
.ea-code { display: flex; align-items: center; gap: 8px; }
.ea-code-in { width: 6.5em; letter-spacing: 0.22em; text-align: center; text-transform: uppercase; font-size: 18px; }
.ea-dash { color: var(--ink-3); }
.ea-input, .ea-code-in { background: var(--log-bg); color: var(--ink); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 7px 9px; font-size: 13px; min-width: 0; }
.ea-projects { display: grid; gap: 2px; max-height: 180px; overflow: auto; border: 1px solid var(--line); border-radius: var(--r-chip); padding: 4px; }
.ea-row { display: flex; align-items: center; gap: 8px; padding: 4px 6px; cursor: pointer; }
.ea-row:hover { background: var(--log-bg); }
.ea-row code { font-size: 12.5px; }
.ea-facts { display: grid; gap: 2px; font-size: 12px; color: var(--ink-3); }
.ea-facts p { margin: 0; overflow-wrap: anywhere; }
.ea-facts b { display: inline-block; min-width: 6em; color: var(--ink); font-weight: 500; }
.ea-msg { margin: 0; padding: 8px 12px; border: 1px solid var(--fail); border-radius: var(--r-chip); color: var(--fail); font-size: 13px; overflow-wrap: anywhere; }
.ea-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
.ea-btn { font: inherit; font-size: 12px; padding: 6px 14px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); }
.ea-btn:hover:not(:disabled) { border-color: var(--accent); }
.ea-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.ea-btn--primary { color: var(--bg); background: var(--accent); border-color: var(--accent); }
@media (max-width: 760px) {
  .ea-overlay { padding: 0; align-items: flex-end; }
  .ea-panel { max-width: none; max-height: 92vh; border-radius: var(--r-chip) var(--r-chip) 0 0; }
  .ea-projects { max-height: 30vh; }
}
</style>
