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
import type { Interaction, InteractionOption } from '../api/types'
import { t } from '../i18n'
import { choiceAnswer, initialPicks, interactionAnswerText, isRecommended } from '../utils/interactionCard'

const props = defineProps<{
  interaction: Interaction
  submitting?: boolean
  isDecision?: boolean
  // 只读身份（调用方上限 read_only）：只展示问题，不给作答 / 转交入口（服务端也会 403）。
  readonly?: boolean
  // 「其他」输入框的提示语（默认通用文案；会话权限卡用它说明「填了就是附原因拒绝」）。
  otherLabel?: string
}>()
const emit = defineEmits<{
  (e: 'answer', value: string): void
  (e: 'punt'): void
}>()

const pending = computed(() => props.interaction.status === 'pending')
const answered = computed(() => props.interaction.status === 'answered')
const expired = computed(() => props.interaction.status === 'expired')
const disabled = computed(() => !!props.submitting || !pending.value || !!props.readonly)
const canPunt = computed(
  () =>
    pending.value &&
    !props.isDecision &&
    !props.readonly &&
    props.interaction.needs_human !== 1,
)
const answeredBy = computed(() => props.interaction.answered_by?.trim() || 'human')

// question：本地文本输入
const text = ref('')

// confirmation 默认 yes/no，options 非空则用 options 的 value 覆盖
const confirmYes = computed(() => props.interaction.options?.[0]?.value ?? 'yes')
const confirmNo = computed(() => props.interaction.options?.[1]?.value ?? 'no')
const confirmYesLabel = computed(
  () => props.interaction.options?.[0]?.label ?? t('util.icard.yes'),
)
const confirmNoLabel = computed(
  () => props.interaction.options?.[1]?.label ?? t('util.icard.no'),
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

// ---- choice 扩展（gofer_ask）：多选 / 「其他」自由文本 / 推荐项（T6 R11）----

const multi = computed(() => props.interaction.multi === true)
const allowOther = computed(() => props.interaction.allow_other === true)
const choiceOptions = computed(() => props.interaction.options ?? [])
// 列表模式：多选、有推荐项或选项带说明时改用「勾选 + 统一提交」——按钮里塞不下说明与
// 推荐徽标；普通 choice（无这些字段）保持点一下即作答的老样子。
const listChoice = computed(
  () =>
    multi.value ||
    (props.interaction.recommended ?? '') !== '' ||
    choiceOptions.value.some((o) => !!o.description),
)
// picks 存选项的 value，提交时才换成 label（后端两者都认，label 是人话）。
const picks = ref<string[]>(initialPicks(props.interaction))
const other = ref('')
const choiceValue = computed(() => choiceAnswer(props.interaction, picks.value, other.value))
const canSubmitChoice = computed(
  () => pending.value && !disabled.value && choiceValue.value !== null,
)
const otherPlaceholder = computed(() => props.otherLabel || t('util.icard.otherLabel'))

function togglePick(value: string): void {
  if (disabled.value) {
    return
  }
  if (multi.value) {
    picks.value = picks.value.includes(value)
      ? picks.value.filter((v) => v !== value)
      : [...picks.value, value]
    return
  }
  picks.value = [value]
}

function submitChoice(): void {
  const v = choiceValue.value
  if (v === null) {
    return
  }
  submit(v)
}

function isReco(opt: InteractionOption): boolean {
  return isRecommended(props.interaction, opt)
}

// 只读回显：多选的 JSON 数组转成人话
const finalAnswer = computed(() => interactionAnswerText(props.interaction))

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
  <div class="icard msg-body" :class="{ 'icard--answered': answered || expired }">
    <div class="icard-head mono">
      <span class="icard-type">{{ interaction.type }}</span>
      <span class="icard-id">{{ interaction.id }}</span>
    </div>

    <p class="icard-prompt">{{ interaction.prompt }}</p>

    <!-- answered：只读回显 -->
    <template v-if="answered">
      <p class="icard-done mono">
        {{ t('util.icard.submitted') }}<span class="icard-answer" data-test="icard-final-answer">{{ finalAnswer }}</span>
      </p>
      <p class="icard-meta mono">
        <span>answered_by {{ answeredBy }}</span>
        <span v-if="fmtTime(interaction.answered_at)">{{ fmtTime(interaction.answered_at) }}</span>
        <span v-if="interaction.needs_human === 1" class="icard-mark">needs_human</span>
      </p>
    </template>

    <!-- expired（decision 投影）：只读，无按钮、无假回显 -->
    <template v-else-if="expired">
      <p class="icard-done mono">{{ t('util.icard.expired') }}</p>
      <p class="icard-meta mono">
        <span v-if="fmtTime(interaction.created_at)">asked {{ fmtTime(interaction.created_at) }}</span>
      </p>
    </template>

    <!-- cancelled：只读，不渲染任何可点按钮 -->
    <template v-else-if="interaction.status === 'cancelled'">
      <p class="icard-done mono" data-test="icard-cancelled">{{ t('util.icard.cancelled') }}</p>
      <p v-if="finalAnswer" class="icard-meta mono"><span class="icard-answer">{{ finalAnswer }}</span></p>
    </template>

    <!-- 只读身份：只展示问题，不摆任何作答入口 -->
    <p v-if="pending && readonly" class="icard-meta mono" data-test="icard-readonly">{{ t('util.icard.readonly') }}</p>

    <!-- permission：审批卡（GATE-01 §1）——工具调用摘要 + 按 ACP kind 分组的选项按钮 -->
    <div v-if="pending && isPermission && !readonly" class="icard-body icard-approve">
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
        <span v-if="leftSec >= 0">{{ t('util.icard.countdown', { left: fmtLeft(leftSec) }) }}</span>
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
      <!-- 其他（自由文本）：会话权限卡用它写「附原因拒绝」的原因 -->
      <div v-if="allowOther" class="icard-row icard-other">
        <input
          v-model="other"
          class="icard-input mono"
          type="text"
          autocomplete="off"
          :placeholder="otherPlaceholder"
          :disabled="disabled"
          data-test="icard-other"
          @keydown.enter.prevent="submit(other.trim())"
        />
        <button
          class="icard-btn mono"
          type="button"
          :disabled="disabled || other.trim() === ''"
          @click="submit(other.trim())"
        >
          {{ t('util.icard.submit') }}
        </button>
      </div>
    </div>

    <!-- question：文本输入 + 提交 -->
    <div v-else-if="pending && interaction.type === 'question' && !readonly" class="icard-body">
      <label class="icard-label" :for="`ia-${interaction.id}`">{{ t('util.icard.answerLabel') }}</label>
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
          {{ t('util.icard.submit') }}
        </button>
      </div>
    </div>

    <!-- choice：勾选列表（多选 / 推荐项 / 带说明）或选项按钮组 -->
    <div
      v-else-if="pending && interaction.type === 'choice' && !readonly"
      class="icard-body"
    >
      <div v-if="listChoice" class="icard-opts" data-test="icard-choice-list">
        <label v-for="opt in choiceOptions" :key="opt.value" class="icard-opt">
          <input
            class="icard-pick"
            :type="multi ? 'checkbox' : 'radio'"
            :name="`ip-${interaction.id}`"
            :value="opt.value"
            :checked="picks.includes(opt.value)"
            :disabled="disabled"
            @change="togglePick(opt.value)"
          />
          <span class="icard-opt-text">
            <span class="icard-opt-head">
              <span class="icard-opt-label">{{ optLabel(opt) }}</span>
              <span v-if="isReco(opt)" class="icard-reco mono">{{ t('util.icard.recommended') }}</span>
            </span>
            <span v-if="opt.description" class="icard-opt-desc">{{ opt.description }}</span>
          </span>
        </label>
      </div>
      <div v-else class="icard-choices">
        <button
          v-for="opt in choiceOptions"
          :key="opt.value"
          class="icard-btn mono"
          type="button"
          :disabled="disabled"
          @click="submit(opt.value)"
        >
          {{ optLabel(opt) }}
          <span v-if="isReco(opt)" class="icard-reco mono">{{ t('util.icard.recommended') }}</span>
        </button>
      </div>
      <!-- 其他（自由文本）：填了以它为准（单选替换选项，多选作为其中一项） -->
      <div v-if="allowOther" class="icard-row icard-other">
        <input
          v-model="other"
          class="icard-input mono"
          type="text"
          autocomplete="off"
          :placeholder="otherPlaceholder"
          :disabled="disabled"
          data-test="icard-other"
          @keydown.enter.prevent="listChoice ? submitChoice() : submit(other.trim())"
        />
        <button
          v-if="!listChoice"
          class="icard-btn mono"
          type="button"
          :disabled="disabled || other.trim() === ''"
          @click="submit(other.trim())"
        >
          {{ t('util.icard.submit') }}
        </button>
      </div>
      <div v-if="listChoice" class="icard-row">
        <button
          class="icard-btn icard-btn--primary mono"
          type="button"
          :disabled="!canSubmitChoice"
          data-test="icard-choice-submit"
          @click="submitChoice"
        >
          {{ t('util.icard.submit') }}
        </button>
      </div>
    </div>

    <!-- confirmation：确认 / 取消（仅 pending；cancelled/expired 已在上方拦截） -->
    <div v-else-if="pending && !readonly" class="icard-body icard-confirm">
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

    <div v-if="canPunt || $slots.delegate" class="icard-footer">
      <span v-if="$slots.delegate" class="icard-delegate"><slot name="delegate" /></span>
      <button
        v-if="canPunt"
        class="icard-punt mono"
        type="button"
        :disabled="disabled"
        @click="punt"
      >
        {{ submitting ? t('util.icard.punting') : 'punt' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.icard {
  min-width: 0;
  border: 1px solid var(--line);
  border-left: 2px solid var(--accent);
  border-radius: var(--r-chip);
  background: var(--surface);
  padding: 12px 14px;
  margin-bottom: 10px;
  animation: icard-slide 0.22s ease-out;
}
.icard--answered {
  opacity: 0.6;
  border-left-color: var(--ink-3);
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
  color: var(--accent);
  text-transform: uppercase;
}
.icard-id {
  color: var(--ink-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.icard-prompt {
  margin: 0 0 10px;
  color: var(--ink);
  font-size: 13px;
  line-height: 1.5;
}

.icard-done {
  margin: 0;
  font-size: 12px;
  color: var(--ink-3);
}
.icard-answer {
  color: var(--accent);
}
.icard-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 6px 0 0;
  color: var(--ink-3);
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
  border: 1px solid var(--accent);
  border-radius: 9px;
  color: var(--accent);
  font-size: 10px;
  letter-spacing: 0.06em;
  padding: 0 6px;
  text-transform: uppercase;
}
.icard-tool-title {
  color: var(--ink);
  word-break: break-word;
}
.icard-tool-path {
  color: var(--ink-3);
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
  border-radius: var(--r-chip);
  color: var(--ink-3);
  font-size: 11px;
  white-space: pre-wrap;
}
.icard-label {
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--ink-3);
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
  background: var(--sunken);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 6px 10px;
  font-size: 12px;
}
.icard-input:focus {
  border-color: var(--accent);
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

/* choice 扩展：勾选列表（多选 / 推荐项 / 带说明的选项） */
.icard-opts {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.icard-opt {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 4px 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  font-size: 12px;
  cursor: pointer;
}
.icard-opt:hover {
  border-color: var(--accent);
}
.icard-pick {
  accent-color: var(--accent);
  flex: none;
  margin-top: 3px;
}
.icard-opt-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.icard-opt-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.icard-opt-label {
  color: var(--ink);
}
.icard-opt-desc {
  color: var(--ink-3);
  font-size: 11px;
  line-height: 1.4;
}
/* 推荐项徽标：与状态徽标同一形状，不抢主色 */
.icard-reco {
  border: 1px solid var(--done);
  border-radius: 9px;
  color: var(--done);
  font-size: 10px;
  letter-spacing: 0.06em;
  padding: 0 6px;
}
.icard-other {
  margin-top: 2px;
}

.icard-btn {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 5px 14px;
  font-size: 12px;
  flex: none;
  transition: background 0.15s, color 0.15s, border-color 0.15s;
}
.icard-btn:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.icard-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.icard-btn--primary {
  border-color: var(--accent);
  color: var(--accent);
}
.icard-btn--primary:hover:not(:disabled) {
  background: var(--accent);
  color: var(--bg);
}
.icard-btn--ghost {
  color: var(--ink-3);
}
.icard-footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 8px;
}
.icard-delegate {
  display: inline-flex;
  align-items: center;
}
.icard-punt {
  background: transparent;
  border: none;
  color: var(--ink-3);
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
