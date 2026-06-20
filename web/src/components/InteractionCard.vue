<script setup lang="ts">
// 运行中交互卡：question 文本问答 / choice 选项 / confirmation 确认。
//  - pending：可输入/点选，emit answer(value)；
//  - answered：整卡只读，展示「已提交：{answer}」；
//  - submitting：禁用全部输入/按钮，防重复提交。
// 视觉：--panel 底 + --line 边 + --phosphor 强调标题；id/type mono 小字。
// 一次性滑入动画，prefers-reduced-motion 下关闭。
import { computed, ref } from 'vue'
import type { Interaction } from '../api/types'

const props = defineProps<{ interaction: Interaction; submitting?: boolean }>()
const emit = defineEmits<{ (e: 'answer', value: string): void }>()

const answered = computed(() => props.interaction.status === 'answered')
const disabled = computed(() => !!props.submitting || answered.value)

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

// label 优先，回退 value
function optLabel(opt: { value: string; label?: string }): string {
  return opt.label ?? opt.value
}
</script>

<template>
  <div class="icard" :class="{ 'icard--answered': answered }">
    <div class="icard-head mono">
      <span class="icard-type">{{ interaction.type }}</span>
      <span class="icard-id">{{ interaction.id }}</span>
    </div>

    <p class="icard-prompt">{{ interaction.prompt }}</p>

    <!-- answered：只读回显 -->
    <p v-if="answered" class="icard-done mono">
      已提交：<span class="icard-answer">{{ interaction.answer }}</span>
    </p>

    <!-- question：文本输入 + 提交 -->
    <div v-else-if="interaction.type === 'question'" class="icard-body">
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
      v-else-if="interaction.type === 'choice'"
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

    <!-- confirmation：确认 / 取消 -->
    <div v-else class="icard-body icard-confirm">
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

.icard-body {
  display: flex;
  flex-direction: column;
  gap: 6px;
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
