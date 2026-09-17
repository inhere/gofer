<script setup lang="ts">
// 运行中交互卡：question 文本问答 / choice 选项 / confirmation 确认。
//  - pending：可输入/点选，emit answer(value)，未留人时可 emit punt；
//  - answered：整卡只读，展示「已提交：{answer}」和归属；
//  - expired（decision 投影，T4）：只读展示「已过期」，无按钮、无回显；
//  - cancelled 等非 pending 态：不渲染任何可点按钮（此前会落入 confirmation 分支）；
//  - submitting：禁用全部输入/按钮，防重复提交。
// isDecision：decision 投影卡（T4）恒无 punt（无 job 可 punt）。
// 视觉：--panel 底 + --line 边 + --phosphor 强调标题；id/type mono 小字。
// 一次性滑入动画，prefers-reduced-motion 下关闭。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import type { Interaction } from '../api/types'

const props = defineProps<{
  interaction: Interaction
  submitting?: boolean
  isDecision?: boolean
}>()
const emit = defineEmits<{
  (e: 'answer', value: string): void
  (e: 'punt'): void
}>()

const pending = computed(() => props.interaction.status === 'pending')
const answered = computed(() => props.interaction.status === 'answered')
const expired = computed(() => props.interaction.status === 'expired')
const disabled = computed(() => !!props.submitting || !pending.value)
const canPunt = computed(
  () =>
    pending.value &&
    !props.isDecision &&
    props.interaction.needs_human !== 1,
)
const answeredBy = computed(() => props.interaction.answered_by?.trim() || 'human')

// question：本地文本输入
const text = ref('')

// confirmation 默认 yes/no，options 非空则用 options 的 value 覆盖
const confirmYes = computed(() => props.interaction.options?.[0]?.value ?? 'yes')
const confirmNo = computed(() => props.interaction.options?.[1]?.value ?? 'no')
const confirmYesLabel = computed(
  () => props.interaction.options?.[0]?.label ?? '确认',
)
const confirmNoLabel = computed(
  () => props.interaction.options?.[1]?.label ?? '取消',
)

function submitText(): void {
  if (disabled.value) {
    return
  }
  const v = text.value.trim()
  if (v.length === 0) {
    return
  }
  emit('answer', v)
}

function submit(value: string): void {
  if (disabled.value) {
    return
  }
  emit('answer', value)
}

// ---- 审批卡（type=permission，GATE-01 §1）----

const isPermission = computed(() => props.interaction.type === 'permission')
const toolCall = computed(() => props.interaction.tool_call)
const KNOWN_PERMISSION_KINDS = new Set([
  'allow_once',
  'allow_always',
  'reject_once',
  'reject_always',
])
// 选项按 ACP kind 分组：放行（allow_once/allow_always）在前，拒绝在后；未知 kind
// 归入「其他」，按钮文案取 label（回退 optionId）。
const allowOptions = computed(() => permissionOptions(['allow_once', 'allow_always']))
const rejectOptions = computed(() => permissionOptions(['reject_once', 'reject_always']))
const otherOptions = computed(() =>
  (props.interaction.options ?? []).filter((o) => !KNOWN_PERMISSION_KINDS.has(o.kind ?? '')),
)

function permissionOptions(kinds: string[]) {
  const wanted = new Set(kinds)
  return (props.interaction.options ?? []).filter((o) => wanted.has(o.kind ?? ''))
}

// kind 徽标：allow_* 主色、reject_* 幽灵（样式类由 kindClass 给）。
function kindClass(kind: string | undefined): string {
  if (kind?.startsWith('allow')) {
    return 'icard-btn--primary'
  }
  if (kind?.startsWith('reject')) {
    return 'icard-btn--ghost'
  }
  return ''
}

// 倒计时：后端 expires_at 是审批门放弃等待的时刻；到期后清空（答案是权威，超时由
// 后端兜底），不做本地推断。
const now = ref(Math.floor(Date.now() / 1000))
const leftSec = computed(() => {
  const exp = props.interaction.expires_at ?? 0
  if (exp <= 0) {
    return -1
  }
  return exp - now.value
})
let ticker: number | undefined
onMounted(() => {
  if ((props.interaction.expires_at ?? 0) > 0) {
    ticker = window.setInterval(() => {
      now.value = Math.floor(Date.now() / 1000)
      if (leftSec.value <= 0 && ticker !== undefined) {
        window.clearInterval(ticker)
        ticker = undefined
      }
    }, 1000)
  }
})
onUnmounted(() => {
  if (ticker !== undefined) {
    window.clearInterval(ticker)
  }
})

// fmtLeft 渲染剩余时间为 mm:ss（未设置截止 / 已过期时返回 ''）。
function fmtLeft(sec: number): string {
  if (sec < 0) {
    return ''
  }
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
}

function punt(): void {
  if (disabled.value || !canPunt.value) {
    return
  }
  emit('punt')
}

// label 优先，回退 value
function optLabel(opt: { value: string; label?: string }): string {
  return opt.label ?? opt.value
}

function fmtTime(v: number | undefined): string {
  if (v == null || v <= 0) {
    return ''
  }
  return new Date(v * 1000).toLocaleString()
}
</script>

<template>
  <div class="icard" :class="{ 'icard--answered': answered || expired }">
    <div class="icard-head mono">
      <span class="icard-type">{{ interaction.type }}</span>
      <span class="icard-id">{{ interaction.id }}</span>
    </div>

    <p class="icard-prompt">{{ interaction.prompt }}</p>

    <!-- answered：只读回显 -->
    <template v-if="answered">
      <p class="icard-done mono">
        已提交：<span class="icard-answer">{{ interaction.answer }}</span>
      </p>
      <p class="icard-meta mono">
        <span>answered_by {{ answeredBy }}</span>
        <span v-if="fmtTime(interaction.answered_at)">{{ fmtTime(interaction.answered_at) }}</span>
        <span v-if="interaction.needs_human === 1" class="icard-mark">needs_human</span>
      </p>
    </template>

    <!-- expired（decision 投影）：只读，无按钮、无假回显 -->
    <template v-else-if="expired">
      <p class="icard-done mono">已过期 · 未在限时内作答</p>
      <p class="icard-meta mono">
        <span v-if="fmtTime(interaction.created_at)">asked {{ fmtTime(interaction.created_at) }}</span>
      </p>
    </template>

    <!-- 非 pending（cancelled 等）：只读，不渲染任何可点按钮 -->

    <!-- permission：审批卡（GATE-01 §1）——工具调用摘要 + 按 ACP kind 分组的选项按钮 -->
    <div v-if="pending && isPermission" class="icard-body icard-approve">
      <div v-if="toolCall" class="icard-tool">
        <span v-if="toolCall.kind" class="icard-kind mono">{{ toolCall.kind }}</span>
        <span class="icard-tool-title mono">{{ toolCall.title || toolCall.id }}</span>
        <span v-if="toolCall.locations?.length" class="icard-tool-path mono">
          {{ toolCall.locations.join(', ') }}
        </span>
      </div>
      <pre v-if="toolCall?.raw_input_summary" class="icard-raw mono">{{
        toolCall.raw_input_summary
      }}</pre>
      <p v-if="interaction.policy_hint" class="icard-meta mono">
        <span>{{ interaction.policy_hint }}</span>
        <span v-if="leftSec >= 0">倒计时 {{ fmtLeft(leftSec) }}</span>
      </p>
      <div class="icard-choices">
        <button
          v-for="opt in allowOptions"
          :key="opt.value"
          class="icard-btn mono"
          :class="kindClass(opt.kind)"
          type="button"
          :disabled="disabled"
          @click="submit(opt.value)"
        >
          {{ optLabel(opt) }}
        </button>
      </div>
      <div class="icard-choices">
        <button
          v-for="opt in rejectOptions"
          :key="opt.value"
          class="icard-btn mono icard-btn--ghost"
          type="button"
          :disabled="disabled"
          @click="submit(opt.value)"
        >
          {{ optLabel(opt) }}
        </button>
        <button
          v-for="opt in otherOptions"
          :key="opt.value"
          class="icard-btn mono"
          type="button"
          :disabled="disabled"
          @click="submit(opt.value)"
        >
          {{ optLabel(opt) }}
        </button>
      </div>
    </div>

    <!-- question：文本输入 + 提交 -->
    <div v-else-if="pending && interaction.type === 'question'" class="icard-body">
      <label class="icard-label" :for="`ia-${interaction.id}`">回答</label>
      <div class="icard-row">
        <input
          :id="`ia-${interaction.id}`"
          v-model="text"
          class="icard-input mono"
          type="text"
          autocomplete="off"
          :disabled="disabled"
          @keydown.enter.prevent="submitText"
        />
        <button
          class="icard-btn icard-btn--primary mono"
          type="button"
          :disabled="disabled"
          @click="submitText"
        >
          提交
        </button>
      </div>
    </div>

    <!-- choice：选项按钮组 -->
    <div
      v-else-if="pending && interaction.type === 'choice'"
      class="icard-body icard-choices"
    >
      <button
        v-for="opt in interaction.options ?? []"
        :key="opt.value"
        class="icard-btn mono"
        type="button"
        :disabled="disabled"
        @click="submit(opt.value)"
      >
        {{ optLabel(opt) }}
      </button>
    </div>

    <!-- confirmation：确认 / 取消（仅 pending；cancelled/expired 已在上方拦截） -->
    <div v-else-if="pending" class="icard-body icard-confirm">
      <button
        class="icard-btn icard-btn--primary mono"
        type="button"
        :disabled="disabled"
        @click="submit(confirmYes)"
      >
        {{ confirmYesLabel }}
      </button>
      <button
        class="icard-btn icard-btn--ghost mono"
        type="button"
        :disabled="disabled"
        @click="submit(confirmNo)"
      >
        {{ confirmNoLabel }}
      </button>
    </div>

    <div v-if="canPunt" class="icard-footer">
      <button
        class="icard-punt mono"
        type="button"
        :disabled="disabled"
        @click="punt"
      >
        {{ submitting ? '提交中' : 'punt' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.icard {
  border: 1px solid var(--line);
  border-left: 2px solid var(--phosphor);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 12px 14px;
  margin-bottom: 10px;
  animation: icard-slide 0.22s ease-out;
}
.icard--answered {
  opacity: 0.6;
  border-left-color: var(--queue);
}

.icard-head {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 10px;
  letter-spacing: 0.06em;
  margin-bottom: 6px;
}
.icard-type {
  color: var(--phosphor);
  text-transform: uppercase;
}
.icard-id {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.icard-prompt {
  margin: 0 0 10px;
  color: var(--paper);
  font-size: 13px;
  line-height: 1.5;
  word-break: break-word;
}

.icard-done {
  margin: 0;
  font-size: 12px;
  color: var(--queue);
}
.icard-answer {
  color: var(--phosphor);
}
.icard-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 6px 0 0;
  color: var(--queue);
  font-size: 11px;
}
.icard-mark {
  border: 1px solid var(--fail);
  border-radius: 9px;
  color: var(--fail);
  padding: 0 6px;
}

.icard-body {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

/* 审批卡（GATE-01）：工具调用摘要 + 原始输入片段 + 分组选项按钮 */
.icard-approve {
  gap: 8px;
}
.icard-tool {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px;
  font-size: 12px;
}
.icard-kind {
  border: 1px solid var(--phosphor);
  border-radius: 9px;
  color: var(--phosphor);
  font-size: 10px;
  letter-spacing: 0.06em;
  padding: 0 6px;
  text-transform: uppercase;
}
.icard-tool-title {
  color: var(--paper);
  word-break: break-word;
}
.icard-tool-path {
  color: var(--queue);
  font-size: 11px;
  word-break: break-all;
}
.icard-raw {
  margin: 0;
  padding: 6px 8px;
  max-height: 120px;
  overflow: auto;
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
  font-size: 11px;
  white-space: pre-wrap;
  word-break: break-all;
}
.icard-label {
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
  text-transform: uppercase;
}
.icard-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.icard-input {
  flex: 1;
  min-width: 0;
  background: var(--term-bg);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 6px 10px;
  font-size: 12px;
}
.icard-input:focus {
  border-color: var(--phosphor);
}
.icard-input:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.icard-choices,
.icard-confirm {
  flex-direction: row;
  flex-wrap: wrap;
  gap: 8px;
}

.icard-btn {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px 14px;
  font-size: 12px;
  flex: none;
  transition: background 0.15s, color 0.15s, border-color 0.15s;
}
.icard-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.icard-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.icard-btn--primary {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.icard-btn--primary:hover:not(:disabled) {
  background: var(--phosphor);
  color: var(--ink);
}
.icard-btn--ghost {
  color: var(--queue);
}
.icard-footer {
  display: flex;
  justify-content: flex-end;
  margin-top: 8px;
}
.icard-punt {
  background: transparent;
  border: none;
  color: var(--queue);
  padding: 0;
  font-size: 11px;
}
.icard-punt:hover:not(:disabled) {
  color: var(--run);
}
.icard-punt:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

@keyframes icard-slide {
  from {
    opacity: 0;
    transform: translateY(-6px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
@media (prefers-reduced-motion: reduce) {
  .icard {
    animation: none;
  }
}
</style>
